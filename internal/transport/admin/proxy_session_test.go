// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
)

type proxySessionStore struct {
	application.Store
	retries atomic.Int64
}

func (s *proxySessionStore) RetryProcessing(context.Context, string) error {
	s.retries.Add(1)
	return nil
}

func (*proxySessionStore) Retained(context.Context, string) (host.Retained, error) {
	return host.Retained{Connection: "synthetic"}, nil
}

func (*proxySessionStore) Dashboard(context.Context) (application.Dashboard, error) {
	return application.Dashboard{}, nil
}

func newProxySessionServer(t *testing.T) (*httptest.Server, *authenticator, *proxySessionStore) {
	t.Helper()
	dir := t.TempDir()

	guard, err := SetProxyGuard(dir)
	if err != nil {
		t.Fatal(err)
	}

	bridge := &httputil.ReverseProxy{}
	proxy := httptest.NewUnstartedServer(bridge)
	origin := "https://" + proxy.Listener.Addr().String()

	auth, enabled, err := Open(dir, "proxy", origin)
	if err != nil || !enabled {
		t.Fatal(enabled, err)
	}

	t.Cleanup(auth.Close)

	store := new(proxySessionStore)
	workflow := application.New(store, nil, nil, "", "", "", nil, nil)
	stopping := make(chan struct{})

	handler, err := NewHandler(auth, Dependencies{Workflow: workflow, Stopping: stopping})
	if err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	bridge.Rewrite = func(r *httputil.ProxyRequest) {
		r.SetURL(upstreamURL)
		r.Out.Host = r.In.Host
		r.Out.Header.Set("X-Wake-Proxy-Authorization", guard)
	}

	proxy.StartTLS()
	t.Cleanup(func() { close(stopping); proxy.Close() })

	return proxy, auth, store
}

func TestProxySessionThroughHTTPReverseProxy(t *testing.T) {
	proxy, _, store := newProxySessionServer(t)
	origin := proxy.URL
	client := proxy.Client()

	jar, err := cookiejar.New(nil)
	client.Jar = jar

	if err != nil {
		t.Fatal(err)
	}

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, proxy.URL+"/connections/new", nil)
	if err != nil {
		t.Fatal(err)
	}

	request.Header.Set("Sec-Fetch-Mode", "navigate")
	request.Header.Set("Sec-Fetch-Site", "cross-site")

	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}

	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatal(response.StatusCode, string(body), err)
	}

	match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindSubmatch(body)
	if len(match) != 2 {
		t.Fatal("session did not reach the rendered form", string(body))
	}

	csrf := string(match[1])
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, test := range []struct {
		name, origin, token string
		want                int
	}{
		{"missing-origin", "", csrf, http.StatusForbidden},
		{"wrong-origin", "https://other.invalid", csrf, http.StatusForbidden},
		{"missing-token", origin, "", http.StatusForbidden},
		{"wrong-token", origin, "wrong", http.StatusForbidden},
		{"valid", origin, csrf, http.StatusSeeOther},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, e := http.NewRequestWithContext(
				t.Context(),
				http.MethodPost,
				proxy.URL+"/processing/report/retry",
				strings.NewReader(url.Values{"csrf": {test.token}}.Encode()),
			)
			if e != nil {
				t.Fatal(e)
			}

			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", test.origin)

			res, e := client.Do(r)
			if e != nil {
				t.Fatal(e)
			}

			_ = res.Body.Close()
			if res.StatusCode != test.want {
				t.Fatal(res.StatusCode, test.want)
			}
		})
	}

	if store.retries.Load() != 1 {
		t.Fatal("unexpected accepted command count", store.retries.Load())
	}
}

