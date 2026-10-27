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

	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestStartupReportsActualBindingAndShutdownFollowsCleanup(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			cfg := shellConfig(t)
			cfg.Admin.Port = 0
			if enabled {
				if err := admin.SetUnlock(cfg.Paths.ConfigDir, []byte("synthetic-startup-unlock")); err != nil {
					t.Fatal(err)
				}
			}

			var output bytes.Buffer

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			if err := Run(ctx, cfg, slog.New(slog.NewJSONHandler(&output, nil)), func(phase string) {
				if phase == "ready" {
					cancel()
				}
			}); err != nil {
				t.Fatal(err)
			}

			decoder := json.NewDecoder(strings.NewReader(output.String()))

			var startup map[string]any
			for decoder.More() {
				var entry map[string]any
				if err := decoder.Decode(&entry); err != nil {
					t.Fatal(err)
				}

				if entry["event"] == "daemon_started" {
					startup = entry
				}
			}

			if startup == nil || startup["admin_enabled"] != enabled {
				t.Fatal(startup)
			}

			if enabled &&
				(startup["admin_bind"] != "127.0.0.1" || startup["admin_port"] == "0" || startup["admin_port"] == "") {
				t.Fatal("did not report actual binding", startup)
			}

			if !enabled && (startup["admin_bind"] != "" || startup["admin_port"] != "") {
				t.Fatal("disabled listener announced", startup)
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

func TestAdminBindFailureIsClassifiedWithoutReadiness(t *testing.T) {
	cfg := shellConfig(t)
	if err := admin.SetUnlock(cfg.Paths.ConfigDir, []byte("synthetic-startup-unlock")); err != nil {
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
