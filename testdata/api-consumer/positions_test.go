// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const positionsFixture = `{"baseline_id":"warehouse","account_id":"account","version":"positions-version","status":"available","as_of":{"date":"2026-11-04","at":"2026-11-04T12:00:00.1200Z","basis":"source_fetched"},"reason":null,"positions":[{"instrument_id":"instrument","quantity":{"value":"-0.0000000012300","unit":"units"},"side":"short","unit_cost_basis":{"amount":"3.2000","currency":"USD","quotation_basis":null},"instrument_terms":{"family":"debt","type":null,"currency":null,"maturity":null,"issue_date":null,"dated_date":null,"coupon_rate":null,"payments_per_year":null}}]}`
const positionAccount = `{"id":"account","kind":"accounts","revision":1,"account":{"name":"Synthetic","currency":null}}`
const positionInstrument = `{"id":"instrument","kind":"instruments","revision":1,"instrument":{"name":"Synthetic","ticker":null,"figi":null,"underlier_id":null,"family":"debt","reference_status":"provisional","identifiers":[],"figi_allocation":null,"reference_reason":null}}`

func TestPositionsAdapterReusesSavedPublication(t *testing.T) {
	var unchanged, interrupted, empty atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/accounts/account/positions":
			condition := r.Header.Get("If-None-Match")
			if (unchanged.Load() || interrupted.Load()) && condition != `"positions-version"` {
				t.Errorf("expected saved positions condition, got %q", condition)
			}
			if empty.Load() {
				w.WriteHeader(http.StatusOK)
				return
			}
			if interrupted.Load() {
				w.Header().Set("ETag", `"new-version"`)
				w.Header().Set("Content-Length", "100")
				_, _ = io.WriteString(w, "{")
				return
			}
			w.Header().Set("ETag", `"positions-version"`)
			if condition == `"positions-version"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = io.WriteString(w, positionsFixture)
		case "/v1/accounts/account":
			_, _ = io.WriteString(w, positionAccount)
		case "/v1/instruments/instrument":
			_, _ = io.WriteString(w, positionInstrument)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("fake-token"), 0600); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(adapterRequest{
		Endpoint: server.URL, TokenFile: token, AccountID: "account",
		Protocol: 1, Operation: "sync", Profile: "positions", StateDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = adapter(t.Context(), bytes.NewReader(request), io.Discard); err != nil {
		t.Fatal(err)
	}

	unchanged.Store(true)
	if err = adapter(t.Context(), bytes.NewReader(request), io.Discard); err != nil {
		t.Fatalf("unchanged synchronization failed: %v", err)
	}

	empty.Store(true)
	var output bytes.Buffer
	if err = adapter(t.Context(), bytes.NewReader(request), &output); !errors.Is(err, io.EOF) {
		t.Fatalf("empty positions response returned %v, want EOF", err)
	}
	if bytes.Contains(output.Bytes(), []byte(`"event":"complete"`)) {
		t.Fatalf("adapter reported completion after an empty positions response: %s", &output)
	}
	empty.Store(false)

	interrupted.Store(true)
	if err = adapter(t.Context(), bytes.NewReader(request), io.Discard); err == nil {
		t.Fatal("interrupted synchronization succeeded")
	}
	saved, err := os.ReadFile(filepath.Join(dir, "positions.json"))
	if err != nil || !sameJSON(saved, []byte(positionsFixture)) {
		t.Fatalf("interrupted synchronization did not preserve the committed view: %s, %v", saved, err)
	}
}

func TestSeparatePositionsValidateResolveAndKeepPriorOnFailure(t *testing.T) {
	for _, failure := range []string{"", "availability", "side", "lookup", "duplicate", "quantity"} {
		t.Run(failure, func(t *testing.T) {
			body := positionsFixture
			switch failure {
			case "availability":
				body = strings.Replace(body, `"status":"available"`, `"status":"unavailable"`, 1)
			case "side":
				body = strings.Replace(body, `"side":"short"`, `"side":"long"`, 1)
			case "duplicate":
				body = strings.Replace(body, `"version":"positions-version"`, `"version":"v","version":"x"`, 1)
			case "quantity":
				body = strings.Replace(body, `-0.0000000012300`, `0.0000`, 1)
			}
			conditional := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/accounts/account/positions":
					w.Header().Set("ETag", `"positions-version"`)
					if r.Header.Get("If-None-Match") == `"positions-version"` {
						conditional++
						w.WriteHeader(304)
						return
					}
					_, _ = io.WriteString(w, body)
				case "/v1/accounts/account":
					_, _ = io.WriteString(w, positionAccount)
				case "/v1/instruments/instrument":
					if failure == "lookup" {
						w.WriteHeader(404)
						_, _ = io.WriteString(w, `{"code":"not_found","message":"missing"}`)
					} else {
						_, _ = io.WriteString(w, positionInstrument)
					}
				default:
					t.Error("positions client requested unrelated endpoint", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			c := client{http: server.Client(), base: server.URL, stderr: io.Discard}
			prior := positionsView{Version: "old"}
			got, err := c.readPositions(t.Context(), "account", prior)
			if failure != "" {
				if err == nil || got.Version != prior.Version {
					t.Fatal(got, err)
				}
				return
			}
			if err != nil || got.Version != "positions-version" {
				t.Fatal(got, err)
			}
			if _, err = c.readPositions(t.Context(), "account", got); err != nil || conditional != 1 {
				t.Fatal(conditional, err)
			}
		})
	}
}

func TestPositionsEventsValidateAndIgnorePublicationVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: publication\ndata: {\"baseline_id\":\"warehouse\",\"version\":\"activity-version\"}\n\nevent: positions\ndata: {\"baseline_id\":\"warehouse\",\"account_id\":\"account\",\"version\":\"positions-version\"}\n\n")
	}))
	defer server.Close()
	c := client{http: server.Client(), base: server.URL, stderr: io.Discard}
	seen := 0
	c.positionUpdate = func(p positionsPublication) error {
		seen++
		if p.Version != "positions-version" || p.AccountID != "account" {
			t.Error(p)
		}
		return nil
	}
	if err := c.events(context.Background(), func(publication) error { return nil }); !errors.Is(err, io.EOF) || seen != 1 {
		t.Fatal(seen, err)
	}
}
