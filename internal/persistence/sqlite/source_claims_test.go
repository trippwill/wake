// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
	"go.trippwill.dev/wake/source"
)

func recordTestReceipt(t *testing.T, s *Store, id string) {
	t.Helper()

	_, err := s.db.Exec(
		`INSERT INTO acquisition_receipts(id,artifact_id,operation,retrieved,hash,policy,digest,requested_from,requested_to,reported_from,reported_to) SELECT id,id,operation,retrieved,hash,'exact-bytes/1',hash,requested_from,requested_to,reported_from,reported_to FROM artifacts WHERE id=?`,
		id,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func acquireClaimReport(t *testing.T, s *Store, body string, equivalence bool) string {
	t.Helper()
	ctx := t.Context()

	artifact, err := provider.NewArtifact(
		ibkrflex.ConfirmationFormat,
		"application/xml",
		time.Now(),
		[]byte(body),
	)
	if err != nil {
		t.Fatal(err)
	}

	if equivalence {
		artifact, err = artifact.WithEquivalence("synthetic-window/1", strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
	}

	key := host.WorkKey{Connection: "synthetic", Stream: "confirmations", Operation: "acquire"}

	baseline, err := s.Checkpoint(ctx, key)
	if err != nil {
		t.Fatal(err)
	}

	step, err := provider.NewStep(provider.State{}, artifact, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	id, err := s.Accept(ctx, key, baseline.Revision, step, nil, provider.Progress{})
	if err != nil {
		t.Fatal(err)
	}

	r, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err = s.Normalize(ctx, id, ibkrflex.Version, r.Normalizer()); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(ctx, id, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	return id
}

func tableCount(t *testing.T, s *Store, table string) int {
	t.Helper()

	var n int
	// Fixed test-only identifiers, never user input.
	if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}

	return n
}

func TestAcquisitionReceiptsReuseBodiesAndNormalization(t *testing.T) {
	for _, equivalence := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "window"}[equivalence], func(t *testing.T) {
			s := coverageStore(t)
			body := singleClaimBody(equityRow("U1", "fill", "100"))
			first := acquireClaimReport(t, s, body, equivalence)

			baseline, err := snapshotState(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}

			if equivalence {
				body = strings.Replace(body, `fromDate="2026-10-20"`, `fromDate="2026-10-19"`, 1)
			}

			for range 4 {
				if next := acquireClaimReport(t, s, body, equivalence); next != first {
					t.Fatal("repeated body retained", first, next)
				}
			}

			for table, want := range map[string]int{"acquisition_receipts": 5, "artifacts": 2, "source_claims": 1, "records": 1, "attempts": 1, "claim_processing": 1} {
				if got := tableCount(t, s, table); got != want {
					t.Fatal(table, got, want)
				}
			}

			receipts, err := s.queries.AcquisitionReceipts(t.Context(), first)
			if err != nil || len(receipts) != 5 {
				t.Fatal(receipts, err)
			}

			if changes := snapshotChanges(t, s, baseline); len(changes.Items) != 0 {
				t.Fatal("repeated acquisition published", changes)
			}
		})
	}
}

func TestSourceClaimsCompareBeforeOperatorCorrectionAndKeepMultiplicity(t *testing.T) {
	s := coverageStore(t)
	row := strings.Replace(equityRow("U1", "fill", "100"), `quantity="0.000000000123"`, `quantity="10"`, 1)
	// Use the source fixture's exact quantity spelling deliberately.
	first := acquireClaimReport(t, s, singleClaimBody(row), false)
	view, err := s.Dashboard(t.Context())
	if err != nil || len(view.Executions) != 1 {
		t.Fatal(view, err)
	}

	id := view.Executions[0].ID()
	retainDecision(t, s, "activity", id, "quantity", "12", "", "set")

	baseline, err := snapshotState(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	// Different bytes and locations, same pre-correction source claim.
	next := acquireClaimReport(t, s, singleClaimBody("\n"+row+"\n"), false)
	if next == first {
		t.Fatal("changed bytes were discarded")
	}

	detail, err := s.ExecutionDetail(t.Context(), id)
	if err != nil || detail.Execution.Terms().Fields().Quantity.Value() != "12" {
		t.Fatal("source repeat changed operator value", detail, err)
	}

	if tableCount(t, s, "source_claims") != 1 || tableCount(t, s, "claim_processing") != 1 ||
		tableCount(t, s, "records") != 2 {
		t.Fatal("known source claim repeated processing")
	}

	if changes := snapshotChanges(t, s, baseline); len(changes.Items) != 0 {
		t.Fatal("known claim published", changes)
	}

	acquireClaimReport(t, s, singleClaimBody(row+row), false)

	if tableCount(t, s, "source_claims") != 1 || tableCount(t, s, "claim_processing") != 2 ||
		tableCount(t, s, "records") != 4 {
		t.Fatal("duplicate row multiplicity lost")
	}
	// A genuinely disputed source variant remains a different claim.
	acquireClaimReport(t, s, singleClaimBody(strings.Replace(row, `price="100"`, `price="101"`, 1)), false)

	if tableCount(t, s, "source_claims") != 2 {
		t.Fatal("disputed variant lost")
	}

	values, err := recordValues(t.Context(), s.queries, first, ibkrflex.Version)
	if err != nil || len(values) != 1 {
		t.Fatal(values, err)
	}

	raw, err := s.queries.Records(t.Context(), sqlc.RecordsParams{ArtifactID: first, Version: ibkrflex.Version})
	if err != nil || len(raw) != 1 || strings.Contains(string(raw[0].Data), values[0].Location) {
		t.Fatal("structural locator leaked into source claim", raw, err)
	}
}

func singleClaimBody(rows string) string {
	return `<FlexQueryResponse type="TCF"><FlexStatements><FlexStatement accountId="U1" fromDate="2026-10-20" toDate="2026-10-21"><TradeConfirms>` + rows + `</TradeConfirms></FlexStatement></FlexStatements></FlexQueryResponse>`
}

func TestDiscardedWindowBytesDoNotBecomeExactMatches(t *testing.T) {
	s := coverageStore(t)
	body := singleClaimBody(equityRow("U1", "fill", "100"))

	first := acquireClaimReport(t, s, body, true)
	window := strings.Replace(body, `fromDate="2026-10-20"`, `fromDate="2026-10-19"`, 1)
	if acquireClaimReport(t, s, window, true) != first {
		t.Fatal("equivalent window did not reuse body")
	}

	exact := acquireClaimReport(t, s, window, false)
	if exact == first {
		t.Fatal("default comparison reused unretained bytes")
	}

	if acquireClaimReport(t, s, body, false) != first {
		t.Fatal("exact retained bytes were copied")
	}

	receipts, err := s.queries.AcquisitionReceipts(t.Context(), first)
	if err != nil || len(receipts) != 3 || receipts[0].Hash == receipts[1].Hash {
		t.Fatal("actual receipt digests lost", receipts, err)
	}

	if tableCount(t, s, "source_claims") != 1 || tableCount(t, s, "claim_processing") != 1 {
		t.Fatal("window variation repeated financial processing")
	}
}

func TestDiscoveryBodyCanLaterBeAcquiredAndAcknowledgementIsAtomic(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	body := singleClaimBody(equityRow("U1", "fill", "100"))

	a, err := provider.NewArtifact(ibkrflex.ConfirmationFormat, "application/xml", time.Now(), []byte(body))
	if err != nil {
		t.Fatal(err)
	}

	step, err := provider.NewStep(provider.State{}, a, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	discover := host.WorkKey{Connection: "synthetic", Stream: "confirmations", Operation: "discover"}

	baseline, err := s.Checkpoint(ctx, discover)
	if err != nil {
		t.Fatal(err)
	}

	id, err := s.Accept(ctx, discover, baseline.Revision, step, nil, provider.Progress{})
	if err != nil {
		t.Fatal(err)
	}

	acquire := discover
	acquire.Operation = "acquire"

	baseline, err = s.Checkpoint(ctx, acquire)
	if err != nil {
		t.Fatal(err)
	}

	same, err := s.Accept(ctx, acquire, baseline.Revision, step, nil, provider.Progress{})
	if err != nil || same != id {
		t.Fatal(id, same, err)
	}

	pending, err := s.PendingArtifacts(ctx, time.Now(), []string{"ibkr-flex"})
	if err != nil || len(pending) != 1 || pending[0].ID != id {
		t.Fatal("first discovery operation hid acquired body", pending, err)
	}

	if _, err = s.Accept(
		ctx,
		acquire,
		baseline.Revision,
		step,
		nil,
		provider.Progress{},
	); err != provider.ErrConflict {
		t.Fatal("stale acknowledgement accepted", err)
	}

	if tableCount(t, s, "acquisition_receipts") != 2 {
		t.Fatal("stale acknowledgement leaked receipt")
	}
}

func TestSourceClaimKeysPreservePrecisionUnknownsAndQualifiedIdentity(t *testing.T) {
	s := coverageStore(t)
	id := acquireClaimReport(t, s, singleClaimBody(equityRow("U1", "fill", "100")), false)
	raw, err := s.queries.Records(t.Context(), sqlc.RecordsParams{ArtifactID: id, Version: ibkrflex.Version})
	if err != nil || len(raw) != 1 {
		t.Fatal(raw, err)
	}

	var original tradeData
	if err = json.Unmarshal(raw[0].Data, &original); err != nil {
		t.Fatal(err)
	}

	original.EventAliases = []refData{{"source", "event", "scope", "one"}, {"source", "event", "scope", "two"}}
	original.Charges = []chargeData{
		{Field: "b", Money: moneyData{Amount: "1.00", Currency: "USD"}},
		{Field: "a", Money: moneyData{Amount: "2", Currency: "USD"}},
	}
	baseline, _ := json.Marshal(original)

	canonical, err := claimKeyData("trade", baseline)
	if err != nil {
		t.Fatal(err)
	}

	reordered := original
	reordered.EventAliases = append([]refData(nil), original.EventAliases...)
	slices.Reverse(reordered.EventAliases)

	reordered.Charges = append([]chargeData(nil), original.Charges...)
	slices.Reverse(reordered.Charges)
	encoded, _ := json.Marshal(reordered)

	same, err := claimKeyData("trade", encoded)
	if err != nil || !bytes.Equal(same, canonical) {
		t.Fatal("set order changed source identity", err)
	}

	for _, mutate := range []func(*tradeData){
		func(d *tradeData) { d.Location = "different/path" },
	} {
		changed := original
		mutate(&changed)
		encoded, _ = json.Marshal(changed)

		same, err = claimKeyData("trade", encoded)
		if err != nil || !bytes.Equal(same, canonical) {
			t.Fatal("locator changed source identity", err)
		}
	}

	for _, mutate := range []func(*tradeData){
		func(d *tradeData) { d.Price = "100.0" },
		func(d *tradeData) { d.ExecutedAt = strings.Replace(d.ExecutedAt, ".120", ".12", 1) },
		func(d *tradeData) { d.Event[2] = "other-scope" },
		func(d *tradeData) { d.Net = moneyData{} },
		func(d *tradeData) { d.Charges = append(append([]chargeData(nil), d.Charges...), d.Charges[0]) },
	} {
		changed := original
		mutate(&changed)

		encoded, _ = json.Marshal(changed)
		next, e := claimKeyData("trade", encoded)
		if e != nil || bytes.Equal(next, canonical) {
			t.Fatal("meaning or multiplicity lost", changed, e)
		}
	}
}

func TestReceiptCoverageSurvivesEquivalentBodyReuse(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	firstDate, _ := financial.ParseDate("2026-10-19")

	lastDate, _ := financial.ParseDate("2026-10-21")
	key := host.WorkKey{Connection: "synthetic", Stream: "confirmations", Operation: "acquire"}

	var bodyID string
	for i := range 2 {
		body := []byte(singleClaimBody(equityRow("U1", "fill", "100")))
		if i == 1 {
			body = bytes.Replace(body, []byte(`fromDate="2026-10-20"`), []byte(`fromDate="2026-10-19"`), 1)
		}

		a, err := provider.NewArtifact(
			ibkrflex.ConfirmationFormat,
			"application/xml",
			time.Date(2026, 10, 22+i, 12, 0, 0, 0, time.UTC),
			body,
		)
		if err != nil {
			t.Fatal(err)
		}

		a, err = a.WithEquivalence("synthetic-window/1", strings.Repeat("b", 64))
		if err != nil {
			t.Fatal(err)
		}

		coverage, err := provider.NewCoverage(firstDate, lastDate, firstDate, lastDate)
		if err != nil {
			t.Fatal(err)
		}

		if i == 1 {
			coverage, err = provider.NewCoverage(lastDate, lastDate, firstDate, lastDate)
			if err != nil {
				t.Fatal(err)
			}
		}

		step, err := provider.NewStep(provider.State{}, a, time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}

		step, err = step.WithCoverage(coverage)
		if err != nil {
			t.Fatal(err)
		}

		cp, err := s.Checkpoint(ctx, key)
		if err != nil {
			t.Fatal(err)
		}

		id, err := s.Accept(ctx, key, cp.Revision, step, nil, provider.Progress{})
		if err != nil {
			t.Fatal(err)
		}

		if i == 0 {
			bodyID = id
		} else if id != bodyID {
			t.Fatal("window variant retained twice")
		}
	}

	receipts, err := s.queries.AcquisitionReceipts(ctx, bodyID)
	if err != nil || len(receipts) != 2 {
		t.Fatal(receipts, err)
	}

	if receipts[0].RequestedFrom != "2026-10-19" || receipts[1].RequestedFrom != "2026-10-21" ||
		receipts[1].ReportedFrom != "2026-10-19" ||
		receipts[1].Retrieved != "2026-10-23T12:00:00Z" ||
		receipts[0].Hash == receipts[1].Hash {
		t.Fatal("receipt context collapsed", receipts)
	}

	retained, err := s.Retained(ctx, bodyID)
	if err != nil || retained.Artifact.Hash() != receipts[0].Hash {
		t.Fatal("retained original body changed", retained, err)
	}
}

func TestLatestDiscoveryProfileFollowsReceiptsWhenBodyIsReused(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	if err := s.Configure(
		ctx,
		host.Connection{
			ID:       "synthetic",
			Provider: "ibkr-flex",
			Name:     "Synthetic",
			Settings: confirmationSettings("123"),
		},
		nil,
	); err != nil {
		t.Fatal(err)
	}

	key := host.WorkKey{Connection: "synthetic", Stream: "confirmations", Operation: "discover"}

	ref, err := source.NewReference("synthetic", "account", "scope", "one")
	if err != nil {
		t.Fatal(err)
	}

	var first, last string
	for i, report := range []struct{ body, name, currency string }{{"A", "First", "USD"}, {"B", "Second", "GBP"}, {"A", "Latest", "EUR"}} {
		currency, e := financial.ParseCurrency(report.currency)
		if e != nil {
			t.Fatal(e)
		}

		candidate, e := provider.NewCandidate(ref, report.name, currency)
		if e != nil {
			t.Fatal(e)
		}

		a, e := provider.NewArtifact("synthetic", "text/plain", time.Now(), []byte(report.body))
		if e != nil {
			t.Fatal(e)
		}

		step, e := provider.NewStep(provider.State{}, a, time.Now().Add(time.Hour))
		if e != nil {
			t.Fatal(e)
		}

		cp, e := s.Checkpoint(ctx, key)
		if e != nil {
			t.Fatal(e)
		}

		last, e = s.Accept(ctx, key, cp.Revision, step, []provider.Candidate{candidate}, provider.Progress{})
		if e != nil {
			t.Fatal(e)
		}

		if i == 0 {
			first = last
		}
	}

	if first != last {
		t.Fatal("discovery body was not reused", first, last)
	}

	view, err := s.Dashboard(ctx)
	if err != nil || len(view.Connections) != 1 || len(view.Connections[0].Candidates) != 1 {
		t.Fatal(view, err)
	}

	latest := view.Connections[0].Candidates[0]
	if latest.Artifact != first || latest.Candidate.Name() != "Latest" ||
		latest.Candidate.Currency().String() != "EUR" {
		t.Fatal("stale discovery profile", latest)
	}

	candidates, err := s.Candidates(ctx, "synthetic")
	if err != nil || len(candidates) != 3 {
		t.Fatal("prior discovery appearances lost", candidates, err)
	}

	details, err := application.NewAccountDetails("Chosen", "EUR")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = s.Confirm(ctx, "synthetic", last, ref, details, true); err != nil {
		t.Fatal("repeated discovery could not be confirmed", err)
	}
}
