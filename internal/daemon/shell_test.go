// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/currencycatalog"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestShellStartupAndJoinedShutdown(t *testing.T) {
	for _, tc := range []struct {
		name               string
		options            sourceOptions
		corrupt, recovered bool
	}{
		{"local only", sourceOptions{}, false, false},
		{"corrupt optional stores", sourceOptions{}, true, false},
		{"installed recovery", sourceOptions{}, false, true},
		{"upstream harness", sourceOptions{openSources: persistence.Open}, false, false},
		{"upstream initialization failed", sourceOptions{openSources: persistence.Open, ReferenceURL: ":invalid"}, false, false},
	} {
		t.Run(
			tc.name,
			func(t *testing.T) { testShellStartupAndJoinedShutdown(t, tc.options, tc.corrupt, tc.recovered) },
		)
	}
}

func testShellStartupAndJoinedShutdown(t *testing.T, options sourceOptions, corrupt, recovered bool) {
	cfg := shellConfig(t)
	seedAPIClient(t, cfg)

	const invalid = "unrelated optional bytes"

	profile := persistence.Profile{
		WarehousePath:   cfg.Storage.WarehousePath,
		ConnectionsPath: cfg.Storage.ConnectionsPath,
		SyncPath:        cfg.Storage.SyncPath,
	}
	if recovered {
		cfg.Credentials.KeyPath = filepath.Join(cfg.Paths.ConfigDir, "credentials.key")
		if generated, err := EnsureCredentialKey(t.Context(), cfg); err != nil || !generated {
			t.Fatal("expected a new credential key", err)
		}

		seedSavedCredential(t, cfg, false)
		seedShellRecovery(t, profile)
	} else if options.openSources == nil && !corrupt {
		store, err := persistence.OpenFinancial(t.Context(), profile, nil)
		if err != nil {
			t.Fatal(err)
		}

		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	optional := []string{cfg.Storage.ConnectionsPath, cfg.Storage.SyncPath}
	if corrupt {
		for _, path := range optional {
			if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := admin.SetPassword(cfg.Paths.ConfigDir, []byte("synthetic-shell-unlock")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), options, func(p string) {
			if p == "ready" {
				close(ready)
			}
		})
	}()

	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("startup stalled")
	}

	transport := financialTransport(t, cfg)
	client := &http.Client{Transport: transport, Timeout: time.Second}
	for _, path := range []string{"/v1/trades", "/control/ingest"} {
		res, e := client.Get("http://wake" + path)
		if e != nil {
			t.Fatal(e)
		}

		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatal(path, res.Status)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Admin.Origin()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}

	browser := &http.Client{
		Transport:     adminLoopbackTransport(t, cfg.Admin),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       time.Second,
	}

	res, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatal("admin not protected", res.Status)
	}

	cookies := assertLocalFinancialAccess(t, ctx, cfg, client, browser)
	if recovered {
		approveShellRecovery(t, cfg.Admin.Origin(), browser, cookies)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown not joined")
	}

	if conn, err := net.Dial("tcp", cfg.API.Address()); err == nil {
		_ = conn.Close()

		t.Fatal("API listener retained")
	}

	if err = Run(ctx, cfg, slog.Default()); err != nil {
		t.Fatal(err)
	}

	if recovered {
		store, err := persistence.OpenInstalled(t.Context(), profile, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = store.Close() }()

		if _, err := store.UpstreamRevision(t.Context()); err != nil {
			t.Fatal("reviewed authorization did not survive daemon shutdown", err)
		}
	}

	if options.openSources == nil && !recovered {
		for _, path := range optional {
			//nolint:gosec // The paths refer to this test's disposable stores.
			data, err := os.ReadFile(path)
			if corrupt && (err != nil || string(data) != invalid) || !corrupt && !errors.Is(err, os.ErrNotExist) {
				t.Fatal("daemon changed optional store", path, err)
			}
		}
	}
}

