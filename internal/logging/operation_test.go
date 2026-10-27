// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"go.trippwill.dev/wake/provider"
)

func TestOperationsCorrelateClassifyAndFilter(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		t.Run(level.String(), func(t *testing.T) {
			var output bytes.Buffer

			logger := Component(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: level})), "test")

			ctx, finish := Start(context.Background(), logger, "acquire")
			nested, normalized := Start(ctx, logger, "normalize")
			if ID(ctx) == "" || ID(ctx) != ID(nested) {
				t.Fatal("lost correlation")
			}

			normalized(nil)
			finish(fmt.Errorf("private-token-and-payload: %w", provider.ErrUnauthorized))

			_, deferred := Start(context.Background(), logger, "reference")
			deferred(nil, "outcome", "deferred")

			_, unknown := Start(context.Background(), logger, "process")
			unknown(errors.New("private-financial-value"))

			_, cancelled := Start(context.Background(), logger, "shutdown_work")
			cancelled(context.Canceled)

			if strings.Contains(output.String(), "private-") {
				t.Fatal("private error leaked")
			}

			decoder := json.NewDecoder(&output)
			seen := map[string]map[string]any{}
			for decoder.More() {
				var record map[string]any
				if err := decoder.Decode(&record); err != nil {
					t.Fatal(err)
				}

				seen[record["event"].(string)] = record
			}

			if seen["acquire_finished"]["code"] != "access_required" ||
				seen["process_finished"]["code"] != "operation_failed" {
				t.Fatal(seen)
			}

			if seen["normalize_finished"]["operation_id"] != seen["acquire_finished"]["operation_id"] ||
				seen["normalize_finished"]["duration_ms"] == nil {
				t.Fatal(seen)
			}

			_, starts := seen["acquire_started"]
			_, deferrals := seen["reference_finished"]
			_, cancellations := seen["shutdown_work_finished"]
			if want := level == slog.LevelDebug; starts != want || deferrals != want || cancellations != want {
				t.Fatal("level filtering", seen)
			}
		})
	}
}

func TestProgressIsPeriodicAndJoined(t *testing.T) {
	for _, elapsed := range []time.Duration{29 * time.Second, 30 * time.Second, time.Minute} {
		synctest.Test(t, func(t *testing.T) {
			var output bytes.Buffer

			logger := slog.New(slog.NewJSONHandler(&output, nil))
			stop := Progress(context.Background(), logger, "rebuild_progress", 30*time.Second)

			synctest.Wait()
			time.Sleep(elapsed)
			synctest.Wait()
			stop()

			want := int(elapsed / (30 * time.Second))
			if got := bytes.Count(output.Bytes(), []byte("\n")); got != want {
				t.Fatal("progress cadence", elapsed, got, want)
			}

			time.Sleep(time.Minute)
			synctest.Wait()

			if got := bytes.Count(output.Bytes(), []byte("\n")); got != want {
				t.Fatal("reporter outlived work")
			}
		})
	}
}

func TestErrorsAlsoCoalesce(t *testing.T) {
	var output bytes.Buffer

	logger, flush := Coalesce(slog.New(slog.NewJSONHandler(&output, nil)))
	for range 100 {
		logger.Error("request failed", "event", "admin_request", "code", "unavailable")
	}

	if bytes.Count(output.Bytes(), []byte("\n")) != 1 {
		t.Fatal("unbounded errors")
	}

	flush()

	if !strings.Contains(output.String(), `"suppressed":99`) {
		t.Fatal(output.String())
	}
}
