// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bytes"
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
	"unicode/utf8"

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
	// Form encoding can use three bytes per password byte.
	maxLoginBodyBytes    = int64(len("password=") + 3*MaxPasswordBytes)
	maxPasswordBodyBytes = 64 << 10
	maxPasswordAttempts  = 2
	attemptInterval      = time.Second
)

const (
	MinimumPasswordBytes = 12
	MaxPasswordBytes     = (4 << 10) - 2
)

type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrInvalidAuthConfiguration Error = "invalid admin authentication configuration"
	ErrWeakPassword             Error = "password must contain at least 12 bytes"
	ErrLongPassword             Error = "password exceeds the input limit"
	ErrInvalidPassword          Error = "password must be valid UTF-8 on one line"
)

type verifier struct {
	salt [saltSize]byte
	hash [keySize]byte
}

type authenticator struct {
	origin        *url.URL
	state         clientaccess.FileStore
	passwordSlots chan struct{}
	attemptMu     sync.Mutex
	nextAttempt   time.Time
	sessionMu     sync.Mutex
	sessions      map[string]browserSession
	nextSweep     int64
	now           func() time.Time
	lifetime      time.Duration
}

type browserSession struct {
	expires int64
	done    chan struct{}
}

// SetPassword sets the operator password and invalidates existing browser sessions.
func SetPassword(configDir string, password []byte) error {
	record, err := passwordVerifier(password)
	if err != nil {
		return err
	}

	return clientaccess.NewFileStore(configDir).SetBrowser(record)
}

func passwordVerifier(password []byte) (string, error) {
	if len(password) < MinimumPasswordBytes {
		return "", ErrWeakPassword
	}

	if len(password) > MaxPasswordBytes {
		return "", ErrLongPassword
	}

	if !utf8.Valid(password) || bytes.ContainsAny(password, "\x00\r\n") {
		return "", ErrInvalidPassword
	}

	var salt [saltSize]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}

	hash := argon2.IDKey(password, salt[:], argonTime, argonMemory, argonThreads, keySize)
	defer clear(hash)

	return "v1." + base64.RawURLEncoding.EncodeToString(
		salt[:],
	) + "." + base64.RawURLEncoding.EncodeToString(
		hash,
	), nil
}

// Open validates the initial auth file and supports an unprovisioned browser listener.
func Open(configDir, origin string) (*authenticator, bool, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" {
		return nil, false, ErrInvalidAuthConfiguration
	}

	auth := &authenticator{
		origin: parsed, state: clientaccess.NewFileStore(configDir),
		passwordSlots: make(chan struct{}, maxPasswordAttempts),
		sessions:      make(map[string]browserSession),
		now:           time.Now, lifetime: 8 * time.Hour,
	}

	record, key, err := auth.state.Browser()
	clear(key[:])

	return auth, record != "", err
}

func (a *authenticator) login(w http.ResponseWriter, value string) bool {
	if !a.reserveAttempt() {
		return false
	}

	record, key, err := a.state.Browser()
	defer clear(key[:])

	if err != nil || record == "" {
		return false
	}

	if !a.tryPasswordWork() {
		return false
	}
	defer a.finishPasswordWork()

	if !matchesPassword(record, value) {
		return false
	}

	// Sign with the same snapshot that verified the password.
	a.issue(w, key[:])

	return true
}

func matchesPassword(record, value string) bool {
	if len(value) > MaxPasswordBytes {
		return false
	}

	v, err := parseVerifier([]byte(record))
	if err != nil {
		return false
	}

	derived := argon2.IDKey([]byte(value), v.salt[:], argonTime, argonMemory, argonThreads, keySize)
	defer clear(derived)

	return subtle.ConstantTimeCompare(derived, v.hash[:]) == 1
}

func (a *authenticator) tryPasswordWork() bool {
	select {
	case a.passwordSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (a *authenticator) finishPasswordWork() { <-a.passwordSlots }

func (a *authenticator) reserveAttempt() bool {
	a.attemptMu.Lock()
	defer a.attemptMu.Unlock()

	now := a.now()
	if now.Before(a.nextAttempt) {
		return false
	}

	a.nextAttempt = now.Add(attemptInterval)

	return true
}

func (a *authenticator) issue(w http.ResponseWriter, key []byte) string {
	csrf := make([]byte, keySize)
	// crypto/rand.Read fills the buffer or terminates the process.
	_, _ = rand.Read(csrf)
	defer clear(csrf)

	now := a.now()
	expires := now.Add(a.lifetime).Unix()
	id := base64.RawURLEncoding.EncodeToString(csrf)
	a.registerSession(id, expires, now.Unix())

	payload := fmt.Sprintf("%d.%s", expires, id)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	value := payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	http.SetCookie(
		w,
		//nolint:gosec // The admin listener uses HTTP on IPv4 loopback.
		&http.Cookie{
			Name:     sessionCookie,
			Value:    value,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   int(a.lifetime.Seconds()),
		})

	return id
}

func (a *authenticator) registerSession(id string, expires, now int64) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	if now >= a.nextSweep {
		for id, session := range a.sessions {
			if now >= session.expires {
				delete(a.sessions, id)
				close(session.done)
			}
		}

		a.nextSweep = now + 60
	}

	a.sessions[id] = browserSession{expires: expires, done: make(chan struct{})}
}

func (a *authenticator) sessionDone(id string) <-chan struct{} {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	return a.sessions[id].done
}

func (a *authenticator) revokeSession(id string) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	if session, ok := a.sessions[id]; ok {
		delete(a.sessions, id)
		close(session.done)
	}
}

func (a *authenticator) session(r *http.Request) (string, bool) {
	record, key, err := a.state.Browser()
	defer clear(key[:])

	if err != nil || record == "" {
		return "", false
	}

	return a.sessionWithKey(r, key[:])
}

func (a *authenticator) sessionWithKey(r *http.Request, key []byte) (string, bool) {
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

	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(supplied, mac.Sum(nil)) {
		return "", false
	}

	var expires int64
	if _, err = fmt.Sscanf(parts[0], "%d", &expires); err != nil || a.now().Unix() >= expires {
		return "", false
	}

	csrf, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(csrf) != keySize {
		clear(csrf)
		return "", false
	}

	clear(csrf)

	if a.sessionDone(parts[1]) == nil {
		return "", false
	}

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
