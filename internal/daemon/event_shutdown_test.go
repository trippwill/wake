// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/transport/admin"
)

func TestEventStreamStopsBeforeShutdownGrace(t *testing.T) {
	cfg := shellConfig(t)
	if err := admin.SetUnlock(cfg.Paths.ConfigDir, []byte("synthetic-stream-unlock")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
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
	// Keep the browser request independent of daemon cancellation: only the server
	// can drain it. A client timeout would hide the five-second shutdown defect.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	login, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		cfg.Admin.Origin()+"/login",
		strings.NewReader(url.Values{"unlock": {"synthetic-stream-unlock"}}.Encode()),
	)
	if err != nil {
		t.Fatal(err)
	}

	login.Header.Set("Origin", cfg.Admin.Origin())
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := client.Do(login)
	if err != nil {
		t.Fatal(err)
	}

	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || len(response.Cookies()) != 1 {
		t.Fatal("login failed", response.Status)
	}

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, cfg.Admin.Origin()+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}

	request.AddCookie(response.Cookies()[0])

	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		t.Fatal(response.Status)
	}

	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != ": connected\n" {
		t.Fatal(line, err)
	}

	cancel()

	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event stream exhausted ordinary HTTP shutdown grace")
	}
}
