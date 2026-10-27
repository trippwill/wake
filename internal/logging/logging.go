// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package logging provides component scoping and bounded failure repetition.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

func Component(logger *slog.Logger, name string) *slog.Logger {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return logger.With("component", name)
}

type (
	warning struct {
		at         time.Time
		suppressed uint64
		handler    slog.Handler
		record     slog.Record
	}
	warningState struct {
		mu    sync.Mutex
		items map[string]*warning
		now   func() time.Time
	}
	coalescer struct {
		handler slog.Handler
		state   *warningState
		key     string
	}
)

// Coalesce emits at most one identical component/event/code failure per minute.
// IDs/durations do not split the diagnostic group; suppressed counts are retained.
func Coalesce(logger *slog.Logger) (*slog.Logger, func()) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	state := &warningState{items: make(map[string]*warning), now: time.Now}

	return slog.New(&coalescer{handler: logger.Handler(), state: state}), func() { state.flush() }
}

func (h *coalescer) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

func keyAttr(a slog.Attr) string {
	switch a.Key {
	case "component", "event", "code", "stage", "operation":
		return a.Key + "=" + a.Value.String() + ";"
	}

	return ""
}

func (h *coalescer) WithAttrs(attrs []slog.Attr) slog.Handler {
	key := h.key
	for _, a := range attrs {
		key += keyAttr(a)
	}

	return &coalescer{handler: h.handler.WithAttrs(attrs), state: h.state, key: key}
}

func (h *coalescer) WithGroup(name string) slog.Handler {
	return &coalescer{handler: h.handler.WithGroup(name), state: h.state, key: h.key + name + ":"}
}

func (h *coalescer) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < slog.LevelWarn {
		return h.handler.Handle(ctx, r)
	}

	var key strings.Builder
	key.WriteString(h.key)
	key.WriteString(r.Level.String())
	key.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool { key.WriteString(keyAttr(a)); return true })
	h.state.mu.Lock()
	defer h.state.mu.Unlock()

	now := h.state.now()
	prior := h.state.items[key.String()]
	if prior != nil && now.Sub(prior.at) < time.Minute {
		prior.suppressed++
		return nil
	}

	original := r.Clone()
	if prior != nil && prior.suppressed > 0 {
		r.AddAttrs(slog.Uint64("suppressed", prior.suppressed))
	}

	if len(h.state.items) >= 1024 {
		h.state.flushLocked()
		clear(h.state.items)
	}

	h.state.items[key.String()] = &warning{at: now, handler: h.handler, record: original}

	return h.handler.Handle(ctx, r)
}
func (s *warningState) flush() { s.mu.Lock(); defer s.mu.Unlock(); s.flushLocked(); clear(s.items) }
func (s *warningState) flushLocked() {
	for _, item := range s.items {
		if item.suppressed > 0 {
			r := item.record.Clone()
			r.Message = "repeated diagnostics suppressed"
			r.Time = s.now()
			r.AddAttrs(slog.Uint64("suppressed", item.suppressed))
			_ = item.handler.Handle(context.Background(), r)
			item.suppressed = 0
		}
	}
}