func TestProxySessionAdmissionAndRenewal(t *testing.T) {
	dir := t.TempDir()

	guard, err := SetProxyGuard(dir)
	if err != nil {
		t.Fatal(err)
	}

	const origin = "https://wake.example.test"

	auth, _, err := Open(dir, "proxy", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()

	now := time.Now()
	auth.now = func() time.Time { return now }

	handler, err := NewHandler(auth, Dependencies{Workflow: new(application.Workflow)})
	if err != nil {
		t.Fatal(err)
	}

	request := func(method, path, supplied string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+path, nil)
		r.Header.Set("X-Wake-Proxy-Authorization", supplied)
		r.Header.Set("Accept", "text/html")

		if cookie != nil {
			r.AddCookie(cookie)
		}

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		return w
	}

	issued := request(http.MethodGet, "/configuration?tab=one", guard, nil)
	cookies := issued.Result().Cookies()
	if issued.Code != http.StatusOK || len(cookies) != 1 ||
		issued.Header().Get("Location") != "" {
		t.Fatal("proxy did not establish session", issued.Code, cookies, issued.Header())
	}

	cookie := cookies[0]
	if cookie.Name != sessionCookie || !cookie.Secure || !cookie.HttpOnly ||
		cookie.SameSite != http.SameSiteStrictMode ||
		cookie.Path != "/" ||
		cookie.MaxAge != int(auth.lifetime.Seconds()) {
		t.Fatal("invalid session attributes", cookie)
	}

	for _, supplied := range []string{"", "invalid"} {
		w := request(http.MethodGet, "/configuration", supplied, cookie)
		if w.Code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 {
			t.Fatal("cookie bypassed guard", w.Code)
		}
	}

	w := request(http.MethodGet, "/configuration", guard, cookie)
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 0 {
		t.Fatal("valid session was not reused", w.Code)
	}

	for _, path := range []string{"/assets/vendor/README.md", "/assets/app.js"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, mode := range []string{"", "navigate"} {
				r := httptest.NewRequest(method, origin+path, nil)
				r.Header.Set("X-Wake-Proxy-Authorization", guard)
				r.Header.Set("Accept", "text/html")
				r.Header.Set("Sec-Fetch-Mode", mode)

				w = httptest.NewRecorder()
				handler.ServeHTTP(w, r)

				if w.Code != http.StatusOK || w.Header().Get("Cache-Control") == "no-store" ||
					len(w.Result().Cookies()) != 0 {
					t.Fatal("asset navigation established a cacheable session", path, method, mode, w.Code, w.Header())
				}
			}
		}
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		w = request(method, "/processing/report/retry", guard, nil)
		if len(w.Result().Cookies()) != 0 || w.Code == http.StatusOK {
			t.Fatal("unsafe method bootstrapped session", method, w.Code)
		}
	}

	bad := &http.Cookie{
		Name:     sessionCookie,
		Value:    cookie.Value + "tampered",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}

	w = request(http.MethodGet, "/configuration", guard, bad)
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 1 {
		t.Fatal("tampered session not replaced", w.Code)
	}

	now = now.Add(auth.lifetime + time.Second)

	for _, mode := range []string{"", "cors", "no-cors", "navigate"} {
		r := httptest.NewRequest(http.MethodGet, origin+"/status/contact", nil)
		r.Header.Set("X-Wake-Proxy-Authorization", guard)
		r.Header.Set("Sec-Fetch-Mode", mode)

		if mode == "navigate" {
			r.Header.Set("HX-Request", "true")
		}

		r.AddCookie(cookie)

		w = httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		if w.Code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 {
			t.Fatal("background request rotated the session", mode, w.Code)
		}
	}

	w = request(http.MethodGet, "/login", guard, cookie)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" || len(w.Result().Cookies()) != 0 {
		t.Fatal("proxy recovery path", w.Code, w.Header())
	}

	w = request(http.MethodGet, "/configuration", guard, cookie)
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 1 {
		t.Fatal("expired session not replaced", w.Code)
	}

	r := httptest.NewRequest(http.MethodGet, "https://other.invalid/", nil)
	r.Header.Set("X-Wake-Proxy-Authorization", guard)

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest || len(w.Result().Cookies()) != 0 {
		t.Fatal("wrong host established session", w.Code)
	}
}
