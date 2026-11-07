// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/transport/socket"
)

func TestPositionsStreamUsesIndependentVersionsAndReconnects(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "baseline", equityRow("U1", "position", "100"))
	acquireRealized(
		t,
		s,
		reportedPositionsXML(
			"2026-11-01",
			"<OpenPositions>"+holdingXML("2026-11-01", "10", "")+"</OpenPositions>",
		),
	)

	server := httptest.NewServer(socket.NewHandler(s, make(chan struct{}), nil))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	connect := func() (*http.Response, *bufio.Reader) {
		t.Helper()

		r, e := client.Get(server.URL + "/v1/events")
		if e != nil {
			t.Fatal(e)
		}

		return r, bufio.NewReader(r.Body)
	}
	frame := func(reader *bufio.Reader) (string, map[string]string) {
		t.Helper()

		event, e := reader.ReadString('\n')
		if e != nil {
			t.Fatal(e)
		}

		data, e := reader.ReadString('\n')
		if e != nil {
			t.Fatal(e)
		}

		blank, e := reader.ReadString('\n')
		if e != nil || blank != "\n" {
			t.Fatal(blank, e)
		}

		var values map[string]string
		if e = json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &values); e != nil {
			t.Fatal(e)
		}

		return strings.TrimSpace(strings.TrimPrefix(event, "event: ")), values
	}

	response, reader := connect()
	defer func() { _ = response.Body.Close() }()

	name, pub := frame(reader)
	if name != "publication" {
		t.Fatal(name)
	}

	name, positions := frame(reader)
	if name != "positions" || positions["version"] != readPositions(t, s).Version {
		t.Fatal(name, positions)
	}

	acquireRealized(
		t,
		s,
		reportedPositionsXML(
			"2026-11-02",
			"<OpenPositions>"+holdingXML("2026-11-02", "11", "")+"</OpenPositions>",
		),
	)

	name, positions = frame(reader)
	if name != "positions" || positions["version"] != readPositions(t, s).Version {
		t.Fatal(name, positions)
	}

	snap, e := s.Snapshot(t.Context(), application.SnapshotCondition{})
	if e != nil || snap.Snapshot.Version != pub["version"] {
		t.Fatal("position-only change advanced snapshot", e)
	}

	tx, e := s.db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}

	if _, e = tx.Exec(`UPDATE accounts SET name='Changed name'`); e != nil {
		t.Fatal(e)
	}

	if e = s.commit(t.Context(), tx); e != nil {
		t.Fatal(e)
	}

	name, pub = frame(reader)
	if name != "publication" {
		t.Fatal(name)
	}

	_ = response.Body.Close()
	response, reader = connect()
	defer func() { _ = response.Body.Close() }()

	name, current := frame(reader)
	if name != "publication" || current["version"] != pub["version"] {
		t.Fatal(name, current)
	}

	name, current = frame(reader)
	if name != "positions" || current["version"] != positions["version"] {
		t.Fatal(name, current)
	}
}
