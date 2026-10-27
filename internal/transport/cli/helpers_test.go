// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/daemon"
)

func run(args []string, stdout, stderr io.Writer) int { return Run(args, stdout, stderr) }
func runAdmin(args []string, stdout, stderr io.Writer) int {
	return run(append([]string{"admin"}, args...), stdout, stderr)
}

func startShellDaemon(t *testing.T, cfg config.Config) (func(), *http.Client) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), func(p string) {
			if p == "ready" {
				close(ready)
			}
		})
	}()

	select {
	case <-ready:
	case e := <-done:
		cancel()
		t.Fatal(e)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("startup timeout")
	}

	stopped := false
	stop := func() {
		if stopped {
			return
		}

		stopped = true

		cancel()

		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(10 * time.Second):
			t.Error("shutdown timeout")
		}
	}
	t.Cleanup(stop)

	return stop, nil
}

func TestCommandDiscoveryReflectsFoundation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--json", "help"}, &stdout, &stderr); code != exitOK {
		t.Fatal(code, stderr.String())
	}

	var tree commandDescription
	if err := json.Unmarshal(stdout.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}

	names := map[string]bool{}
	for _, command := range tree.Commands {
		names[command.Name] = true
	}

	for _, name := range []string{"wake setup", "wake serve", "wake admin", "wake config", "wake help"} {
		if !names[name] {
			t.Fatal("retained command missing", name)
		}
	}

	for _, name := range []string{"ingest", "trades", "connections", "warehouse", "reset"} {
		stdout.Reset()
		stderr.Reset()

		if code := run([]string{name}, &stdout, &stderr); code != exitUsage {
			t.Fatal("retired command available", name, code)
		}
	}
}
