// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package devenv

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/clientaccess"
)

func TestConcurrentSessionsReportBoundEndpoints(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()

	readers := make([]*io.PipeReader, 0, 2)
	for range 2 {
		dir := filepath.Join(t.TempDir(), "workspace")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}

		reader, writer := io.Pipe()
		readers = append(readers, reader)
		done := make(chan error, 1)

		go func() {
			err := Session(ctx, dir, true, false, json.NewEncoder(writer))
			_ = writer.CloseWithError(err)
			done <- err
		}()

		stop := context.AfterFunc(ctx, func() { _ = reader.CloseWithError(ctx.Err()) })
		t.Cleanup(func() {
			stop()

			_ = reader.Close()

			if err := <-done; err != nil {
				t.Error(err)
			}
		})
	}

	endpoints := make(map[string]bool)

	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()

	for _, reader := range readers {
		var ready struct {
			Endpoint  string `json:"endpoint"`
			TokenFile string `json:"token_file"`
		}
		if err := json.NewDecoder(reader).Decode(&ready); err != nil {
			t.Fatal(err)
		}

		if endpoints[ready.Endpoint] {
			t.Fatalf("both sessions reported %q", ready.Endpoint)
		}

		endpoints[ready.Endpoint] = true
		assertSnapshotServed(t, ctx, client, ready.Endpoint, ready.TokenFile)
	}
}

func assertSnapshotServed(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	endpoint, tokenPath string,
) {
	t.Helper()

	secret, err := clientaccess.ReadTokenFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Clear()

	token, err := secret.Copy()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(token)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/v1/snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}

	request.Header.Set("Authorization", "Bearer "+string(token))

	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("snapshot from %q: %s", endpoint, response.Status)
	}
}

type brokenReadyOutput struct {
	endpoint string
	err      error
}

func (w *brokenReadyOutput) Write(payload []byte) (int, error) {
	var msg struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return 0, err
	}

	w.endpoint = msg.Endpoint

	return 0, w.err
}

func TestSessionJoinsAfterReadinessWriteFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	dir := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	out := &brokenReadyOutput{err: errors.New("readiness output closed")}
	err := Session(ctx, dir, false, true, json.NewEncoder(out))
	if !errors.Is(err, out.err) || ctx.Err() != nil {
		t.Fatalf("expected immediate readiness error, got %v", err)
	}

	address := strings.TrimPrefix(out.endpoint, "http://")

	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
	if err == nil {
		_ = conn.Close()

		t.Fatalf("daemon listener still open after readiness failure: %s", address)
	}
}
