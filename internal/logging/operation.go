// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package logging

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"time"

	"go.trippwill.dev/wake/provider"
)

type operationKey struct{}

// Correlate keeps one opaque ID across nested work, without accepting client IDs.
func Correlate(ctx context.Context) context.Context {
	if ctx.Value(operationKey{}) != nil {
		return ctx
	}

	return context.WithValue(ctx, operationKey{}, rand.Text())
}

func ID(ctx context.Context) string {
	id, _ := ctx.Value(operationKey{}).(string)
	return id
}

// Code classifies failures without formatting potentially private error text.
func Code(err error) string {
	for _, item := range []struct {
		err  error
		code string
	}{
		{context.Canceled, "cancelled"},
		{context.DeadlineExceeded, "deadline_exceeded"},
		{provider.ErrUnauthorized, "access_required"},
		{provider.ErrTemporary, "temporary_failure"},
		{provider.ErrUnavailable, "unavailable"},
		{provider.ErrMalformed, "malformed_input"},
		{provider.ErrLimit, "limit_exceeded"},
		{provider.ErrConflict, "conflict"},
		{provider.ErrInvalid, "invalid_input"},
	} {
		if errors.Is(err, item.err) {
			return item.code
		}
	}

	return "operation_failed"
}

// Start must be paired with finish after the operation's commit or rollback.
// Attribute values must be safe metadata, never provider payloads or error text.
func Start(
	ctx context.Context,
	logger *slog.Logger,
	operation string,
	attrs ...any,
) (context.Context, func(error, ...any)) {
	ctx = Correlate(ctx)
	logger = logger.With("operation_id", ID(ctx), "operation", operation)
	logger = logger.With(attrs...)
	started := time.Now()

	logger.DebugContext(ctx, "operation started", "event", operation+"_started")
	stop := Progress(ctx, logger, operation+"_progress", 30*time.Second)

	return ctx, func(err error, result ...any) {
		stop()

		level, outcome := slog.LevelInfo, "completed"
		for i := 0; i+1 < len(result); i += 2 {
			if result[i] == "outcome" {
				switch result[i+1] {
				case "deferred":
					level, outcome = slog.LevelDebug, "deferred"
				case "continued":
					outcome = "continued"
				case "retry_scheduled":
					level, outcome = slog.LevelWarn, "retry_scheduled"
				}

				result = append(result[:i], result[i+2:]...)

				break
			}
		}

		if err != nil {
			level, outcome = slog.LevelWarn, "failed"
			if errors.Is(err, context.Canceled) {
				level, outcome = slog.LevelDebug, "cancelled"
			}

			result = append(result, "code", Code(err))
		}

		attrs := []any{
			"event",
			operation + "_finished",
			"outcome",
			outcome,
			"duration_ms",
			time.Since(started).Milliseconds(),
		}
		logger.Log(ctx, level, "operation finished", append(attrs, result...)...)
	}
}

// Progress returns a stop function that joins the reporter before returning.
func Progress(ctx context.Context, logger *slog.Logger, event string, interval time.Duration) func() {
	stop, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				logger.InfoContext(
					ctx,
					"operation still running",
					"event",
					event,
					"outcome",
					"running",
				)
			}
		}
	}()

	return func() { close(stop); <-joined }
}
