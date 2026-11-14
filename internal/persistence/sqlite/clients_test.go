// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/clientaccess"
	"go.trippwill.dev/wake/internal/transport/financialapi"
)

func TestClientRevocationStreamsAndPersistence(t *testing.T) {
	store, path := testStore(t)
	auth := testAuthStore(t)
	manager := clientaccess.New(auth)
	issue := func(name string) (clientaccess.Client, string) {
		t.Helper()

		c, s, err := manager.Create(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Clear()

		v, err := s.Copy()
		if err != nil {
			t.Fatal(err)
		}
		defer clear(v)

		return c, string(v)
	}
	first, token := issue("First")
	_, second := issue("Second")

	verifier, err := auth.ClientVerifier(t.Context(), first.ID())
	if err != nil || bytes.Contains(verifier, []byte(token)) || len(verifier) != 32 {
		t.Fatal("stored verifier", err)
	}

	server := httptest.NewServer(financialapi.Authenticated(manager, financialapi.NewHandler(store, nil, nil)))
	defer server.Close()

	request := func(path, token string) *http.Response {
		t.Helper()

		r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}

		r.Header.Set("Authorization", "Bearer "+token)

		res, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}

		return res
	}
	for _, path := range []string{"/v1/snapshot", "/v1/events", "/v1/accounts/missing", "/v1/accounts/missing/positions"} {
		res := request(path, "invalid")
		_ = res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatal(path, res.Status)
		}
	}

	stream := request("/v1/events", token)

	reader := bufio.NewReader(stream.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != "event: publication\n" {
		t.Fatal(line, err)
	}

	closed := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, reader); closed <- err }()

	if err := manager.Revoke(t.Context(), first.ID()); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoked stream remained open")
	}

	_ = stream.Body.Close()
	for _, path := range []string{"/v1/snapshot", "/v1/events"} {
		res := request(path, token)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatal("revoked credential", res.Status)
		}
	}

	res := request("/v1/snapshot", second)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatal("other client revoked")
	}

	server.Close()

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(t.Context(), testProfile(path), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	manager = clientaccess.New(auth)
	if _, _, err := manager.Admit(t.Context(), token); !errors.Is(err, clientaccess.ErrUnauthorized) {
		t.Fatal("revocation lost", err)
	}

	_, release, err := manager.Admit(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}

	release()

	if err := auth.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, _, err := manager.Admit(t.Context(), second); !errors.Is(err, clientaccess.ErrUnauthorized) {
		t.Fatal("recovery retained grant", err)
	}
}

func TestClientConcurrentAdmissionAndRevocation(t *testing.T) {
	m := clientaccess.New(testAuthStore(t))

	c, secret, err := m.Create(t.Context(), "Concurrent")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Clear()

	value, err := secret.Copy()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(value)

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 10 {
				ctx, done, err := m.Admit(t.Context(), string(value))
				if err == nil {
					select {
					case <-ctx.Done():
					default:
					}

					done()
				} else if !errors.Is(err, clientaccess.ErrUnauthorized) && !errors.Is(err, clientaccess.ErrBusy) {
					t.Error(err)
				}
			}
		})
	}

	if err := m.Revoke(t.Context(), c.ID()); err != nil {
		t.Fatal(err)
	}

	wg.Wait()

	if _, _, err := m.Admit(t.Context(), string(value)); !errors.Is(err, clientaccess.ErrUnauthorized) {
		t.Fatal("post-revocation admission", err)
	}

	if _, _, err := m.Create(
		t.Context(),
		strings.Repeat("x", 101),
	); !errors.Is(
		err,
		clientaccess.ErrInvalidClient,
	) {
		t.Fatal(err)
	}
}

