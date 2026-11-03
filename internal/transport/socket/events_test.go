// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package socket

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
)

type eventReads struct {
	reads
	changes    chan struct{}
	subscribed atomic.Bool
	removed    chan struct{}
	snapshot   func(context.Context, application.SnapshotCondition) (application.SnapshotRead, error)
}

func (s *eventReads) SubscribePublications() (<-chan struct{}, func()) {
	s.subscribed.Store(true)
	return s.changes, func() { close(s.removed) }
}

func (s *eventReads) Snapshot(
	ctx context.Context,
	condition application.SnapshotCondition,
) (application.SnapshotRead, error) {
	return s.snapshot(ctx, condition)
}

func publicationFrame(reader *bufio.Reader) (Publication, error) {
	var event Publication

	name, err := reader.ReadString('\n')
	if err != nil {
		return event, err
	}

	data, err := reader.ReadString('\n')
	if err != nil || name != "event: publication\n" || !strings.HasPrefix(data, "data: ") {
		return event, errors.New("invalid publication frame")
	}

	blank, err := reader.ReadString('\n')
	if err != nil || blank != "\n" {
		return event, errors.New("incomplete publication frame")
	}

	err = json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &event)

	return event, err
}

func TestPublicationStreamSubscribesBeforeInitialReadAndCleansUp(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnect", true: "shutdown"}[shutdown], func(t *testing.T) {
			source := &eventReads{changes: make(chan struct{}, 1), removed: make(chan struct{})}

			var calls atomic.Int64

			source.snapshot = func(ctx context.Context, condition application.SnapshotCondition) (application.SnapshotRead, error) {
				if !source.subscribed.Load() || !condition.Matches("any-version") {
					t.Error("metadata read before subscription or loaded full resources")
				}

				if _, ok := ctx.Deadline(); !ok {
					t.Error("metadata read lacks a deadline")
				}

				version := "new"
				if calls.Add(1) == 1 {
					version = "initial"
					// Commit after the initial read's snapshot, before it returns.
					source.changes <- struct{}{}
				}

				return application.SnapshotRead{
					Snapshot: application.FinancialSnapshot{WarehouseID: "warehouse", Version: version},
				}, nil
			}
			stopping := make(chan struct{})

			server := httptest.NewServer(NewHandler(source, stopping, nil))
			defer server.Close()

			client := &http.Client{Timeout: 5 * time.Second}

			response, err := client.Get(server.URL + "/v1/events")
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = response.Body.Close() }()

			if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" ||
				response.Header.Get("Cache-Control") != "no-store" {
				t.Fatal(response.Status, response.Header)
			}

			reader := bufio.NewReader(response.Body)
			for _, version := range []string{"initial", "new"} {
				event, e := publicationFrame(reader)
				if e != nil || event.Version != version || event.WarehouseId != "warehouse" {
					t.Fatal("lost setup transition", event, e)
				}
			}

			if shutdown {
				close(stopping)

				if _, err = reader.ReadByte(); !errors.Is(err, io.EOF) {
					t.Fatal("shutdown did not end stream", err)
				}
			} else {
				_ = response.Body.Close()
			}

			select {
			case <-source.removed:
			case <-time.After(time.Second):
				t.Fatal("stream leaked subscription")
			}
		})
	}
}

func TestPublicationStreamShutdownCancelsMetadataRead(t *testing.T) {
	source := &eventReads{changes: make(chan struct{}, 1), removed: make(chan struct{})}
	started := make(chan struct{})
	source.snapshot = func(ctx context.Context, _ application.SnapshotCondition) (application.SnapshotRead, error) {
		close(started)
		<-ctx.Done()

		return application.SnapshotRead{}, ctx.Err()
	}
	stopping := make(chan struct{})

	server := httptest.NewServer(NewHandler(source, stopping, nil))
	defer server.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)

		response, err := server.Client().Get(server.URL + "/v1/events")
		if err == nil {
			_ = response.Body.Close()
		}
	}()

	<-started
	close(stopping)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel publication read")
	}
}

func TestPublicationStreamRejectsUnsupportedRequests(t *testing.T) {
	for _, test := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/v1/events", 405},
		{"HEAD", "/v1/events", 405},
		{"GET", "/v1/events?cursor=old", 400},
		{"GET", "/v1/events/extra", 404},
	} {
		response := httptest.NewRecorder()
		NewHandler(reads{}, nil, nil).ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))

		if response.Code != test.status {
			t.Fatal(test, response.Code)
		}
	}
}

// net.Pipe has no output buffer, so a client that never reads blocks the first flush.
type eventListener struct {
	connection net.Conn
	closed     chan struct{}
	once       sync.Once
}

func (l *eventListener) Accept() (net.Conn, error) {
	if l.connection != nil {
		connection := l.connection
		l.connection = nil

		return connection, nil
	}

	<-l.closed

	return nil, net.ErrClosed
}

func (l *eventListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *eventListener) Addr() net.Addr { return &net.UnixAddr{Name: "event-test", Net: "unix"} }

func TestPublicationStreamDisconnectsClientThatDoesNotRead(t *testing.T) {
	source := &eventReads{changes: make(chan struct{}, 1), removed: make(chan struct{})}
	source.snapshot = source.reads.Snapshot

	serverConnection, clientConnection := net.Pipe()
	defer func() { _ = clientConnection.Close() }()

	server := httptest.NewUnstartedServer(NewHandler(source, nil, nil))

	_ = server.Listener.Close()
	server.Listener = &eventListener{connection: serverConnection, closed: make(chan struct{})}
	server.Start()
	defer server.Close()

	if err := clientConnection.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	if _, err := io.WriteString(clientConnection, "GET /v1/events HTTP/1.1\r\nHost: wake\r\n\r\n"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-source.removed:
	case <-time.After(7 * time.Second):
		t.Fatal("stalled stream outlived bounded write")
	}
}
