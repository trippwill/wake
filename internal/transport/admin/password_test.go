// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/clientaccess"
)

const authTestOrigin = "http://127.0.0.1:8787"

func TestBrowserSignOutRevokesOnlyCurrentSessionAndClosesStream(t *testing.T) {
	dir := privateAuthDir(t)
	if err := SetPassword(dir, []byte("synthetic browser password")); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()

	auth, _, err := Open(dir, origin)
	if err != nil {
		t.Fatal(err)
	}

	server.Config.Handler, err = NewHandler(auth, Dependencies{
		Workflow: application.New(nil, nil, nil, nil, nil, nil),
	})
	if err != nil {
		t.Fatal(err)
	}

	server.Start()
	t.Cleanup(server.Close)

	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	current := httptest.NewRecorder()
	csrf := issueTestSession(t, auth, current)
	currentCookie := current.Result().Cookies()[0]
	other := httptest.NewRecorder()
	issueTestSession(t, auth, other)
	otherCookie := other.Result().Cookies()[0]

	request := func(method, path string, cookie *http.Cookie, form url.Values) *http.Response {
		t.Helper()

		r, err := http.NewRequestWithContext(t.Context(), method, origin+path, strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}

		r.AddCookie(cookie)

		if method == http.MethodPost {
			r.Header.Set("Origin", origin)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}

		result, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { _ = result.Body.Close() })

		return result
	}

	stream := request(http.MethodGet, "/events", currentCookie, nil)
	reader := bufio.NewReader(stream.Body)

	if frame, err := reader.ReadString(
		'\n',
	); stream.StatusCode != http.StatusOK || err != nil ||
		frame != ": connected\n" {
		t.Fatal("event stream did not connect", stream.StatusCode, frame, err)
	}

	result := request(http.MethodPost, "/logout", currentCookie, url.Values{"csrf": {csrf}})
	if result.StatusCode != http.StatusSeeOther || result.Header.Get("Location") != "/login" {
		t.Fatal("sign-out failed", result.StatusCode)
	}

	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(reader); done <- err }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal("event stream did not close cleanly", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event stream remained open after sign-out")
	}

	if result := request(
		http.MethodGet,
		"/system/contact",
		currentCookie,
		nil,
	); result.StatusCode != http.StatusUnauthorized {
		t.Fatal("Wake still accepted the signed-out cookie", result.StatusCode)
	}

	if result := request(
		http.MethodGet,
		"/system/contact",
		otherCookie,
		nil,
	); result.StatusCode != http.StatusOK {
		t.Fatal("sign-out invalidated another session", result.StatusCode)
	}

	restarted, _, err := Open(dir, origin)
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, origin+"/system/contact", nil)
	r.AddCookie(currentCookie)

	if _, ok := restarted.session(r); ok {
		t.Fatal("restart restored a revoked session")
	}
}

