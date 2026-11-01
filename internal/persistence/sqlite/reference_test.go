// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/internal/reference"
)

func insertReferenceInstrument(t *testing.T, s *Store, id, identifier string) {
	t.Helper()

	reported := fmt.Appendf(
		nil,
		`{"Reference":["test","contract","","%s"],"Kind":"equity","Ticker":"SYN","Name":"Source label","Identifiers":[{"Kind":"isin","Value":"%s","Field":"isin"}]}`,
		id,
		identifier,
	)
	if err := s.queries.PutInstrumentRoot(
		context.Background(),
		sqlc.PutInstrumentRootParams{
			ID:        id,
			SourceKey: id,
			Name:      "Source label",
			Ticker:    "SYN",
			Kind:      "equity",
			Reported:  reported,
			Rule:      application.InstrumentRule,
		},
	); err != nil {
		t.Fatal(err)
	}
}

func TestReferenceProfilesAndDurableAdmission(t *testing.T) {
	for _, limit := range []int{25, 250} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			s, path := testStore(t)
			now := time.Now().UTC()
			for i := 0; i < limit; i++ {
				granted, _, err := s.ReserveReference(context.Background(), now, limit)
				if err != nil || !granted {
					t.Fatal("early denial", i, err)
				}
			}

			if granted, _, err := s.ReserveReference(context.Background(), now, limit); err != nil || granted {
				t.Fatal("exceeded profile", err)
			}

			until := now.Add(2 * time.Minute)

			if err := s.ReferenceCooldown(context.Background(), until); err != nil {
				t.Fatal(err)
			}

			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			s, err := Open(
				context.Background(),
				testProfile(path),
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()

			if granted, next, err := s.ReserveReference(
				context.Background(),
				now.Add(time.Minute),
				limit,
			); err != nil || granted ||
				!next.Equal(until) {
				t.Fatal("restart lost cooldown", next, err)
			}

			if granted, _, err := s.ReserveReference(context.Background(), until, limit); err != nil || !granted {
				t.Fatal("cooldown did not recover", err)
			}
		})
	}
}

