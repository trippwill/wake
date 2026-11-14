// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.trippwill.dev/wake/internal/clientaccess"
	"golang.org/x/crypto/argon2"
)

const (
	sessionCookie = "wake_admin_session"
	keySize       = 32
	saltSize      = 16
	argonTime     = 3
	argonMemory   = 64 * 1024
	argonThreads  = 4
	// Form encoding can use three bytes per byte of the unlock.
	maxLoginBodyBytes = int64(len("unlock=") + 3*MaxUnlockBytes)
	maxUnlockAttempts = 2
	unlockInterval    = time.Second
)

// MinimumUnlockBytes is the minimum length accepted during local provisioning.
const MinimumUnlockBytes = 12

// MaxUnlockBytes leaves room for the newline and a truncation check in Linux
// canonical terminal input, whose full line buffer is 4096 bytes.
const MaxUnlockBytes = (4 << 10) - 2

type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrInvalidAuthConfiguration Error = "invalid admin authentication configuration"
	ErrWeakUnlock               Error = "operator unlock must contain at least 12 bytes"
	ErrLongUnlock               Error = "operator unlock exceeds the input limit"
)

type verifier struct {
	salt [saltSize]byte
	hash [keySize]byte
}

type authenticator struct {
	origin      *url.URL
	state       clientaccess.FileStore
	unlockSlots chan struct{}
	unlockMu    sync.Mutex
	nextUnlock  time.Time
	now         func() time.Time
	lifetime    time.Duration
}

// SetUnlock replaces the local operator unlock and invalidates existing sessions.
func SetUnlock(configDir string, unlock []byte) error {
	if len(unlock) < MinimumUnlockBytes {
		return ErrWeakUnlock
	}

	if len(unlock) > MaxUnlockBytes {
		return ErrLongUnlock
	}

	var salt [saltSize]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return err
	}

	hash := argon2.IDKey(unlock, salt[:], argonTime, argonMemory, argonThreads, keySize)
	defer clear(hash)

	record := "v1." + base64.RawURLEncoding.
		EncodeToString(salt[:]) + "." + base64.RawURLEncoding.
		EncodeToString(hash)

	return clientaccess.NewFileStore(configDir).SetBrowser(record)
}

// Open validates the initial auth file and supports an unprovisioned browser listener.
func Open(configDir, origin string) (*authenticator, bool, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, false, ErrInvalidAuthConfiguration
	}

	auth := &authenticator{
		origin: parsed, state: clientaccess.NewFileStore(configDir),
		unlockSlots: make(chan struct{}, maxUnlockAttempts),
		now:         time.Now, lifetime: 8 * time.Hour,
	}

	record, key, err := auth.state.Browser()
	clear(key[:])

	return auth, record != "", err
}

func (a *authenticator) login(w http.ResponseWriter, value string) bool {
	if !a.reserveUnlockAttempt() {
		return false
	}

	record, key, err := a.state.Browser()
	defer clear(key[:])

	if err != nil || record == "" {
		return false
	}

	select {
	case a.unlockSlots <- struct{}{}:
		defer func() { <-a.unlockSlots }()
	default:
		return false
	}

	v, err := parseVerifier([]byte(record))
	if err != nil {
		return false
	}

	derived := argon2.IDKey([]byte(value), v.salt[:], argonTime, argonMemory, argonThreads, keySize)
	defer clear(derived)

	if subtle.ConstantTimeCompare(derived, v.hash[:]) != 1 {
		return false
	}

	// Sign with the same snapshot that verified the unlock.
	a.issue(w, key[:])

	return true
}

func (a *authenticator) reserveUnlockAttempt() bool {
	a.unlockMu.Lock()
	defer a.unlockMu.Unlock()

	now := a.now()
	if now.Before(a.nextUnlock) {
		return false
	}

	a.nextUnlock = now.Add(unlockInterval)

	return true
}

func (a *authenticator) issue(w http.ResponseWriter, key []byte) string {
	csrf := make([]byte, keySize)
	// crypto/rand.Read fills the buffer or terminates the process.
	_, _ = rand.Read(csrf)
	defer clear(csrf)

	expires := a.now().Add(a.lifetime).Unix()
	payload := fmt.Sprintf("%d.%s", expires, base64.RawURLEncoding.EncodeToString(csrf))
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	value := payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	//nolint:gosec // Cookie security follows the configured browser origin, independently of backend TCP.
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     sessionCookie,
			Value:    value,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			Secure:   a.origin.Scheme == "https",
			MaxAge:   int(a.lifetime.Seconds()),
		})

	return base64.RawURLEncoding.EncodeToString(csrf)
}

func (a *authenticator) session(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}

	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 {
		return "", false
	}

	payload := strings.Join(parts[:2], ".")

	supplied, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", false
	}
	defer clear(supplied)

	record, key, err := a.state.Browser()
	if err != nil || record == "" {
		return "", false
	}
	defer clear(key[:])

	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(supplied, mac.Sum(nil)) {
		return "", false
	}

	var expires int64
	if _, err = fmt.Sscanf(parts[0], "%d", &expires); err != nil || a.now().Unix() > expires {
		return "", false
	}

	csrf, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(csrf) != keySize {
		clear(csrf)
		return "", false
	}

	clear(csrf)

	return parts[1], true
}

func (a *authenticator) correctHost(r *http.Request) bool { return r.Host == a.origin.Host }
func (a *authenticator) sameOrigin(r *http.Request) bool {
	return a.correctHost(r) && r.Header.Get("Origin") == a.origin.String()
}

func parseVerifier(value []byte) (verifier, error) {
	parts := strings.Split(string(value), ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return verifier{}, clientaccess.ErrInvalidSecretFile
	}

	salt, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(salt) != saltSize {
		return verifier{}, clientaccess.ErrInvalidSecretFile
	}

	hash, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(hash) != keySize {
		return verifier{}, clientaccess.ErrInvalidSecretFile
	}

	var result verifier
	copy(result.salt[:], salt)
	copy(result.hash[:], hash)
	clear(salt)
	clear(hash)

	return result, nil
}

func clear(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
