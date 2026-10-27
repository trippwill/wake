// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

func TestWarningsCoalesceCountsAndRespectLevels(t *testing.T) {
	var output bytes.Buffer

	logger, flush := Coalesce(
		slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelInfo})),
	)
	h := logger.Handler().(*coalescer)
	now := time.Unix(1000, 0)
	h.state.now = func() time.Time { return now }
	scoped := Component(logger, "processing")
	scoped.Debug("detail", "event", "debug_only")
	scoped.Warn("retry", "event", "storage_retry", "code", "storage_unavailable", "job_id", "opaque-one")
	scoped.Warn("retry", "event", "storage_retry", "code", "storage_unavailable", "job_id", "opaque-two")
	scoped.Warn("retry", "event", "storage_retry", "code", "storage_unavailable", "duration_ms", 44)

	if lines := bytes.Count(output.Bytes(), []byte("\n")); lines != 1 {
		t.Fatal("warning repetition", lines)
	}

	now = now.Add(time.Minute)

	scoped.Warn("retry", "event", "storage_retry", "code", "storage_unavailable")
	scoped.Warn("retry", "event", "storage_retry", "code", "storage_unavailable")
	flush()

	decoder := json.NewDecoder(&output)
	counts := []float64{0, 2, 1}
	for _, want := range counts {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			t.Fatal(err)
		}

		if entry["level"] != "WARN" || entry["component"] != "processing" {
			t.Fatal("component or level", entry)
		}

		got, _ := entry["suppressed"].(float64)
		if got != want {
			t.Fatal("suppression count", got, want)
		}
	}

	if !h.Enabled(context.Background(), slog.LevelError) || h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("level filtering lost")
	}
}
