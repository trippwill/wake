// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const positionsFixture = `{"warehouse_id":"warehouse","account_id":"account","version":"positions-version","status":"available","as_of":{"date":"2026-11-04","at":"2026-11-04T12:00:00.1200Z","basis":"source_fetched"},"reason":null,"positions":[{"instrument_id":"instrument","quantity":{"value":"-0.0000000012300","unit":"units"},"side":"short","unit_cost_basis":{"amount":"3.2000","currency":"USD"},"option":null,"debt":null}]}`
const positionAccount = `{"id":"account","kind":"accounts","revision":1,"account":{"name":"Synthetic","currency":null}}`
const positionInstrument = `{"id":"instrument","kind":"instruments","revision":1,"instrument":{"name":"Synthetic","ticker":null,"figi":null,"underlier_id":null,"family":"debt","reference_status":"provisional","identifiers":[],"figi_allocation":null,"reference_reason":null}}`

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
		_, _ = fmt.Fprint(w, "event: publication\ndata: {\"warehouse_id\":\"warehouse\",\"version\":\"activity-version\"}\n\nevent: positions\ndata: {\"warehouse_id\":\"warehouse\",\"account_id\":\"account\",\"version\":\"positions-version\"}\n\n")
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