func TestCurrencyRefreshFollowsAdminAndDaemonLifetime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		admin   bool
		refresh bool
	}{
		{"refresh enabled", true, true},
		{"refresh disabled", true, false},
		{"unlock unprovisioned", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := shellConfig(t)
			cfg.Admin.CurrencyRefresh = tc.refresh

			if tc.admin {
				if err := admin.SetPassword(cfg.Paths.ConfigDir, []byte("synthetic-currency-unlock")); err != nil {
					t.Fatal(err)
				}
			}

			requested := make(chan struct{}, 1)
			canceled := make(chan struct{}, 1)

			upstream := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				requested <- struct{}{}

				<-r.Context().Done()

				canceled <- struct{}{}
			}))
			defer upstream.Close()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			ready := make(chan struct{})
			done := make(chan error, 1)
			options := sourceOptions{
				CurrencyCatalog: currencycatalog.Options{Client: upstream.Client(), URL: upstream.URL},
			}

			go func() {
				done <- run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), options, func(phase string) {
					if phase == "ready" {
						close(ready)
					}
				})
			}()

			select {
			case <-ready:
			case err := <-done:
				t.Fatal(err)
			case <-time.After(5 * time.Second):
				t.Fatal("currency request blocked startup")
			}

			if tc.refresh {
				select {
				case <-requested:
				case <-time.After(5 * time.Second):
					t.Fatal("daemon did not request currency codes")
				}
			}

			cancel()

			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("currency request blocked shutdown")
			}

			if tc.refresh {
				select {
				case <-canceled:
				case <-time.After(5 * time.Second):
					t.Fatal("currency request was not canceled")
				}
			}

			select {
			case <-requested:
				t.Fatal("unexpected currency request")
			default:
			}
		})
	}
}

func TestCurrencyCachePathAvoidsConfiguredFiles(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, currencycatalog.CacheName)
	cfg := config.Config{Paths: config.Paths{RuntimeDir: dir, ConfigFile: cache}}

	if got := currencyCachePath(cfg); got != "" {
		t.Fatal("currency cache would overwrite the configured TOML file", got)
	}

	cfg.Paths.ConfigFile = ""
	cfg.Credentials.KeyPath = filepath.Join(dir, "master.key")

	if err := os.WriteFile(cfg.Credentials.KeyPath, []byte("synthetic key"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Link(cfg.Credentials.KeyPath, cache); err != nil {
		t.Fatal(err)
	}

	if got := currencyCachePath(cfg); got != "" {
		t.Fatal("currency cache aliases the configured key", got)
	}
}

func shellConfig(t *testing.T) config.Config {
	t.Helper()

	adminPort := freeTCPPort(t)
	apiPort := freeTCPPort(t)
	for apiPort == adminPort {
		apiPort = freeTCPPort(t)
	}

	return config.Config{
		Paths: config.Paths{ConfigDir: resetPrivateDir(t), RuntimeDir: resetPrivateDir(t)},
		Storage: config.Storage{
			WarehousePath:   filepath.Join(resetPrivateDir(t), "history.sqlite"),
			ConnectionsPath: filepath.Join(resetPrivateDir(t), "credentials.sqlite"),
			SyncPath:        filepath.Join(resetPrivateDir(t), "sync.sqlite"),
		},
		Admin: config.Admin{Port: adminPort},
		API:   config.API{Port: apiPort},
	}
}

func adminLoopbackTransport(t *testing.T, admin config.Admin) *http.Transport {
	t.Helper()

	// Keep the browser authority in the request without relying on OS DNS for .localhost.
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", admin.Address())
	}}
	t.Cleanup(transport.CloseIdleConnections)

	return transport
}

