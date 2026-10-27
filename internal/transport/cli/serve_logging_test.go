// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestStartupBindingAlwaysVisible(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		t.Run(level.String(), func(t *testing.T) {
			var output bytes.Buffer

			logger := newLogger(&output, level)
			logger.Info(
				"daemon ready",
				"event",
				"daemon_started",
				"admin_enabled",
				true,
				"admin_bind",
				"127.0.0.1",
				"admin_port",
				"8787",
			)

			var entry map[string]any
			if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}

			if entry["admin_bind"] != "127.0.0.1" || entry["admin_port"] != "8787" {
				t.Fatal(entry)
			}

			output.Reset()
			logger.Info("ordinary work", "event", "process_finished")

			if (output.Len() == 0) != (level > slog.LevelInfo) {
				t.Fatal("ordinary filtering lost")
			}
		})
	}
}