func TestBrowserPasswordChangeAndSessionRevocation(t *testing.T) {
	const browserOrigin = "https://wake.internal:18787"

	dir := privateAuthDir(t)

	auth, _, err := Open(dir, browserOrigin)
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
		// Wake receives HTTP from the trusted TLS endpoint.
		r := httptest.NewRequest(
			http.MethodPost,
			"http://wake.internal:18787"+path,
			strings.NewReader(values.Encode()),
		)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)

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

		if cookie := cookies[0]; cookie.Name != sessionCookie || !cookie.Secure || !cookie.HttpOnly ||
			cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
			t.Fatal("unexpected HTTPS session cookie attributes")
		}

		return cookies[0]
	}

	active := func(cookie *http.Cookie) (string, bool) {
		r := httptest.NewRequest(http.MethodGet, browserOrigin+"/", nil)
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

	now := time.Now()
	auth.now = func() time.Time { return now }

	original := cookieFrom(post("/login", form, nil, browserOrigin))

	invalidLogin := httptest.NewRequest(
		http.MethodPost,
		browserOrigin+"/login",
		strings.NewReader("password=%"),
	)
	invalidLogin.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	invalidLogin.Header.Set("Origin", browserOrigin)

	invalidLoginResult := httptest.NewRecorder()
	handler.ServeHTTP(invalidLoginResult, invalidLogin)

	if invalidLoginResult.Code != http.StatusBadRequest {
		t.Fatal("accepted a malformed login form", invalidLoginResult.Code)
	}

	csrf, ok := active(original)
	if !ok {
		t.Fatal("original session rejected")
	}

	form = url.Values{
		"password": {"synthetic changed password"}, "confirm": {"synthetic changed password"},
		"current": {password},
	}
	if got := post(passwordPath, form, original, browserOrigin).Code; got != http.StatusForbidden {
		t.Fatal("password change accepted without CSRF", got)
	}

	form.Set("csrf", csrf)

	if got := post(passwordPath, form, original, browserOrigin).Code; got != http.StatusTooManyRequests {
		t.Fatal("password change did not respect the login attempt limit", got)
	}

	settings := httptest.NewRequest(http.MethodGet, browserOrigin+passwordPath, nil)
	settings.AddCookie(original)

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, settings)

	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `name="current"`) {
		t.Fatal("password change form unavailable", page.Code)
	}

	for _, test := range []struct {
		field, value, message string
	}{
		{"current", "wrong current password", "The current password is incorrect."},
		{"confirm", "different confirmation", "The passwords do not match."},
		{"", "short", "Use at least 12 bytes for the password."},
		{"", strings.Repeat("a", MaxPasswordBytes+1), "The password is too long."},
		{"", "synthetic\npassword", "Use valid text on one line for the password."},
	} {
		invalid := url.Values{
			"current":  form["current"],
			"password": form["password"],
			"confirm":  form["confirm"],
			"csrf":     form["csrf"],
		}
		if test.field == "" {
			invalid.Set("password", test.value)
			invalid.Set("confirm", test.value)
		} else {
			invalid.Set(test.field, test.value)
		}

		now = now.Add(attemptInterval)

		result := post(passwordPath, invalid, original, browserOrigin)
		if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), test.message) {
			t.Fatal("password change did not explain a validation error", result.Code, test.message)
		}
	}

	now = now.Add(attemptInterval)

	changed := cookieFrom(post(passwordPath, form, original, browserOrigin))
	if _, ok := active(original); ok {
		t.Fatal("old session accepted after password change")
	}

	newCSRF, ok := active(changed)
	if !ok {
		t.Fatal("password change did not renew the browser session")
	}

	signedOut := cookieFrom(post("/logout", url.Values{"csrf": {newCSRF}}, changed, browserOrigin))
	if signedOut.MaxAge >= 0 {
		t.Fatal("sign-out did not expire the browser cookie")
	}

	other := httptest.NewRecorder()
	issueTestSession(t, auth, other)
	otherCookie := other.Result().Cookies()[0]

	if err := auth.state.RevokeBrowserSessions(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, ok := active(otherCookie); ok {
		t.Fatal("old session accepted after revocation")
	}

	now = now.Add(attemptInterval)

	cookieFrom(post("/login", url.Values{"password": {form.Get("password")}}, nil, browserOrigin))

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

func TestExpiredBrowserSessionIsRejectedAndPruned(t *testing.T) {
	dir := privateAuthDir(t)
	if err := SetPassword(dir, []byte("synthetic browser password")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, authTestOrigin)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	auth.now = func() time.Time { return now }

	original := httptest.NewRecorder()
	id := issueTestSession(t, auth, original)
	done := auth.sessionDone(id)

	now = now.Add(auth.lifetime)

	r := httptest.NewRequest(http.MethodGet, authTestOrigin+"/system/contact", nil)
	r.AddCookie(original.Result().Cookies()[0])

	if _, ok := auth.session(r); ok {
		t.Fatal("session remained valid at its expiry")
	}

	issueTestSession(t, auth, httptest.NewRecorder())

	select {
	case <-done:
	default:
		t.Fatal("expired session was not closed during sign-in")
	}

	if auth.sessionDone(id) != nil {
		t.Fatal("expired session remained registered")
	}
}

func TestBrowserPasswordChangeRejectsConcurrentReset(t *testing.T) {
	dir := privateAuthDir(t)

	const (
		originalPassword = "synthetic original password"
		resetPassword    = "synthetic reset password"
	)

	if err := SetPassword(dir, []byte(originalPassword)); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, authTestOrigin)
	if err != nil {
		t.Fatal(err)
	}

	handler, err := NewHandler(auth, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	login := httptest.NewRecorder()
	csrf := issueTestSession(t, auth, login)

	form := url.Values{
		"csrf": {csrf}, "current": {originalPassword},
		"password": {"synthetic browser replacement"}, "confirm": {"synthetic browser replacement"},
	}

	r := httptest.NewRequest(http.MethodPost, authTestOrigin+passwordPath, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", authTestOrigin)
	r.AddCookie(login.Result().Cookies()[0])

	now := time.Now()
	clockReads := 0
	auth.now = func() time.Time {
		clockReads++
		// The handler has loaded and validated the original snapshot when it reserves the attempt.
		if clockReads == 3 {
			if err := SetPassword(dir, []byte(resetPassword)); err != nil {
				t.Fatal(err)
			}
		}

		return now
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatal("password change did not require sign-in after concurrent reset", w.Code)
	}

	verifier, key, err := auth.state.Browser()
	clear(key[:])

	if err != nil || !matchesPassword(verifier, resetPassword) {
		t.Fatal("browser password change replaced the concurrently reset password", err)
	}
}
