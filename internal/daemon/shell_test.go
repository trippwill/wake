// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestShellStartupAndJoinedShutdown(t *testing.T) {
	cfg := shellConfig(t)
	if err := admin.SetUnlock(cfg.Paths.ConfigDir, []byte("synthetic-shell-unlock")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), func(p string) {
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

	socket := filepath.Join(cfg.Paths.RuntimeDir, "wake.sock")

	info, err := os.Stat(socket)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("socket permissions", info, err)
	}

	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()

	client := &http.Client{Transport: transport, Timeout: time.Second}
	for _, path := range []string{"/v1/trades", "/v1/accounts", "/control/ingest"} {
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

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown not joined")
	}

	if _, err = os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("socket retained", err)
	}

	if err = Run(ctx, cfg, slog.Default()); err != nil {
		t.Fatal(err)
	}
}

func TestShellConfiguredMissingKeyFails(t *testing.T) {
	cfg := shellConfig(t)
	cfg.Credentials.MasterKeyPath = filepath.Join(cfg.Paths.ConfigDir, "missing.key")
	if err := Run(context.Background(), cfg, slog.Default()); err == nil {
		t.Fatal("accepted missing configured key")
	}

	if _, err := os.Stat(cfg.Credentials.MasterKeyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("startup generated key")
	}
}

func shellConfig(t *testing.T) config.Config {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// TCP listener ports are guaranteed to fit uint16.
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	return config.Config{
		Paths: config.Paths{ConfigDir: resetPrivateDir(t), RuntimeDir: resetPrivateDir(t)},
		Storage: config.Storage{
			WarehousePath:   filepath.Join(resetPrivateDir(t), "history.sqlite"),
			ConnectionsPath: filepath.Join(resetPrivateDir(t), "credentials.sqlite"),
			OperationalPath: filepath.Join(resetPrivateDir(t), "execution.sqlite"),
		},
		//nolint:gosec // Port obtained from a bound TCP listener.
		Admin: config.Admin{Port: uint16(port), AuthMode: "local"},
	}
}
