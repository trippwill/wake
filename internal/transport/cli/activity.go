// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

type synchronizedWriter struct {
	mu     sync.Mutex
	writer io.Writer
	err    error
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.err != nil {
		return 0, w.err
	}

	n, err := w.writer.Write(p)
	if n < len(p) && err == nil {
		err = io.ErrShortWrite
	}

	w.err = err

	return n, err
}

func (w *synchronizedWriter) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.err
}

type activity struct {
	mu             sync.Mutex
	writer         io.Writer
	json           bool
	command, phase string
	started        time.Time
	bytes, total   int64
	paused         bool
	stop, done     chan struct{}
	once           sync.Once
}

func newActivity(w io.Writer, jsonMode bool, command, phase string) *activity {
	return startActivity(w, jsonMode, command, phase, 2*time.Second)
}

func startActivity(w io.Writer, jsonMode bool, command, phase string, interval time.Duration) *activity {
	a := &activity{
		writer:  w,
		json:    jsonMode,
		command: command,
		phase:   phase,
		started: time.Now(),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	a.emit()
	go func() {
		defer close(a.done)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-a.stop:
				return
			case <-ticker.C:
				a.emit()
			}
		}
	}()

	return a
}

func (a *activity) emit() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.paused {
		return
	}

	elapsed := time.Since(a.started).Milliseconds()
	if a.json {
		event := map[string]any{
			"event":      "activity",
			"command":    a.command,
			"phase":      a.phase,
			"elapsed_ms": elapsed,
		}
		if a.total != 0 {
			event["bytes_read"] = a.bytes
			event["total_bytes"] = a.total
		}

		_ = json.NewEncoder(a.writer).Encode(event)
	} else {
		writef(a.writer, "wake: %s: %s (%ds)", a.command, a.phase, elapsed/1000)

		if a.total != 0 {
			writef(a.writer, "; %d/%d bytes read", a.bytes, a.total)
		}

		writeString(a.writer, "\n")
	}
}

func (a *activity) Phase(phase string) {
	a.mu.Lock()
	a.phase = phase
	a.paused = false
	a.mu.Unlock()
	a.emit()
}
func (a *activity) Pause() { a.mu.Lock(); a.paused = true; a.mu.Unlock() }
func (a *activity) Stop()  { a.once.Do(func() { close(a.stop); <-a.done }) }
