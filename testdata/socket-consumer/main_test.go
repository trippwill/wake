// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

var account = json.RawMessage(`{"id":"account","kind":"accounts","revision":1,"account":{"name":"Synthetic account","currency":null}}`)

func sample(revision int, price string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"id":"a","kind":"activity","revision":%d,"activity":{"instrument_id":null,"listing_id":null,"at":"2026-10-20T13:30:00.120Z","trade_kind":"execution","side":"buy","position_effect":null,"price_basis":"shares","settlement_date":null,"lifecycle_kind":null,"rate_base":null,"family":"trade","account_id":"account","state":"accepted","conflict":false,"date":"2026-10-20","order":null,"relationships":[],"quantity":{"value":"1.0000","unit":"shares"},"charges":[],"sent":null,"received":null,"option":null,"debt":null,"price":{"amount":%q,"currency":"USD"},"rate":null,"gross":null,"proceeds":null,"net":null,"net_with_billable":null,"accrued_interest":null}}`, revision, price))
}
func fixture(version string, revision int, price string) snapshot {
	return snapshot{"warehouse", version, []json.RawMessage{account, sample(revision, price)}}
}
func mirrorDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openMirror(filepath.Join(t.TempDir(), "mirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func requireState(t *testing.T, db *sql.DB, current state, version, price string) {
	t.Helper()
	stored, err := readState(db)
	if err != nil || stored.Version != version || current.Version != version || !sameJSON(stored.Resources["activity/a"], current.Resources["activity/a"]) {
		t.Fatal("mirror/version diverged", current.Version, stored.Version, err)
	}
	if price != "" && !bytes.Contains(stored.Resources["activity/a"], []byte(price)) {
		t.Fatal("exact price lost", stored)
	}
}
func TestSnapshotReplacementConditionalReadAndRestart(t *testing.T) {
	input := fixture("one", 1, "1.23000000000000000100")
	conditional := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/snapshot" {
			t.Error(r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"`+input.Version+`"`)
		if r.Header.Get("If-None-Match") == `"`+input.Version+`"` {
			conditional++
			w.WriteHeader(304)
			return
		}
		_ = json.NewEncoder(w).Encode(input)
	}))
	defer server.Close()
	var progress bytes.Buffer
	c := client{http: server.Client(), base: server.URL, stderr: &progress}
	db := mirrorDB(t)
	var current state
	if changed, err := c.sync(t.Context(), &current, db); err != nil || changed {
		t.Fatal(changed, err)
	}
	requireState(t, db, current, "one", "1.23000000000000000100")
	if _, err := c.sync(t.Context(), &current, db); err != nil || conditional != 1 {
		t.Fatal("conditional polling", conditional, err)
	}
	input = fixture("three", 3, "3.0000")
	if _, err := c.sync(t.Context(), &current, db); err != nil {
		t.Fatal(err)
	}
	requireState(t, db, current, "three", "3.0000")
	stored, err := readState(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.sync(t.Context(), &stored, db); err != nil || conditional != 2 {
		t.Fatal("restart baseline", err)
	}
	input = snapshot{"replacement", "new", []json.RawMessage{}}
	if changed, e := c.sync(t.Context(), &current, db); e != nil || !changed || len(current.Resources) != 0 || !strings.Contains(progress.String(), "Warehouse changed") {
		t.Fatal("new baseline not installed/reported", current, changed, e)
	}
}
func TestIncompleteResponsesAndFailedWritesPreservePriorMirror(t *testing.T) {
	good, _ := json.Marshal(fixture("next", 2, "2.00000000001"))
	for _, failure := range []string{"truncated", "trailing", "duplicate-key", "duplicate-resource", "missing-envelope", "missing-field", "unknown-field", "unknown-reference", "bad-decimal", "changed-revision", "regressed-revision", "write", "canceled-write", "conditional-without-matching-version"} {
		t.Run(failure, func(t *testing.T) {
			db := mirrorDB(t)
			current, err := validateSnapshot(fixture("prior", 1, "1.0000"))
			if err != nil {
				t.Fatal(err)
			}
			if err = save(t.Context(), db, current); err != nil {
				t.Fatal(err)
			}
			data := append([]byte(nil), good...)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ETag", `"next"`)
				switch failure {
				case "truncated":
					w.Header().Set("Content-Length", "100000")
					data = []byte(`{"warehouse_id":`)
				case "trailing":
					data = append(data, []byte(` {}`)...)
				case "duplicate-key":
					data = []byte(strings.Replace(string(data), `"version":"next"`, `"version":"prior","version":"next"`, 1))
				case "duplicate-resource":
					input := fixture("next", 2, "2.00")
					input.Resources = append(input.Resources, input.Resources[1])
					data, _ = json.Marshal(input)
				case "missing-envelope":
					data = []byte(`{"version":"next","resources":[]}`)
				case "missing-field":
					data = []byte(strings.Replace(string(data), `"price_basis":"shares",`, "", 1))
				case "unknown-field":
					data = []byte(strings.Replace(string(data), `"family":"trade"`, `"unrecognized":true,"family":"trade"`, 1))
				case "unknown-reference":
					data = []byte(strings.Replace(string(data), `"account_id":"account"`, `"account_id":"missing"`, 1))
				case "bad-decimal":
					data = []byte(strings.Replace(string(data), `"amount":"2.00000000001"`, `"amount":2.0`, 1))
				case "changed-revision":
					data, _ = json.Marshal(fixture("next", 1, "2.00"))
				case "regressed-revision":
					data = []byte(strings.Replace(string(data), `"revision":2`, `"revision":0`, 1))
				case "conditional-without-matching-version":
					w.Header().Set("ETag", `"other"`)
					w.WriteHeader(304)
					return
				}
				_, _ = w.Write(data)
			}))
			defer server.Close()
			if failure == "write" {
				if _, err = db.Exec(`CREATE TRIGGER reject_mirror BEFORE UPDATE ON mirror BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
					t.Fatal(err)
				}
			}
			c := client{http: server.Client(), base: server.URL, stderr: &bytes.Buffer{}}
			if failure == "canceled-write" {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				next, _ := validateSnapshot(fixture("next", 2, "2.00"))
				err = save(ctx, db, next)
			} else {
				_, err = c.sync(t.Context(), &current, db)
			}
			if err == nil {
				t.Fatal("failure accepted", failure)
			}
			requireState(t, db, current, "prior", "1.0000")
		})
	}
}
func TestPersistedMirrorRequiresCompleteValidEnvelope(t *testing.T) {
	db := mirrorDB(t)
	for _, payload := range []string{`{}`, `{"warehouse_id":"w","version":"v","resources":null}`, `{"warehouse_id":"w","version":"v","resources":{"wrong":{}}}`} {
		if _, err := db.Exec(statement("Replace"), []byte(payload)); err != nil {
			t.Fatal(err)
		}
		if _, err := readState(db); err == nil {
			t.Fatal("incomplete persisted mirror accepted", payload)
		}
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
		t.Fatal("consumer did not request snapshot")
	}
	if err = command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err != nil || strings.Contains(output.String(), `"failed"`) {
		t.Fatal("interrupt reported protocol failure", err, output.String())
	}
}

func TestOpenMirrorPreservesUnrelatedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unrelated.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	foreign, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = foreign.Exec(`CREATE TABLE sentinel(value TEXT NOT NULL);INSERT INTO sentinel VALUES('unrelated retained data');`); err != nil {
		t.Fatal(err)
	}
	if err = foreign.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := openMirror(path)
	if db != nil {
		_ = db.Close()
	}
	if err == nil {
		t.Error("accepted an unrelated database as a mirror")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("mirror admission modified an unrelated existing database")
	}
}

func TestOpenMirrorRejectsUnknownFilesWithoutWrites(t *testing.T) {
	for _, kind := range []string{"empty", "text", "other-identity", "other-version"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			if kind == "empty" || kind == "text" {
				data := []byte{}
				if kind == "text" {
					data = []byte("unrelated retained text")
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				db, err := openMirror(path)
				if err != nil {
					t.Fatal(err)
				}
				sql := `PRAGMA application_id=1;`
				if kind == "other-version" {
					sql = `PRAGMA user_version=2;`
				}
				if _, err = db.Exec(sql); err != nil {
					t.Fatal(err)
				}
				if err = db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			db, err := openMirror(path)
			if db != nil {
				_ = db.Close()
			}
			if err == nil {
				t.Fatal("unrecognized file was admitted")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected file changed", err)
			}
			for _, suffix := range []string{"-journal", "-wal", "-shm"} {
				if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("read-only admission created a sidecar", suffix, err)
				}
			}
		})
	}
}
