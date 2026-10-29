// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/argon2"
)

type issuedSessionKey struct{}

func (a *authenticator) establishSession(w http.ResponseWriter, r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), issuedSessionKey{}, a.issue(w)))
}

const (
	sessionCookie     = "wake_admin_session"
	unlockFile        = "admin-unlock"
	sessionKeyFile    = "admin-session.key"
	proxyGuardFile    = "admin-proxy.key"
	keySize           = 32
	saltSize          = 16
	argonTime         = 3
	argonMemory       = 64 * 1024
	argonThreads      = 4
	maxLoginBodyBytes = 4 << 10
	maxUnlockAttempts = 2
	unlockInterval    = time.Second
)

// MinimumUnlockBytes is the minimum length accepted during local provisioning.
const MinimumUnlockBytes = 12

type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrInvalidAuthConfiguration Error = "invalid admin authentication configuration"
	ErrInvalidSecretFile        Error = "invalid admin secret file"
	ErrWeakUnlock               Error = "operator unlock must contain at least 12 bytes"
)

type verifier struct {
	salt [saltSize]byte
	hash [keySize]byte
}

type authenticator struct {
	mode        string
	origin      *url.URL
	verifier    verifier
	sessionKey  []byte
	proxyGuard  []byte
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

	var salt [saltSize]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return err
	}

	hash := argon2.IDKey(unlock, salt[:], argonTime, argonMemory, argonThreads, keySize)
	defer clear(hash)

	record := "v1." + base64.RawURLEncoding.
		EncodeToString(salt[:]) + "." + base64.RawURLEncoding.
		EncodeToString(hash)

	key := randomKey()
	if key == nil {
		return ErrInvalidSecretFile
	}
	defer clear(key)

	if err := writePrivateFile(filepath.Join(configDir, sessionKeyFile), key); err != nil {
		return err
	}

	return writePrivateFile(filepath.Join(configDir, unlockFile), []byte(record))
}

// SetProxyGuard creates or rotates the reverse-proxy guard and session key.
func SetProxyGuard(configDir string) (string, error) {
	guard := randomKey()
	key := randomKey()
	if guard == nil || key == nil {
		return "", ErrInvalidSecretFile
	}
	defer clear(guard)
	defer clear(key)

	if err := writePrivateFile(filepath.Join(configDir, sessionKeyFile), key); err != nil {
		return "", err
	}

	if err := writePrivateFile(filepath.Join(configDir, proxyGuardFile), guard); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(guard), nil
}

func randomKey() []byte {
	value := make([]byte, keySize)
	if _, err := rand.Read(value); err != nil {
		return nil
	}

	return value
}

// Open loads the selected administrator gate. It reports enabled false when
// that gate has not yet been provisioned.
func Open(configDir, mode, origin string) (*authenticator, bool, error) {
	parsedOrigin, err := url.Parse(origin)
	if err != nil || parsedOrigin.Scheme == "" || parsedOrigin.Host == "" {
		return nil, false, ErrInvalidAuthConfiguration
	}

	sessionKey, err := readPrivateFile(filepath.Join(configDir, sessionKeyFile), keySize)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, err
	}

	auth := &authenticator{
		mode:        mode,
		origin:      parsedOrigin,
		sessionKey:  sessionKey,
		unlockSlots: make(chan struct{}, maxUnlockAttempts),
		now:         time.Now,
		lifetime:    8 * time.Hour,
	}

	switch mode {
	case "local":
		value, readErr := readPrivateFile(filepath.Join(configDir, unlockFile), -1)
		if errors.Is(readErr, os.ErrNotExist) {
			auth.Close()
			return nil, false, nil
		}

		if readErr != nil {
			auth.Close()
			return nil, false, readErr
		}

		auth.verifier, readErr = parseVerifier(value)
		clear(value)

		if readErr != nil {
			auth.Close()
			return nil, false, readErr
		}
	case "proxy":
		auth.proxyGuard, err = readPrivateFile(filepath.Join(configDir, proxyGuardFile), keySize)
		if errors.Is(err, os.ErrNotExist) {
			auth.Close()
			return nil, false, nil
		}

		if err != nil {
			auth.Close()
			return nil, false, err
		}
	default:
		auth.Close()
		return nil, false, ErrInvalidAuthConfiguration
	}

	return auth, true, nil
}