func TestReferenceBatchCacheAndFailureOutcomes(t *testing.T) {
	for _, keyed := range []bool{false, true} {
		t.Run(fmt.Sprint(keyed), func(t *testing.T) {
			ctx := context.Background()

			var diagnostics bytes.Buffer

			logger := slog.New(slog.NewJSONHandler(&diagnostics, &slog.HandlerOptions{Level: slog.LevelDebug}))
			defer func() {
				text := diagnostics.String()
				for _, private := range []string{"synthetic-openfigi-key", "replacement-openfigi-key", "identifier-", "Resolved ", "synthetic failure"} {
					if strings.Contains(text, private) {
						t.Error("private reference input leaked", private)
					}
				}

				for _, event := range []string{"reference_finished", "reference_cache_hit"} {
					if !strings.Contains(text, event) {
						t.Error("missing reference event", event)
					}
				}

				if keyed {
					for _, code := range []string{"invalid-key", "throttled", "temporary-failure"} {
						if !strings.Contains(text, `"code":"`+code+`"`) {
							t.Error("missing reference failure", code)
						}
					}
				}
			}()

			s, _ := testStore(t)
			envelope := testEnvelope(t)
			now := time.Now().UTC()

			batch := 10
			if keyed {
				batch = 100
			}

			for i := 0; i < batch+1; i++ {
				insertReferenceInstrument(t, s, fmt.Sprintf("root-%03d", i), fmt.Sprintf("identifier-%d", i))
			}

			calls := 0
			status := http.StatusOK
			ambiguous := false
			seenJobs := []int{}

			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++

				if (r.Header.Get("X-OPENFIGI-APIKEY") != "") == keyed {
				} else {
					t.Error("wrong reference credential profile")
				}

				var jobs []reference.Job
				if err := json.NewDecoder(r.Body).Decode(&jobs); err != nil {
					t.Error(err)
				}

				seenJobs = append(seenJobs, len(jobs))

				if status != http.StatusOK {
					if status == http.StatusTooManyRequests {
						w.Header().Set("Retry-After", "120")
					}

					w.WriteHeader(status)
					_, _ = fmt.Fprint(w, `{"error":"synthetic failure"}`)

					return
				}

				results := make([]map[string]any, len(jobs))
				for i, j := range jobs {
					if strings.HasSuffix(j.IDValue, "-0") {
						results[i] = map[string]any{"warning": "No match"}
						continue
					}

					records := []reference.Record{
						{
							FIGI:       "listing-" + j.IDValue,
							ShareClass: "root-" + j.IDValue,
							Name:       "Resolved " + j.IDValue,
							Market:     "Equity",
						},
					}
					if ambiguous {
						records = append(
							records,
							reference.Record{FIGI: "another", ShareClass: "another-root", Name: "Other", Market: "Equity"},
						)
					}

					results[i] = map[string]any{"data": records}
				}

				_ = json.NewEncoder(w).Encode(results)
			}))
			defer server.Close()

			service, err := reference.New(
				s,
				envelope,
				server.Client(),
				server.URL,
				func() time.Time { return now },
				logger,
			)
			if err != nil {
				t.Fatal(err)
			}

			if keyed {
				key := credential.NewSecret([]byte("synthetic-openfigi-key"))
				defer key.Clear()

				if err = service.SetKey(ctx, key); err != nil {
					t.Fatal(err)
				}
			}

			run := func() {
				t.Helper()

				if _, err = service.RunOnce(ctx); err != nil {
					t.Fatal(err)
				}
			}
			run()
			run()

			if len(seenJobs) != 2 || seenJobs[0] != batch || seenJobs[1] != 1 {
				t.Fatal("wrong batch limits", seenJobs)
			}

			run()

			if calls != 2 {
				t.Fatal("ignored success cache")
			}

			noMatch, _ := s.queries.Instrument(ctx, "root-000")
			resolved, _ := s.queries.Instrument(ctx, "root-001")
			if noMatch.ReferenceStatus != "no-match" || resolved.ReferenceStatus != "resolved" {
				t.Fatal("wrong outcomes", noMatch, resolved)
			}
			// A duplicate lookup reuses retained success evidence without another request.
			insertReferenceInstrument(t, s, "duplicate", "identifier-1")
			run()

			if calls != 2 {
				t.Fatal("duplicate lookup was not suppressed")
			}

			duplicate, e := canonicalID(ctx, s.queries, "instruments", "duplicate")
			if e != nil || duplicate != "root-001" {
				t.Fatal("matching verified FIGIs did not converge", duplicate, e)
			}

			now = now.Add(6 * time.Hour)

			run()

			if calls != 3 {
				t.Fatal("no-match expiry", calls)
			}

			now = now.Add(7 * 24 * time.Hour)
			ambiguous = true

			run()

			r, _ := s.queries.Instrument(ctx, "root-002")
			if batch > 2 && r.ReferenceStatus != "ambiguous" {
				t.Fatal("arbitrary ambiguity selection", r)
			}
			// A configured invalid key remains a failure, never an anonymous retry.
			if keyed {
				now = now.Add(8 * 24 * time.Hour)
				status = http.StatusUnauthorized

				run()

				before := calls

				run()

				if calls != before {
					t.Fatal("invalid key retried or became anonymous")
				}

				_, failure, e := s.ReferenceKey(ctx)
				if e != nil || failure == "" {
					t.Fatal("missing actionable key failure", e)
				}

				key := credential.NewSecret([]byte("replacement-openfigi-key"))
				defer key.Clear()

				if e = service.SetKey(ctx, key); e != nil {
					t.Fatal(e)
				}

				status = http.StatusTooManyRequests

				run()

				before = calls
				now = now.Add(time.Minute)

				run()

				if calls != before {
					t.Fatal("ignored shared throttle")
				}

				now = now.Add(time.Minute)
				status = http.StatusServiceUnavailable

				run()
				// Transient failures never become successful negative cache entries.
				cache, found, e := s.ReferenceCached(
					ctx,
					reference.Job{IDType: "ID_ISIN", IDValue: "identifier-0"}.Key(),
					now,
				)
				if e != nil || found && cache.Status == "temporary-failure" {
					t.Fatal("cached failure as negative", cache, e)
				}
			}
		})
	}
}

func TestReferenceDuplicateJobsAndRetainedEvidence(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	now := time.Now().UTC()

	insertReferenceInstrument(t, s, "a", "same-identifier")
	insertReferenceInstrument(t, s, "b", "same-identifier")

	jobs := 0

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input []reference.Job

		_ = json.NewDecoder(r.Body).Decode(&input)
		jobs += len(input)
		_, _ = fmt.Fprint(
			w,
			`[{"data":[{"figi":"listing","shareClassFIGI":"root","name":"Unambiguous name","marketSector":"Equity"}]}]`,
		)
	}))
	defer server.Close()

	service, err := reference.New(s, nil, server.Client(), server.URL, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = service.RunOnce(ctx); err != nil || jobs != 1 {
		t.Fatal("duplicate batch job", jobs, err)
	}

	attempts, err := s.queries.InstrumentReferenceAttempts(ctx, "a")
	if err != nil || len(attempts) != 1 {
		t.Fatal(err)
	}

	evidence, err := s.ReferenceEvidence(ctx, attempts[0].EvidenceID)
	if err != nil || !strings.Contains(evidence.Response, "Unambiguous name") ||
		!strings.Contains(evidence.Request, "same-identifier") {
		t.Fatal("missing resolution evidence", err, evidence)
	}

	cache, found, err := s.ReferenceCached(
		ctx,
		reference.Job{IDType: "ID_ISIN", IDValue: "same-identifier"}.Key(),
		now.Add(8*24*time.Hour),
	)
	if err != nil || found || cache.Evidence != "" {
		t.Fatal("cache did not expire", err)
	}

	evidence, err = s.ReferenceEvidence(ctx, attempts[0].EvidenceID)
	if err != nil || !bytes.Contains([]byte(evidence.Response), []byte("Unambiguous name")) {
		t.Fatal("cache expiry erased accepted evidence", err)
	}
}
