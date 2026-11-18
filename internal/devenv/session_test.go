// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package devenv

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
