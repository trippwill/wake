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
	"slices"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/daemon"
)

func run(args []string, stdout, stderr io.Writer) int { return Run(args, stdout, stderr) }

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

func TestCommandDiscoveryReflectsCommandTree(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--json", "help"}, &stdout, &stderr); code != exitOK {
		t.Fatal(code, stderr.String())
	}

	var tree commandDescription
	if err := json.Unmarshal(stdout.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}

	var names []string

	var collectLeaves func(commandDescription)

	collectLeaves = func(command commandDescription) {
		if len(command.Commands) == 0 {
			names = append(names, command.Name)
		}

		for _, child := range command.Commands {
			collectLeaves(child)
		}
	}

	collectLeaves(tree)
	slices.Sort(names)

	want := []string{
		"wake access reset",
		"wake access revoke sessions",
		"wake access set password",
		"wake accounts get",
		"wake accounts list",
		"wake activity query",
		"wake api check",
		"wake api get",
		"wake config check",
		"wake config describe",
		"wake config get",
		"wake config show",
		"wake credentials generate-key",
		"wake help",
		"wake positions get",
		"wake serve",
		"wake storage reset",
		"wake storage upgrade",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("command paths:\n got %q\nwant %q", names, want)
	}

	for _, name := range []string{"setup", "ingest", "trades", "connections", "warehouse"} {
		stdout.Reset()
		stderr.Reset()

		if code := run([]string{name}, &stdout, &stderr); code != exitUsage {
			t.Fatal("retired command available", name, code)
		}
	}
}
