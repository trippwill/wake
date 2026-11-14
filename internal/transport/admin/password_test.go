// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/clientaccess"
)

const authTestOrigin = "http://127.0.0.1:8787"

func TestBrowserPasswordChangeAndSessionRevocation(t *testing.T) {
	dir := privateAuthDir(t)

	auth, _, err := Open(dir, authTestOrigin)
	if err != nil {
		t.Fatal(err)
	}

	clients := clientaccess.New(auth.state)

	_, secret, err := clients.Create(t.Context(), "Synthetic reader")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Clear()

	handler, err := NewHandler(auth, Dependencies{Clients: clients})
	if err != nil {
		t.Fatal(err)
	}

	post := func(path string, values url.Values, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, authTestOrigin+path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		r.Header.Set("X-Forwarded-Proto", "https")

		if cookie != nil {
			r.AddCookie(cookie)
		}

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		return w
	}

	cookieFrom := func(w *httptest.ResponseRecorder) *http.Cookie {
		t.Helper()

		res := w.Result()
		defer func() { _ = res.Body.Close() }()

		cookies := res.Cookies()
		if w.Code != http.StatusSeeOther || len(cookies) != 1 {
			t.Fatal("expected a redirect with one browser cookie", w.Code)
		}

		return cookies[0]
	}

	active := func(cookie *http.Cookie) (string, bool) {
		r := httptest.NewRequest(http.MethodGet, authTestOrigin+"/", nil)
		r.AddCookie(cookie)

		return auth.session(r)
	}

	const password = "synthetic original password"
	if err := SetPassword(dir, []byte(password)); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"password": {password}}
	if got := post("/login", form, nil, "http://other.test").Code; got != http.StatusForbidden {
		t.Fatal("wrong origin accepted", got)
	}

	original := cookieFrom(post("/login", form, nil, authTestOrigin))

	csrf, ok := active(original)
	if !ok {
		t.Fatal("original session rejected")
	}

	auth.now = func() time.Time { return time.Now().Add(time.Second) }

	form = url.Values{
		"password": {"synthetic changed password"}, "confirm": {"synthetic changed password"},
		"current": {password},
	}
	if got := post(passwordPath, form, original, authTestOrigin).Code; got != http.StatusForbidden {
		t.Fatal("password change accepted without CSRF", got)
	}

	form.Set("csrf", csrf)

	changed := cookieFrom(post(passwordPath, form, original, authTestOrigin))
	if _, ok := active(original); ok {
		t.Fatal("old session accepted after password change")
	}

	newCSRF, ok := active(changed)
	if !ok {
		t.Fatal("password change did not renew the browser session")
	}

	signedOut := cookieFrom(post("/logout", url.Values{"csrf": {newCSRF}}, changed, authTestOrigin))
	if signedOut.MaxAge >= 0 {
		t.Fatal("sign-out did not expire the browser cookie")
	}

	if err := auth.state.RevokeBrowserSessions(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, ok := active(changed); ok {
		t.Fatal("old session accepted after revocation")
	}

	auth.now = func() time.Time { return time.Now().Add(3 * time.Second) }

	cookieFrom(post("/login", url.Values{"password": {form.Get("password")}}, nil, authTestOrigin))

	token, err := secret.Copy()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(token)

	_, release, err := clients.Admit(t.Context(), string(token))
	if err != nil {
		t.Fatal("browser access changes revoked the API grant", err)
	}

	release()
}
