// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/daemon"
)

func performServe(ctx context.Context, c *cli.Command) error {
	if err := requireNoArgs(c); err != nil {
		return err
	}

	activity := newActivity(c.ErrWriter, true, "serve", "resolving configuration")
	defer activity.Stop()

	cfg, err := config.Load(explicitSettings(c))
	if err != nil {
		return failure("configure: %v", err)
	}

	logger := newLogger(c.ErrWriter, cfg.Log.Level)

	activity.Phase("starting daemon")

	if err := daemon.Run(ctx, cfg, logger, func(phase string) {
		if phase == "ready" {
			activity.Pause()
		} else {
			activity.Phase(phase)
		}
	}); err != nil {
		return failure("daemon_failed: %v", err)
	}

	return nil
}

func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(
		lifecycleHandler{slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}), level},
	)
}

// Lifecycle events must be visible even when diagnostics are filtered.
type lifecycleHandler struct {
	handler slog.Handler
	level   slog.Level
}

func (h lifecycleHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo || level >= h.level
}

func (h lifecycleHandler) Handle(ctx context.Context, r slog.Record) error {
	lifecycle := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "event" && strings.HasPrefix(a.Value.String(), "daemon_") {
			lifecycle = true
		}

		return true
	})

	if r.Level < h.level && !lifecycle {
		return nil
	}

	return h.handler.Handle(ctx, r)
}

func (h lifecycleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return lifecycleHandler{h.handler.WithAttrs(attrs), h.level}
}

func (h lifecycleHandler) WithGroup(name string) slog.Handler {
	return lifecycleHandler{h.handler.WithGroup(name), h.level}
}
