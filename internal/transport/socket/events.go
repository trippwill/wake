// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package socket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.trippwill.dev/wake/internal/application"
)

func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	controller := http.NewResponseController(w)
	if controller.SetWriteDeadline(time.Now().Add(15*time.Second)) != nil {
		h.failure(w, InternalError, http.StatusInternalServerError)
		return
	}

	changes, unsubscribe := h.reads.SubscribePublications()
	defer unsubscribe()

	ctx, cancel := context.WithCancel(r.Context())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)

		select {
		case <-h.stopping:
			cancel()
		case <-ctx.Done():
		}
	}()

	defer func() { cancel(); <-stopped }()

	publication := func() (Publication, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		read, err := h.reads.Snapshot(ctx, application.AnySnapshotVersion())

		return Publication{WarehouseId: read.Snapshot.WarehouseID, Version: read.Snapshot.Version}, err
	}

	current, err := publication()
	if err != nil {
		h.failure(w, InternalError, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")

	write := func(frame string) error {
		if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}

		if _, err := fmt.Fprint(w, frame); err != nil {
			return err
		}

		if err := controller.Flush(); err != nil {
			return err
		}

		return controller.SetWriteDeadline(time.Time{})
	}

	announce := func(p Publication) error {
		data, err := json.Marshal(p)
		if err != nil {
			return err
		}

		return write("event: publication\ndata: " + string(data) + "\n\n")
	}
	if announce(current) != nil {
		return
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			next, err := publication()
			if err != nil {
				return
			}

			if next == current {
				continue
			}

			if announce(next) != nil {
				return
			}

			current = next
		case <-ticker.C:
			if write(": keepalive\n\n") != nil {
				return
			}
		}
	}
}