func (a *authenticator) Close() {
	if a == nil {
		return
	}

	clear(a.sessionKey)
	clear(a.proxyGuard)
	clear(a.verifier.salt[:])
	clear(a.verifier.hash[:])
}

func (a *authenticator) verifyUnlock(value string) bool {
	if !a.reserveUnlockAttempt() {
		return false
	}

	select {
	case a.unlockSlots <- struct{}{}:
		defer func() { <-a.unlockSlots }()
	default:
		return false
	}

	derived := argon2.IDKey([]byte(value), a.verifier.salt[:], argonTime, argonMemory, argonThreads, keySize)
	defer clear(derived)

	return subtle.ConstantTimeCompare(derived, a.verifier.hash[:]) == 1
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

func (a *authenticator) proxyAuthorized(r *http.Request) bool {
	if a.mode != "proxy" {
		return true
	}

	supplied, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Wake-Proxy-Authorization"))
	if err != nil {
		return false
	}
	defer clear(supplied)

	return subtle.ConstantTimeCompare(supplied, a.proxyGuard) == 1
}

func (a *authenticator) issue(w http.ResponseWriter) string {
	csrf := make([]byte, keySize)
	// crypto/rand.Read fills the buffer or terminates the process.
	_, _ = rand.Read(csrf)
	defer clear(csrf)

	expires := a.now().Add(a.lifetime).Unix()
	payload := fmt.Sprintf("%d.%s", expires, base64.RawURLEncoding.EncodeToString(csrf))
	mac := hmac.New(sha256.New, a.sessionKey)
	_, _ = mac.Write([]byte(payload))
	value := payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	//nolint:gosec // Local mode is a loopback-only HTTP listener; proxy mode has an HTTPS public origin.
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
	if csrf, ok := r.Context().Value(issuedSessionKey{}).(string); ok {
		return csrf, true
	}

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

	mac := hmac.New(sha256.New, a.sessionKey)
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
		return verifier{}, ErrInvalidSecretFile
	}

	salt, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(salt) != saltSize {
		return verifier{}, ErrInvalidSecretFile
	}

	hash, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(hash) != keySize {
		return verifier{}, ErrInvalidSecretFile
	}

	var result verifier
	copy(result.salt[:], salt)
	copy(result.hash[:], hash)
	clear(salt)
	clear(hash)

	return result, nil
}

func readPrivateFile(path string, size int) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrInvalidSecretFile
	}

	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}

	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, ErrInvalidSecretFile
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 ||
		info.Mode().Perm()&0o400 == 0 {
		return nil, ErrInvalidSecretFile
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (int(stat.Uid) != 0 && int(stat.Uid) != os.Geteuid()) {
		return nil, ErrInvalidSecretFile
	}

	limit := int64(4097)
	if size >= 0 {
		limit = int64(size + 1)
	}

	value, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return nil, err
	}

	if (size >= 0 && len(value) != size) || (size < 0 && len(value) == int(limit)) {
		clear(value)
		return nil, ErrInvalidSecretFile
	}

	return value, nil
}

func writePrivateFile(path string, value []byte) error {
	if !filepath.IsAbs(path) || len(value) == 0 {
		return ErrInvalidSecretFile
	}

	file, err := os.CreateTemp(filepath.Dir(path), ".wake-admin-")
	if err != nil {
		return err
	}

	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()

	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(value)
	}

	if err == nil {
		err = file.Sync()
	}

	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return err
	}

	return os.Rename(temporary, path)
}

func clear(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
