// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
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

func newBrowserAndAPIServers(t *testing.T) (*httptest.Server, *httptest.Server, *clientaccess.Manager) {
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

	if err := SetPassword(dir, []byte(frontedUnlock)); err != nil {
		t.Fatal(err)
	}

	browser := httptest.NewUnstartedServer(nil)
	origin := "http://" + browser.Listener.Addr().String()

	auth, enabled, err := Open(dir, origin)
	if err != nil || !enabled {
		t.Fatal(enabled, err)
	}

	handler, err := NewHandler(auth, Dependencies{Clients: clients})
	if err != nil {
		t.Fatal(err)
	}

	browser.Config.Handler = handler
	browser.Start()
	t.Cleanup(browser.Close)

	api := httptest.NewServer(financialapi.Authenticated(clients, financialapi.NewHandler(store, nil, nil)))
	t.Cleanup(api.Close)

	return browser, api, clients
}

func TestBrowserAndAPIClientAuthority(t *testing.T) {
	front, api, clients := newBrowserAndAPIServers(t)
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
		url.Values{"password": {frontedUnlock}}.Encode(),
		"http://hostile.test",
		nil,
	)
	if res.StatusCode != http.StatusForbidden {
		t.Fatal("login origin", res.Status)
	}

	res, _ = request(
		http.MethodPost,
		"/login",
		url.Values{"password": {frontedUnlock}}.Encode(),
		front.URL,
		nil,
	)
	if res.StatusCode != http.StatusSeeOther || len(res.Cookies()) != 1 {
		t.Fatal("login", res.Status)
	}

	cookie := res.Cookies()[0]
	if cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
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

	res, body = request(http.MethodPost, "/system/clients",
		url.Values{"name": {" invalid "}, "csrf": {match[1]}}.Encode(), front.URL, cookie)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "Use a client name") {
		t.Fatal("invalid client name accepted", res.Status)
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

	id, err := clientaccess.TokenID(token)
	if err != nil {
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

	for range clientaccess.MaxClients - 1 {
		_, secret, err := clients.Create(t.Context(), "Synthetic client")
		if err != nil {
			t.Fatal(err)
		}

		secret.Clear()
	}

	res, body = request(http.MethodPost, "/system/clients",
		url.Values{"name": {"One too many"}, "csrf": {match[1]}}.Encode(), front.URL, cookie)
	if res.StatusCode != http.StatusConflict || !strings.Contains(body, "Revoke an unused client") {
		t.Fatal("client limit was not reported", res.Status)
	}

	res, _ = request(http.MethodPost, "/system/clients/"+id+"/revoke",
		url.Values{"csrf": {match[1]}}.Encode(), front.URL, cookie)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/system/clients" {
		t.Fatal("revocation did not return to clients", res.Status)
	}

	r, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, api.URL+"/v1/snapshot", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	apiResponse, err = api.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}

	_ = apiResponse.Body.Close()
	if apiResponse.StatusCode != http.StatusUnauthorized {
		t.Fatal("revoked token retained access", apiResponse.Status)
	}
}
