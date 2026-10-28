// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sample(revision int, price string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"id":"a","kind":"activity","revision":%d,"activity":{"family":"trade","price":{"amount":%q,"currency":"USD"}}}`, revision, price))
}
func TestRevisionSafeUpsertPreservesExactTextAndDuplicateHistory(t *testing.T) {
	s := state{Version: 1, Cursor: "start", Resources: map[string]json.RawMessage{}, History: []change{}}
	if err := s.apply(sample(3, "1.23000000000000000100")); err != nil {
		t.Fatal(err)
	}
	if err := s.apply(sample(2, "2.00")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(s.Resources["activity/a"], []byte("1.23000000000000000100")) {
		t.Fatal("older replay replaced scan")
	}
	if err := s.apply(sample(3, "1.23000000000000000100")); err != nil {
		t.Fatal("duplicate upsert", err)
	}
	if err := s.apply(sample(3, "different")); err == nil {
		t.Fatal("same revision content mismatch accepted")
	}
	path := filepath.Join(t.TempDir(), "mirror.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Items   []change `json:"items"`
			Cursor  string   `json:"cursor"`
			Through string   `json:"through"`
			More    bool     `json:"more"`
		}{[]change{{"end", sample(2, "2.00")}}, "end", "end", false})
	}))
	defer server.Close()
	c := client{http: server.Client(), base: server.URL, limit: 1, stderr: &bytes.Buffer{}}
	for range 2 {
		if err := c.catchup(context.Background(), &s, path); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := readState(path)
	if err != nil || len(saved.History) != 1 || saved.Cursor != "end" {
		t.Fatal("atomic duplicate delivery", saved, err)
	}
}
func TestConflictingDuplicateCursorFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"items":[{"cursor":"same","resource":%s}],"cursor":"same","through":"same","more":false}`, sample(2, "2.00"))
	}))
	defer server.Close()
	c := client{http: server.Client(), base: server.URL, limit: 1}
	s := state{Version: 1, Cursor: "same", Resources: map[string]json.RawMessage{"activity/a": sample(1, "1.00")}, History: []change{{"same", sample(1, "1.00")}}}
	if err := c.catchup(context.Background(), &s, filepath.Join(t.TempDir(), "mirror")); err == nil {
		t.Fatal("conflicting duplicate cursor accepted")
	}
}

func TestInterruptedPageRetainsAtomicCursorAndFixedHighWater(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		calls++
		if calls == 1 {
			_, _ = fmt.Fprintf(w, `{"items":[{"cursor":"one","resource":%s}],"cursor":"one","through":"high","more":true}`, sample(1, "1.0000"))
			return
		}
		if r.URL.Query().Get("cursor") != "one" || r.URL.Query().Get("through") != "high" {
			t.Error("catch-up pass changed", r.URL)
		}
		w.Header().Set("Content-Length", "1000")
		_, _ = fmt.Fprint(w, `{"items":[]`)
	}))
	defer server.Close()
	c := client{http: server.Client(), base: server.URL, limit: 1}
	s := state{Version: 1, Cursor: "zero", Resources: map[string]json.RawMessage{}, History: []change{}}
	path := filepath.Join(t.TempDir(), "mirror.json")
	if err := c.catchup(context.Background(), &s, path); err == nil || !strings.Contains(err.Error(), "interrupted response") {
		t.Fatal("truncated response accepted", err)
	}
	saved, err := readState(path)
	if err != nil || saved.Cursor != "one" || saved.Through != "high" || len(saved.Resources) != 1 {
		t.Fatal("cursor/mirror not atomic", saved, err)
	}
	if err = c.catchup(context.Background(), &saved, path); err == nil {
		t.Fatal("restart interruption accepted")
	}
}
func TestExplicitRebootstrapAndFailedBaselinePreservesPriorFile(t *testing.T) {
	invalidated := false
	broken := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/changes" {
			if r.URL.Query().Get("cursor") == "old" {
				invalidated = true
				w.WriteHeader(409)
				_, _ = fmt.Fprint(w, `{"code":"cursor_invalidated","message":"bootstrap"}`)
				return
			}
			if broken {
				w.WriteHeader(503)
				_, _ = fmt.Fprint(w, `{"code":"rebuild_in_progress","message":"wait"}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"items":[],"cursor":"new","through":"new","more":false}`)
			return
		}
		if r.URL.Path == "/v1/checkpoint" {
			_, _ = fmt.Fprint(w, `{"cursor":"new"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"items":[],"next":null}`)
	}))
	defer server.Close()
	var progress bytes.Buffer
	c := client{http: server.Client(), base: server.URL, limit: 1, stderr: &progress}
	path := filepath.Join(t.TempDir(), "mirror.json")
	old := state{Version: 1, Cursor: "old", Resources: map[string]json.RawMessage{"activity/a": sample(1, "1.00")}, History: []change{}}
	if err := save(path, old); err != nil {
		t.Fatal(err)
	}
	s := old
	broken = true
	if _, err := c.sync(context.Background(), &s, path); err == nil {
		t.Fatal("failed bootstrap accepted")
	}
	saved, err := readState(path)
	if err != nil || saved.Cursor != "old" || len(saved.Resources) != 1 {
		t.Fatal("failed rebootstrap replaced old mirror", err)
	}
	broken = false
	s = old
	rebootstrap, err := c.sync(context.Background(), &s, path)
	if err != nil || !rebootstrap || !invalidated || !strings.Contains(progress.String(), "explicitly replacing mirror") || len(s.Resources) != 0 {
		t.Fatal("rebootstrap", rebootstrap, err, progress.String())
	}
}
func TestConsumerFailurePaths(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"--socket", filepath.Join(dir, "absent.sock"), "--state", filepath.Join(dir, "mirror")}, &stdout, &stderr); err == nil || !strings.Contains(stderr.String(), "Connecting to Wake") {
		t.Fatal("unavailable socket was silent", err)
	}
	s := state{Version: 1, Cursor: "cursor", Resources: map[string]json.RawMessage{"activity/a": sample(1, "1.0000")}, History: []change{}}
	if err := save(filepath.Join(dir, "missing", "mirror"), s); err == nil {
		t.Fatal("unwritable state accepted")
	}
	fixture := filepath.Join(dir, "assert.json")
	if err := os.WriteFile(fixture, []byte(`[{"kind":"activity","path":"activity.price.amount","equals":"1.0000","matches":1}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := assertions(fixture, s); err != nil {
		t.Fatal("exact fixture assertion", err)
	}
	if err := os.WriteFile(fixture, []byte(`[{"kind":"activity","path":"activity.price.amount","equals":"1","matches":1}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := assertions(fixture, s); err == nil {
		t.Fatal("failed fixture accepted")
	}
	for _, response := range []string{`{"items":null,"cursor":"end","through":"end","more":false}`, `{"items":[],"cursor":"","through":"end","more":false}`, `{"items":[],"cursor":"end","through":"end","more":true}`, `{} {}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, response)
		}))
		c := client{http: server.Client(), base: server.URL, limit: 1}
		if err := c.catchup(context.Background(), &s, filepath.Join(dir, "mirror")); err == nil {
			t.Error("malformed protocol accepted", response)
		}
		server.Close()
	}
}
