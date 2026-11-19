// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

func TestLogoutRejectsCredentialUpdateWaitingForAuthLock(t *testing.T) {
	for _, tc := range []struct {
		name, path, contentType string
		status                  int
		location                string
	}{
		{"passkey", "/passkeys/finish", "application/json", http.StatusUnauthorized, ""},
		{"password", passwordPath, "application/x-www-form-urlencoded", http.StatusSeeOther, "/login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const (
				origin   = "http://localhost:18787"
				password = "synthetic enrollment password"
			)

			configDir := privateAuthDir(t)
			if err := SetPassword(configDir, []byte(password)); err != nil {
				t.Fatal(err)
			}

			auth, enabled, err := Open(configDir, origin)
			if err != nil || !enabled {
				t.Fatal("password access unavailable", enabled, err)
			}

			handler, err := NewHandler(auth, Dependencies{})
			if err != nil {
				t.Fatal(err)
			}

			now := time.Now()
			auth.now = func() time.Time { return now }
			browser := passkeyHTTPBrowser{t: t, handler: handler, auth: auth, origin: origin}
			browser.signInWithPassword(password)

			now = now.Add(attemptInterval)

			var data []byte
			if tc.name == "passkey" {
				key := newPasskeyHTTPKey(t, "synthetic credential")
				add := beginPasskeyRequest{
					Operation: "add",
					Method:    "password",
					Password:  password,
					Label:     "Desk key",
				}
				registration := browser.begin(add, "create")
				browser.finish(registration.ID, key.response(t, registration, origin, nil), http.StatusOK)

				disable := browser.begin(beginPasskeyRequest{Operation: "disable", Method: "passkey"}, "get")
				data = marshalPasskeyTestJSON(t, finishPasskeyRequest{
					ID: disable.ID, Credential: key.response(t, disable, origin, registration.Options.User.ID),
				})
			} else {
				data = []byte(url.Values{
					"csrf": {browser.csrf}, "current": {password},
					"password": {"synthetic new password"}, "confirm": {"synthetic new password"},
				}.Encode())
			}

			before, err := auth.state.BrowserAccess()
			if err != nil {
				t.Fatal(err)
			}

			dir, err := os.Open(
				configDir,
			) // #nosec G304 -- Open the private test fixture directory to hold its lock.
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dir.Close() }()

			if err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}

			synctest.Test(t, func(t *testing.T) {
				browser.t = t
				updateDone := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					updateDone <- browser.request(http.MethodPost, tc.path, tc.contentType, data)
				}()

				synctest.Wait()

				select {
				case w := <-updateDone:
					t.Fatalf("credential update returned %d while the auth lock was held", w.Code)
				default:
				}

				logoutDone := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					logout := []byte(url.Values{"csrf": {browser.csrf}}.Encode())
					logoutDone <- browser.request(http.MethodPost, "/logout", "application/x-www-form-urlencoded", logout)
				}()

				synctest.Wait()

				select {
				case w := <-logoutDone:
					if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
						t.Errorf("logout returned %d, location %q", w.Code, w.Header().Get("Location"))
					}
				default:
					t.Error("logout did not finish while the auth lock was held")
				}

				if err := syscall.Flock(int(dir.Fd()), syscall.LOCK_UN); err != nil {
					t.Fatal(err)
				}

				w := <-updateDone
				if w.Code != tc.status || w.Header().Get("Location") != tc.location ||
					len(w.Result().Cookies()) != 0 {
					t.Errorf(
						"revoked update returned %d, location %q, %d cookies",
						w.Code,
						w.Header().Get("Location"),
						len(w.Result().Cookies()),
					)
				}

				browser.expectAccess(http.StatusUnauthorized)

				after, err := auth.state.BrowserAccess()
				if err != nil || !reflect.DeepEqual(after, before) {
					t.Fatal("revoked update changed browser credentials", err)
				}
			})
		})
	}
}
