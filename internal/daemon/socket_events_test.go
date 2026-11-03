// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSocketPublicationStreamSurvivesReadAndWriteLimitsAndJoinsShutdown(t *testing.T) {
	cfg := shellConfig(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), func(stage string) {
			if stage == "ready" {
				close(ready)
			}
		})
	}()

	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("daemon startup stalled")
	}

	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(cfg.Paths.RuntimeDir, "wake.sock"))
	}}
	defer transport.CloseIdleConnections()

	client := &http.Client{Transport: transport, Timeout: 25 * time.Second}

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://wake/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}

	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(response.Status, response.Header)
	}

	reader := bufio.NewReader(response.Body)

	line, err := reader.ReadString('\n')
	if err != nil || line != "event: publication\n" {
		t.Fatal(line, err)
	}

	line, err = reader.ReadString('\n')

	var publication struct {
		WarehouseID string `json:"warehouse_id"`
		Version     string `json:"version"`
	}
	if err != nil || json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &publication) != nil ||
		publication.WarehouseID == "" ||
		publication.Version == "" {
		t.Fatal("missing initial publication", line, err)
	}

	if _, err = reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	// The first heartbeat arrives after both ordinary request lifetime limits.
	line, err = reader.ReadString('\n')
	if err != nil || line != ": keepalive\n" {
		t.Fatal("stream ended before heartbeat", line, err)
	}

	cancel()

	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publication stream exhausted shutdown grace")
	}
}