func TestClientLimitsAndInvalidAuthStore(t *testing.T) {
	auth, authPath := testAuthFile(t)
	m := clientaccess.New(auth)
	issue := func(name string) (clientaccess.Client, string) {
		t.Helper()

		c, s, err := m.Create(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Clear()

		v, err := s.Copy()
		if err != nil {
			t.Fatal(err)
		}
		defer clear(v)

		return c, string(v)
	}
	first, token := issue("First")

	var release []func()
	defer func() {
		for _, done := range release {
			done()
		}
	}()

	for range clientaccess.MaxClientRequests {
		_, done, err := m.Admit(t.Context(), token)
		if err != nil {
			t.Fatal(err)
		}

		release = append(release, done)
	}

	if _, _, err := m.Admit(t.Context(), token); !errors.Is(err, clientaccess.ErrBusy) {
		t.Fatal("client limit", err)
	}

	var other string
	for i := 1; i < clientaccess.MaxRequests/clientaccess.MaxClientRequests; i++ {
		_, other = issue("Other")
		for range clientaccess.MaxClientRequests {
			_, done, err := m.Admit(t.Context(), other)
			if err != nil {
				t.Fatal(err)
			}

			release = append(release, done)
		}
	}

	_, other = issue("Waiting")
	if _, _, err := m.Admit(t.Context(), other); !errors.Is(err, clientaccess.ErrBusy) {
		t.Fatal("instance limit", err)
	}

	release[0]()

	_, done, err := m.Admit(t.Context(), other)
	if err != nil {
		t.Fatal(err)
	}

	done()

	for _, done := range release {
		done()
	}

	clients, err := m.Clients(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for i := len(clients); i < clientaccess.MaxClients; i++ {
		issue("Enrollment")
	}

	if _, _, err := m.Create(t.Context(), "Excess"); !errors.Is(err, clientaccess.ErrBusy) {
		t.Fatal("enrollment limit", err)
	}

	if err := m.Revoke(t.Context(), first.ID()); err != nil {
		t.Fatal(err)
	}

	_, other = issue("Replacement")

	// #nosec G302 -- Deliberately insecure mode exercises fail-closed reads and writes.
	if err := os.Chmod(authPath, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := m.Admit(t.Context(), other); err == nil {
		t.Fatal("invalid store granted access")
	}

	if err := m.Revoke(t.Context(), first.ID()); err == nil {
		t.Fatal("invalid store reported durable revocation")
	}
}

func TestClientHTTPBoundary(t *testing.T) {
	store, _ := testStore(t)
	auth, authPath := testAuthFile(t)
	manager := clientaccess.New(auth)

	client, secret, err := manager.Create(t.Context(), "Reader")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Clear()

	value, err := secret.Copy()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(value)

	token := string(value)
	handler := financialapi.Authenticated(manager, financialapi.NewHandler(store, nil, nil))

	request := func(path string, headers []string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header["Authorization"] = headers
		r.Header.Set("If-None-Match", "*")
		r.AddCookie(
			&http.Cookie{
				Name:     "wake_session",
				Value:    token,
				Secure:   true,
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			},
		)

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		return w
	}
	for _, headers := range [][]string{nil, {"Basic " + token}, {"Bearer " + token, "Bearer " + token}, {"Bearer " + token + ", Bearer " + token}, {"Bearer " + token + " "}, {"Bearer " + token[:len(token)-1] + "B"}} {
		response := request("/v1/snapshot?token="+token, headers)
		if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "no-store" ||
			response.Header().Get("WWW-Authenticate") == "" {
			t.Fatal("invalid authentication accepted", response.Code)
		}

		if strings.Contains(response.Body.String(), token) {
			t.Fatal("credential disclosed in error")
		}
	}

	if w := request("/v1/snapshot", []string{"bearer " + token}); w.Code != http.StatusNotModified {
		t.Fatal("conditional authenticated read", w.Code)
	}

	other := testAuthStore(t)
	if _, _, err := clientaccess.New(other).
		Admit(t.Context(), token); !errors.Is(
		err,
		clientaccess.ErrUnauthorized,
	) {
		t.Fatal("wrong instance accepted grant", err)
	}

	if err := manager.Revoke(t.Context(), client.ID()); err != nil {
		t.Fatal(err)
	}

	if w := request("/v1/snapshot", []string{"Bearer " + token}); w.Code != http.StatusUnauthorized {
		t.Fatal("validator bypassed revocation", w.Code)
	}

	// #nosec G302 -- Deliberately insecure mode exercises fail-closed reads and writes.
	if err := os.Chmod(authPath, 0o644); err != nil {
		t.Fatal(err)
	}

	if w := request("/v1/snapshot", []string{"Bearer " + token}); w.Code != http.StatusServiceUnavailable {
		t.Fatal("storage failure did not close access", w.Code)
	}
}

func testAuthStore(t *testing.T) clientaccess.FileStore {
	t.Helper()
	store, _ := testAuthFile(t)

	return store
}

func testAuthFile(t *testing.T) (clientaccess.FileStore, string) {
	t.Helper()

	path := authFixturePath(t)

	return clientaccess.NewFileStore(filepath.Dir(path)), path
}

func authFixturePath(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	return filepath.Join(dir, clientaccess.FileName)
}
