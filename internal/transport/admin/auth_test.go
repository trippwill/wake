// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/clientaccess"
)

func TestOpenReportsUnprovisionedLocalMode(t *testing.T) {
	auth, enabled, err := Open(privateAuthDir(t), "http://127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}

	if auth == nil || enabled {
		t.Fatalf("Open() = (%#v, %t), want (non-nil, false)", auth, enabled)
	}
}

func TestOpenRejectsNamedPipes(t *testing.T) {
	if directory := os.Getenv("WAKE_TEST_ADMIN_FIFO_DIR"); directory != "" {
		_, _, err := Open(directory, "http://127.0.0.1:8787")

		if !errors.Is(err, clientaccess.ErrInvalidSecretFile) {
			t.Fatalf("Open() error = %v, want clientaccess.ErrInvalidSecretFile", err)
		}

		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{clientaccess.FileName} {
		t.Run(name, func(t *testing.T) {
			directory := privateAuthDir(t)
			if err := SetUnlock(directory, []byte("correct horse battery staple")); err != nil {
				t.Fatal(err)
			}

			path := filepath.Join(directory, name)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}

			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			// A blocking open must fail this test without hanging the parent process.
			command := exec.CommandContext(ctx, executable, "-test.run=^TestOpenRejectsNamedPipes$")

			command.Env = append(os.Environ(),
				"WAKE_TEST_ADMIN_FIFO_DIR="+directory,
			)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("secret FIFO was not rejected: %v\n%s", err, output)
			}
		})
	}
}

func TestSetUnlockRejectsWeakValue(t *testing.T) {
	if err := SetUnlock(privateAuthDir(t), []byte("too short")); !errors.Is(err, ErrWeakUnlock) {
		t.Fatalf("SetUnlock() error = %v, want ErrWeakUnlock", err)
	}
}

func TestBrowserLoginAcceptsLongestEncodedUnlock(t *testing.T) {
	const origin = "http://127.0.0.1:8787"

	dir := privateAuthDir(t)
	unlock := strings.Repeat("&", MaxUnlockBytes)

	if err := SetUnlock(dir, []byte(unlock+"&")); !errors.Is(err, ErrLongUnlock) {
		t.Fatal("accepted an oversized unlock", err)
	}

	if err := SetUnlock(dir, []byte(unlock)); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, origin)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerLoginRoutes(mux, auth)

	form := url.Values{"unlock": {unlock}}.Encode()
	req := httptest.NewRequest(http.MethodPost, origin+"/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)

	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)

	response := res.Result()
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusSeeOther || len(response.Cookies()) != 1 {
		t.Fatal("valid unlock rejected after form encoding", res.Code)
	}
}

func TestRotateSessionsRejectsOldCookieAndRetainsUnlock(t *testing.T) {
	dir := privateAuthDir(t)
	unlock := []byte("synthetic rotation unlock")
	if err := SetUnlock(dir, unlock); err != nil {
		t.Fatal(err)
	}

	auth, enabled, err := Open(dir, "http://127.0.0.1:8787")
	if err != nil || !enabled {
		t.Fatal(enabled, err)
	}

	response := httptest.NewRecorder()
	issueTestSession(t, auth, response)

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/", nil)
	request.AddCookie(response.Result().Cookies()[0])

	if _, ok := auth.session(request); !ok {
		t.Fatal("new session rejected before rotation")
	}

	if err := clientaccess.NewFileStore(dir).Reset(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, ok := auth.session(request); ok {
		t.Fatal("old browser session accepted after rotation")
	}

	if !auth.login(httptest.NewRecorder(), string(unlock)) {
		t.Fatal("rotation changed the operator unlock")
	}
}

func TestOpenRejectsInsecureSecretFiles(t *testing.T) {
	configDir := privateAuthDir(t)
	if err := SetUnlock(configDir, []byte("correct horse battery staple")); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // This test must create an insecure file to verify rejection.
	if err := os.Chmod(filepath.Join(configDir, clientaccess.FileName), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := Open(
		configDir,
		"http://127.0.0.1:8787",
	); !errors.Is(
		err,
		clientaccess.ErrInvalidSecretFile,
	) {
		t.Fatalf("Open() error = %v", err)
	}
}

func issueTestSession(t *testing.T, auth *authenticator, w http.ResponseWriter) string {
	t.Helper()

	_, key, err := auth.state.Browser()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key[:])

	return auth.issue(w, key[:])
}

func privateAuthDir(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "wake")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestLiveUnlockUsesOneSnapshotAcrossRotation(t *testing.T) {
	dir := privateAuthDir(t)
	auth, enabled, err := Open(dir, "http://127.0.0.1:8787")
	if err != nil || enabled {
		t.Fatal(enabled, err)
	}

	handler, err := NewHandler(auth, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	locked := httptest.NewRecorder()
	handler.ServeHTTP(locked, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/login", nil))

	if locked.Code != http.StatusServiceUnavailable {
		t.Fatal("unprovisioned browser was not locked", locked.Code)
	}

	oldUnlock := "synthetic original unlock"
	newUnlock := "synthetic replacement unlock"

	if err := SetUnlock(dir, []byte(oldUnlock)); err != nil {
		t.Fatal(err)
	}

	// The first clock read reserves the attempt. The second is after the
	// old verifier succeeded, immediately before signing the cookie.
	calls := 0
	now := time.Now()
	auth.now = func() time.Time {
		calls++
		if calls == 2 {
			if err := SetUnlock(dir, []byte(newUnlock)); err != nil {
				t.Fatal(err)
			}
		}

		return now.Add(time.Duration(calls) * time.Second)
	}

	attempt := httptest.NewRecorder()
	if !auth.login(attempt, oldUnlock) {
		t.Fatal("old verification did not finish")
	}

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/", nil)
	request.AddCookie(attempt.Result().Cookies()[0])

	if _, ok := auth.session(request); ok {
		t.Fatal("old unlock yielded a valid cookie after rotation")
	}

	if auth.login(httptest.NewRecorder(), oldUnlock) || !auth.login(httptest.NewRecorder(), newUnlock) {
		t.Fatal("daemon did not switch to the new unlock")
	}
}

func TestLiveUnlockPreservesConcurrentGrantChanges(t *testing.T) {
	dir := privateAuthDir(t)
	store := clientaccess.NewFileStore(dir)
	manager := clientaccess.New(store)

	client, secret, err := manager.Create(t.Context(), "Old client")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Clear()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	start := make(chan struct{})
	errs := make(chan error, 3)
	go func() {
		<-start

		errs <- SetUnlock(dir, []byte("synthetic concurrent unlock"))
	}()
	go func() {
		<-start

		errs <- manager.Revoke(ctx, client.ID())
	}()
	go func() {
		<-start

		_, token, err := manager.Create(ctx, "New client")
		if token != nil {
			token.Clear()
		}

		errs <- err
	}()

	close(start)

	for range 3 {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}

	record, _, err := store.Browser()
	if err != nil || record == "" {
		t.Fatal("lost unlock", err)
	}

	grants, err := manager.Clients(ctx)
	if err != nil || len(grants) != 1 || grants[0].Name() != "New client" {
		t.Fatal("lost a grant change", grants, err)
	}
}