func assertLocalFinancialAccess(
	t *testing.T,
	ctx context.Context,
	cfg config.Config,
	api, browser *http.Client,
) []*http.Cookie {
	t.Helper()

	snapshot, err := api.Get("http://wake/v1/snapshot")
	if err != nil {
		t.Fatal(err)
	}

	var full application.FinancialSnapshot

	err = json.NewDecoder(snapshot.Body).Decode(&full)
	_ = snapshot.Body.Close()
	if err != nil || snapshot.StatusCode != http.StatusOK || full.Mode != "full" ||
		full.BaselineID == "" || full.Version == "" {
		t.Fatal("local snapshot unavailable", snapshot.Status, err)
	}

	target := "http://wake/v1/snapshot?" + (url.Values{
		"baseline_id": {full.BaselineID}, "from": {full.Version},
	}).Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}

	response, err := api.Do(request)
	if err != nil {
		t.Fatal(err)
	}

	var delta application.SnapshotDelta

	err = json.NewDecoder(response.Body).Decode(&delta)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || delta.Mode != "delta" ||
		delta.BaselineID != full.BaselineID || delta.From != full.Version || delta.Version != full.Version ||
		len(delta.Upserts)+len(delta.Removals) != 0 {
		t.Fatal("local delta read failed", response.Status, err)
	}

	stream, err := api.Get("http://wake/v1/events")
	if err != nil {
		t.Fatal(err)
	}

	line, err := bufio.NewReader(stream.Body).ReadString('\n')
	_ = stream.Body.Close()
	if err != nil || stream.StatusCode != http.StatusOK || line != "event: publication\n" {
		t.Fatal("local event stream unavailable", line, err)
	}

	request, err = http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		cfg.Admin.Origin()+"/login",
		strings.NewReader(url.Values{"password": {"synthetic-shell-unlock"}}.Encode()),
	)
	if err != nil {
		t.Fatal(err)
	}

	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", cfg.Admin.Origin())

	login, err := browser.Do(request)
	if err != nil {
		t.Fatal(err)
	}

	_ = login.Body.Close()
	if login.StatusCode != http.StatusSeeOther || len(login.Cookies()) == 0 {
		t.Fatal("local login failed", login.Status)
	}

	for _, path := range []string{"/activity", "/instruments", "/positions"} {
		request, err = http.NewRequestWithContext(ctx, http.MethodGet, cfg.Admin.Origin()+path, nil)
		if err != nil {
			t.Fatal(err)
		}

		for _, cookie := range login.Cookies() {
			request.AddCookie(cookie)
		}

		response, err = browser.Do(request)
		if err != nil {
			t.Fatal(err)
		}

		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatal("local financial page unavailable", path, response.Status)
		}
	}

	return login.Cookies()
}

func seedShellRecovery(t *testing.T, profile persistence.Profile) {
	t.Helper()

	archive := filepath.Join(filepath.Dir(profile.WarehousePath), "installation.tar")
	if err := persistence.Backup(t.Context(), profile, archive); err != nil {
		t.Fatal(err)
	}

	preview, err := persistence.RecoverSynchronization(t.Context(), profile, archive, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := persistence.RecoverSynchronization(t.Context(), profile, archive, preview.ID); err != nil {
		t.Fatal(err)
	}
}

func approveShellRecovery(t *testing.T, origin string, browser *http.Client, cookies []*http.Cookie) {
	t.Helper()

	call := func(method, path string, data url.Values, status int, redirect string) string {
		t.Helper()

		request, err := http.NewRequestWithContext(
			t.Context(),
			method,
			origin+path,
			strings.NewReader(data.Encode()),
		)
		if err != nil {
			t.Fatal(err)
		}

		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", origin)

		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}

		response, err := browser.Do(request)
		if err != nil {
			t.Fatal(err)
		}

		defer func() { _ = response.Body.Close() }()

		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status || response.Header.Get("Location") != redirect {
			t.Fatal(path, response.Status, response.Header.Get("Location"), err)
		}

		return string(body)
	}

	page := call(http.MethodGet, "/connections/recovery", nil, http.StatusOK, "")
	_, form, found := strings.Cut(page, `action="/recovery/resume"`)
	if !found || !strings.Contains(page, "Review provider restart") {
		t.Fatal("installed recovery did not use the scoped review form")
	}

	fields := url.Values{}
	for _, pair := range regexp.MustCompile(`name="(csrf|revision|plan)" value="([^"]+)"`).FindAllStringSubmatch(form, -1) {
		fields.Set(pair[1], pair[2])
	}

	if fields.Get("plan") == "" || fields.Get("csrf") == "" || fields.Get("revision") == "" {
		t.Fatal("recovery form missing approval fields")
	}

	digest := fields.Get("plan")
	fields.Set("plan", strings.Repeat("0", len(digest)))
	call(http.MethodPost, "/recovery/resume", fields, http.StatusBadRequest, "")
	fields.Set("plan", digest)
	call(http.MethodPost, "/recovery/resume", fields, http.StatusSeeOther, "/connections")

	call(http.MethodPost, "/connections/source-id/secrets", url.Values{
		"csrf":         {fields.Get("csrf")},
		"secret_key":   {"token"},
		"secret.token": {"replacement synthetic token"},
	}, http.StatusSeeOther, "/connections/recovery")
	call(http.MethodPost, "/recovery/resume", fields, http.StatusBadRequest, "")
}
