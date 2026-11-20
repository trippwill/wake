// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestStartupReportsActualBindingAndShutdownFollowsCleanup(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "unprovisioned"}[enabled], func(t *testing.T) {
			cfg := shellConfig(t)
			cfg.Admin.Port = 0
			if enabled {
				if err := admin.SetPassword(cfg.Paths.ConfigDir, []byte("synthetic-startup-unlock")); err != nil {
					t.Fatal(err)
				}
			}

			var output apiTestLog

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			if err := Run(ctx, cfg, slog.New(slog.NewJSONHandler(&output, nil)), func(phase string) {
				if phase == "ready" {
					defer cancel()

					assertAdminBinding(t, ctx, output.String())
				}
			}); err != nil {
				t.Fatal(err)
			}

			closed, stopped := strings.Index(
				output.String(),
				`"storage_closed"`,
			), strings.Index(
				output.String(),
				`"daemon_stopped"`,
			)
			if closed < 0 || stopped <= closed {
				t.Fatal("stop preceded cleanup", output.String())
			}
		})
	}
}

func assertAdminBinding(t *testing.T, ctx context.Context, output string) {
	t.Helper()

	decoder := json.NewDecoder(strings.NewReader(output))
	for decoder.More() {
		var entry struct {
			Event   string `json:"event"`
			Enabled bool   `json:"admin_enabled"`
			Bind    string `json:"admin_bind"`
			Port    string `json:"admin_port"`
		}
		if err := decoder.Decode(&entry); err != nil {
			t.Error(err)
			return
		}

		if entry.Event != "daemon_started" {
			continue
		}

		if !entry.Enabled || entry.Bind != "127.0.0.1" || entry.Port == "0" || entry.Port == "" {
			t.Error("did not report actual binding", entry)
			return
		}

		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			"http://"+net.JoinHostPort(entry.Bind, entry.Port)+"/login",
			nil,
		)
		if err != nil {
			t.Error(err)
			return
		}

		request.Host = net.JoinHostPort("wake.localhost", entry.Port)

		client := &http.Client{Timeout: time.Second}
		defer client.CloseIdleConnections()

		response, err := client.Do(request)
		if err != nil {
			t.Error(err)
			return
		}

		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Error("admin rejected the browser authority on its bound address", response.Status)
		}

		return
	}

	t.Error("did not report startup", output)
}

func TestAdminBindFailureIsClassifiedWithoutReadiness(t *testing.T) {
	cfg := shellConfig(t)
	if err := admin.SetPassword(cfg.Paths.ConfigDir, []byte("synthetic-startup-unlock")); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", cfg.Admin.Address())
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = listener.Close() }()

	var output bytes.Buffer
	if err = Run(context.Background(), cfg, slog.New(slog.NewJSONHandler(&output, nil))); err == nil {
		t.Fatal("accepted occupied port")
	}

	if !strings.Contains(output.String(), `"admin_bind_failed"`) ||
		!strings.Contains(output.String(), `"admin_bind":"127.0.0.1"`) ||
		strings.Contains(output.String(), `"daemon_started"`) {
		t.Fatal(output.String())
	}
}

func TestHTTPServerDoesNotPrintPanicPayloads(t *testing.T) {
	var output bytes.Buffer

	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("private-panic-payload") })
	server := httptest.NewUnstartedServer(handler)
	server.Config = newHTTPServer(handler, slog.New(slog.NewJSONHandler(&output, nil)))
	server.Start()

	response, err := server.Client().Get(server.URL)
	if response != nil {
		_ = response.Body.Close()
	}

	server.Close()

	if err == nil {
		t.Fatal("panic unexpectedly returned a response")
	}

	if strings.Contains(output.String(), "private-panic-payload") ||
		!strings.Contains(output.String(), "http_server_failure") {
		t.Fatal(output.String())
	}
}
