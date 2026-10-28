// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestIdleCatchupPreservesStateFileAndPersistsNewEmptyBaseline(t *testing.T) {
	for _, mode := range []string{"idle", "new-empty", "cursor-only", "finish-pass"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mirror.json")
			s := state{Version: 1, Cursor: "end", Resources: map[string]json.RawMessage{}, History: []change{}}
			if mode == "finish-pass" {
				s.Through = "end"
			}
			if mode == "cursor-only" {
				s.Cursor = "before"
			}
			var before os.FileInfo
			if mode != "new-empty" {
				s.Resources["activity/a"] = sample(1, "1.0000")
				s.History = []change{{"retained", sample(1, "1.0000")}}
				if err := save(path, s); err != nil {
					t.Fatal(err)
				}
				var err error
				s, err = readState(path)
				if err != nil {
					t.Fatal(err)
				}
				before, err = os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"items":[],"cursor":"end","through":"end","more":false}`)
			}))
			defer server.Close()
			c := client{http: server.Client(), base: server.URL, limit: 1}
			if err := c.catchup(t.Context(), &s, path); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "idle" && !os.SameFile(before, after) {
				t.Error("idle poll replaced durable mirror")
			}
			if (mode == "cursor-only" || mode == "finish-pass") && os.SameFile(before, after) {
				t.Error("changed checkpoint was not persisted")
			}
			saved, err := readState(path)
			if err != nil || saved.Cursor != "end" || saved.Through != "" || len(saved.Resources) != len(s.Resources) || len(saved.History) != len(s.History) {
				t.Fatal("saved state changed", saved, err)
			}
		})
	}
}

func TestFollowInterruptHelper(t *testing.T) {
	if os.Getenv("WAKE_INTERRUPT_HELPER") == "" {
		return
	}
	os.Args = []string{"socket-consumer", "--socket", os.Getenv("WAKE_INTERRUPT_SOCKET"), "--state", os.Getenv("WAKE_INTERRUPT_STATE"), "--follow"}
	main()
}

func TestFollowInterruptDuringRequestExitsSuccessfully(t *testing.T) {
	dir, err := os.MkdirTemp("", "wake-int-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "wake.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	requested := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requested)
		<-r.Context().Done()
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestFollowInterruptHelper$")
	command.Env = append(os.Environ(), "WAKE_INTERRUPT_HELPER=1", "WAKE_INTERRUPT_SOCKET="+socket, "WAKE_INTERRUPT_STATE="+filepath.Join(dir, "mirror.json"))
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill() }()
	select {
	case <-requested:
	case <-ctx.Done():
		t.Fatal("consumer did not request checkpoint")
	}
	if err = command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err != nil || strings.Contains(output.String(), `"failed"`) {
		t.Fatal("interrupt reported protocol failure", err, output.String())
	}
}

func TestPersistedStateRequiresCompleteEnvelopeAndRetainedMirror(t *testing.T) {
	baseline := state{Version: 1, Cursor: "end", Resources: map[string]json.RawMessage{"activity/a": sample(2, "2.0000")}, History: []change{{"earlier", sample(1, "1.0000")}}}
	for _, field := range []string{"version", "cursor", "through", "resources", "history"} {
		for _, mode := range []string{"missing", "null"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				data, err := json.Marshal(baseline)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err = json.Unmarshal(data, &fields); err != nil {
					t.Fatal(err)
				}
				if mode == "missing" {
					delete(fields, field)
				} else {
					fields[field] = json.RawMessage("null")
				}
				data, err = json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "mirror.json")
				if err = os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				var output, progress bytes.Buffer
				err = run(t.Context(), []string{"--socket", "/unused.sock", "--state", path}, &output, &progress)
				if err == nil || !strings.Contains(err.Error(), "invalid consumer state") || strings.Contains(output.String(), "synchronized") {
					t.Error("incomplete state did not fail before socket access", err, output.String())
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(after, data) {
					t.Fatal("invalid state was replaced", err)
				}
			})
		}
	}
	for _, mode := range []string{"missing-resource", "older-mirror", "conflicting-revision", "empty-history-cursor", "malformed-history", "conflicting-cursor", "repeated-cursor"} {
		t.Run(mode, func(t *testing.T) {
			s := state{Version: 1, Cursor: "end", Resources: map[string]json.RawMessage{"activity/a": sample(2, "2.0000")}, History: []change{{"end", sample(2, "2.0000")}}}
			switch mode {
			case "missing-resource":
				delete(s.Resources, "activity/a")
			case "older-mirror":
				s.Resources["activity/a"] = sample(1, "1.0000")
			case "conflicting-revision":
				s.Resources["activity/a"] = sample(2, "99.00")
			case "empty-history-cursor":
				s.History[0].Cursor = ""
			case "malformed-history":
				s.History[0].Resource = json.RawMessage(`{}`)
			case "repeated-cursor":
				s.History = append(s.History, s.History[0])
			case "conflicting-cursor":
				s.Resources["activity/a"] = sample(3, "3.0000")
				s.History = []change{{"same", sample(1, "1.0000")}, {"same", sample(2, "2.0000")}}
			}
			path := filepath.Join(t.TempDir(), "mirror.json")
			if err := save(path, s); err != nil {
				t.Fatal(err)
			}
			if _, err := readState(path); err == nil {
				t.Error("inconsistent persisted state accepted")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "valid.json")
	if err := save(path, baseline); err != nil {
		t.Fatal(err)
	}
	loaded, err := readState(path)
	if err != nil || len(loaded.Resources) != 1 || len(loaded.History) != 1 {
		t.Fatal("newer scan plus older replay rejected", loaded, err)
	}
}

func TestHistoricalRevisionContentCannotHideBehindNewerMirror(t *testing.T) {
	for _, retained := range []bool{false, true} {
		for _, conflict := range []bool{false, true} {
			t.Run(fmt.Sprintf("retained=%v/conflict=%v", retained, conflict), func(t *testing.T) {
				older := sample(1, "1.0000")
				replay := older
				if conflict {
					replay = sample(1, "99.0000")
				}
				s := state{Version: 1, Cursor: "start", Resources: map[string]json.RawMessage{"activity/a": sample(3, "3.0000")}, History: []change{}}
				if retained {
					s.History = []change{{"first", older}}
					s.Cursor = "first"
				}
				path := filepath.Join(t.TempDir(), "mirror.json")
				if err := save(path, s); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"items":[{"cursor":"first","resource":%s},{"cursor":"second","resource":%s}],"cursor":"second","through":"second","more":false}`, older, replay)
				}))
				defer server.Close()
				c := client{http: server.Client(), base: server.URL, limit: 2}
				err = c.catchup(t.Context(), &s, path)
				if (err != nil) != conflict {
					t.Error("older replay revision consistency", conflict, err)
				}
				if conflict {
					after, e := os.ReadFile(path)
					if e != nil || !bytes.Equal(after, before) {
						t.Error("conflicting replay replaced durable state", e)
					}
				}
				s.History = []change{{"first", older}, {"second", replay}}
				if err = save(path, s); err != nil {
					t.Fatal(err)
				}
				_, err = readState(path)
				if (err != nil) != conflict {
					t.Error("loaded history revision consistency", conflict, err)
				}
			})
		}
	}
}
