// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.trippwill.dev/wake/internal/clientaccess"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/internal/transport/financialapi"
)

const frontedUnlock = "synthetic-fronted-unlock"

func newFrontedServer(t *testing.T, secure bool) (*httptest.Server, *httptest.Server, *clientaccess.Manager) {
	t.Helper()

	dir := privateAuthDir(t)
	//nolint:gosec // Private directory requires owner traversal.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	store, err := persistence.Open(
		t.Context(),
		persistence.Profile{
			WarehousePath:   filepath.Join(dir, "warehouse.db"),
			ConnectionsPath: filepath.Join(dir, "access.db"),
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	clients := clientaccess.New(clientaccess.NewFileStore(dir))

	if err := SetUnlock(dir, []byte(frontedUnlock)); err != nil {
		t.Fatal(err)
	}

	proxy := httptest.NewUnstartedServer(nil)
	origin := "http://" + proxy.Listener.Addr().String()
	if secure {
		origin = "https://" + proxy.Listener.Addr().String()
	}

	auth, enabled, err := Open(dir, origin)
	if err != nil || !enabled {
		t.Fatal(enabled, err)
	}

	handler, err := NewHandler(auth, Dependencies{Clients: clients})
	if err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	proxy.Config.Handler = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(target); r.Out.Host = r.In.Host },
	}
	if secure {
		proxy.StartTLS()
	} else {
		proxy.Start()
	}

	t.Cleanup(proxy.Close)

	api := httptest.NewServer(financialapi.Authenticated(clients, financialapi.NewHandler(store, nil, nil)))
	t.Cleanup(api.Close)

	return proxy, api, clients
}

func TestFrontedUnlockAndClientAuthority(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "forwarded-http", true: "https-proxy"}[secure], func(t *testing.T) {
			front, api, _ := newFrontedServer(t, secure)
			client := front.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			request := func(method, path, body, origin string, cookie *http.Cookie) (*http.Response, string) {
				t.Helper()

				r, err := http.NewRequestWithContext(t.Context(), method, front.URL+path, strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}

				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("Origin", origin)
				r.Header.Set("X-Wake-Proxy-Authorization", "untrusted")
				r.Header.Set("X-Forwarded-Proto", "https")

				if cookie != nil {
					r.AddCookie(cookie)
				}

				res, err := client.Do(r)
				if err != nil {
					t.Fatal(err)
				}

				data, err := io.ReadAll(res.Body)
				_ = res.Body.Close()

				if err != nil {
					t.Fatal(err)
				}

				return res, string(data)
			}

			res, _ := request(http.MethodGet, "/system/clients", "", front.URL, nil)
			if res.StatusCode != http.StatusSeeOther || len(res.Cookies()) != 0 {
				t.Fatal("front bypassed unlock", res.Status)
			}

			res, _ = request(
				http.MethodPost,
				"/login",
				url.Values{"unlock": {frontedUnlock}}.Encode(),
				"http://hostile.test",
				nil,
			)
			if res.StatusCode != http.StatusForbidden {
				t.Fatal("login origin", res.Status)
			}

			res, _ = request(
				http.MethodPost,
				"/login",
				url.Values{"unlock": {frontedUnlock}}.Encode(),
				front.URL,
				nil,
			)
			if res.StatusCode != http.StatusSeeOther || len(res.Cookies()) != 1 {
				t.Fatal("login", res.Status)
			}

			cookie := res.Cookies()[0]
			if cookie.Secure != secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
				t.Fatal("cookie attributes", cookie)
			}

			res, body := request(http.MethodGet, "/system/clients", "", front.URL, cookie)
			match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(body)
			if res.StatusCode != http.StatusOK || len(match) != 2 || len(res.Cookies()) != 0 {
				t.Fatal("client form", res.Status, body)
			}

			res, _ = request(http.MethodPost, "/system/clients", "name=reader", front.URL, cookie)
			if res.StatusCode != http.StatusForbidden {
				t.Fatal("missing CSRF", res.Status)
			}

			res, body = request(
				http.MethodPost,
				"/system/clients",
				url.Values{"name": {"Reader <script>"}, "csrf": {match[1]}}.Encode(),
				front.URL,
				cookie,
			)
			tokenMatch := regexp.MustCompile(`id="client-token"[^>]*>([^<]+)</textarea>`).FindStringSubmatch(body)
			if res.StatusCode != http.StatusOK || len(tokenMatch) != 2 ||
				strings.Contains(body, "Reader <script>") {
				t.Fatal("issuance", res.Status, body)
			}

			token := tokenMatch[1]
			if _, err := clientaccess.TokenID(token); err != nil {
				t.Fatal(err)
			}

			_, body = request(http.MethodGet, "/system/clients", "", front.URL, cookie)
			if strings.Contains(body, token) {
				t.Fatal("token redisclosed")
			}

			r, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, api.URL+"/v1/snapshot", nil)
			r.AddCookie(cookie)

			apiResponse, err := api.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}

			_ = apiResponse.Body.Close()
			if apiResponse.StatusCode != http.StatusUnauthorized {
				t.Fatal("admin cookie granted API access")
			}

			r.Header.Set("Authorization", "Bearer "+token)

			apiResponse, err = api.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}

			_ = apiResponse.Body.Close()
			if apiResponse.StatusCode != http.StatusOK {
				t.Fatal("token denied", apiResponse.Status)
			}

			r, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, front.URL+"/system/clients", nil)
			r.Header.Set("Authorization", "Bearer "+token)

			res, err = client.Do(r)
			if err != nil {
				t.Fatal(err)
			}

			_ = res.Body.Close()
			if res.StatusCode != http.StatusSeeOther {
				t.Fatal("token granted admin access", res.Status)
			}
		})
	}
}
