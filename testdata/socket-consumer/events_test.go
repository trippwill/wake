// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicationFramesValidateBeforeDelivery(t *testing.T) {
	for _, test := range []struct {
		name, stream string
		count        int
		valid        bool
	}{
		{"complete", ": heartbeat\r\n\r\nevent: publication\r\ndata: {\"warehouse_id\":\"w\",\r\ndata: \"version\":\"v\"}\r\n\r\n", 1, true},
		{"partial", "event: publication\ndata: {\"warehouse_id\":\"w\",\"version\":\"v\"}\n", 0, true},
		{"unknown-event", "event: future\ndata: {}\n\n", 0, true},
		{"invalid", "event: publication\ndata: {\"warehouse_id\":\"w\"}\n\n", 0, false},
		{"duplicate", "event: publication\ndata: {\"warehouse_id\":\"w\",\"version\":\"v\",\"version\":\"x\"}\n\n", 0, false},
		{"large-line", "data: " + strings.Repeat("x", 8192) + "\n\n", 0, false},
		{"large-frame", strings.Repeat(": comment\n", 1000) + "\n", 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, test.stream)
			}))
			defer server.Close()
			c := client{http: server.Client(), base: server.URL, stderr: io.Discard}
			count := 0
			err := c.events(t.Context(), func(p publication) error {
				count++
				if p.WarehouseID != "w" || p.Version != "v" {
					t.Error(p)
				}
				return nil
			})
			if count != test.count || errors.Is(err, io.EOF) != test.valid {
				t.Fatal(count, err)
			}
		})
	}
}

func TestPublicationReconnectAndChangesDuringSnapshot(t *testing.T) {
	db := mirrorDB(t)
	var current state
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	snapshotStarted := make(chan struct{})
	changesSent := make(chan struct{})
	var connections, snapshots atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/events" {
			connection := connections.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			if connection == 1 {
				_, _ = fmt.Fprint(w, "event: publication\ndata: {\"warehouse_id\":\"warehouse\",\"version\":\"one\"}\n\n")
				w.(http.Flusher).Flush()
				<-snapshotStarted
				// These arrive while the first complete snapshot is downloading.
				_, _ = fmt.Fprint(w, "event: publication\ndata: {\"warehouse_id\":\"warehouse\",\"version\":\"two\"}\n\nevent: publication\ndata: {\"warehouse_id\":\"warehouse\",\"version\":\"three\"}\n\n")
				close(changesSent)
			} else {
				_, _ = fmt.Fprint(w, "event: publication\ndata: {\"warehouse_id\":\"replacement\",\"version\":\"new\"}\n\n")
			}
			return
		}
		snapshotNumber := snapshots.Add(1)
		input := fixture("three", 3, "3.0000")
		if snapshotNumber == 1 {
			input = fixture("one", 1, "1.0000")
			close(snapshotStarted)
			<-changesSent
		} else if connections.Load() > 1 {
			input = snapshot{"replacement", "new", []json.RawMessage{}}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"`+input.Version+`"`)
		_ = json.NewEncoder(w).Encode(input)
	}))
	defer server.Close()
	c := client{http: server.Client(), base: server.URL, stderr: io.Discard}
	err := c.follow(ctx, time.Millisecond, func(p publication) error {
		if p.Version == current.Version {
			return nil
		}
		if _, err := c.sync(ctx, &current, db); err != nil {
			return err
		}
		if current.Version == "new" {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || snapshots.Load() != 3 || connections.Load() != 2 {
		t.Fatal("lost pending changes or reconnect", snapshots.Load(), connections.Load(), err)
	}
	stored, err := readState(db)
	if err != nil || stored.Version != "new" || current.Version != "new" || stored.WarehouseID != "replacement" || len(stored.Resources) != 0 {
		t.Fatal("replacement baseline not committed", stored, err)
	}
}

func TestPublicationFollowRetriesInterruptedSnapshot(t *testing.T) {
	for _, failure := range []string{"request", "response"} {
		t.Run(failure, func(t *testing.T) {
			db := mirrorDB(t)
			var current state
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var connections, snapshots atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/events" {
					connections.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "event: publication\ndata: {\"warehouse_id\":\"warehouse\",\"version\":\"ready\"}\n\n")
					return
				}
				if snapshots.Add(1) == 1 {
					if failure == "request" {
						connection, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = connection.Close()
					} else {
						w.Header().Set("Content-Type", "application/json")
						w.Header().Set("Content-Length", "100")
						_, _ = io.WriteString(w, `{"warehouse_id":`)
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ETag", `"ready"`)
				_ = json.NewEncoder(w).Encode(fixture("ready", 1, "1.0000"))
			}))
			defer server.Close()
			transport := &http.Transport{DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			c := client{http: &http.Client{Transport: transport}, base: server.URL, stderr: io.Discard}
			err := c.follow(ctx, time.Millisecond, func(publication) error {
				_, err := c.sync(ctx, &current, db)
				if err == nil {
					cancel()
				}
				return err
			})
			if !errors.Is(err, context.Canceled) || connections.Load() != 2 || snapshots.Load() != 2 {
				t.Fatal("snapshot transport interruption stopped follow", connections.Load(), snapshots.Load(), err)
			}
			stored, err := readState(db)
			if err != nil || stored.Version != "ready" || current.Version != "ready" {
				t.Fatal("reconnect did not commit a complete mirror", stored, err)
			}
		})
	}
}

func TestPublicationFollowStopsOnSynchronizationFailure(t *testing.T) {
	for _, failure := range []error{protocolError{Code: "invalid", Message: "snapshot"}, errors.New("persist mirror failed")} {
		t.Run(failure.Error(), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "event: publication\ndata: {\"warehouse_id\":\"warehouse\",\"version\":\"v\"}\n\n")
			}))
			defer server.Close()
			c := client{http: server.Client(), base: server.URL, stderr: io.Discard}
			attempts := 0
			err := c.follow(t.Context(), time.Millisecond, func(publication) error { attempts++; return failure })
			if !errors.Is(err, failure) || attempts != 1 {
				t.Fatal("terminal synchronization failure was retried", attempts, err)
			}
		})
	}
}
