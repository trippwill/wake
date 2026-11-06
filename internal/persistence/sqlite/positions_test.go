// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
	"go.trippwill.dev/wake/source"
)

func reportedPositionsXML(date, sections string) string {
	return `<FlexQueryResponse type="AF"><FlexStatements><FlexStatement accountId="U1" fromDate="2026-10-01" toDate="` + date + `">` + sections + `</FlexStatement></FlexStatements></FlexQueryResponse>`
}

func holdingXML(date, quantity, cost string) string {
	extra := ""
	if cost != "" {
		extra = ` costBasisMoney="` + cost + `"`
	}

	return fmt.Sprintf(
		`<OpenPosition accountId="U1" conid="100" assetCategory="STK" symbol="SYN" levelOfDetail="SUMMARY" reportDate="%s" position="%s" currency="USD"%s/>`,
		date,
		quantity,
		extra,
	)
}

func readPositions(t *testing.T, s *Store) application.PositionsView {
	t.Helper()

	read, err := s.Positions(t.Context(), "account", application.SnapshotCondition{})
	if err != nil {
		t.Fatal(err)
	}

	return read.View
}

func TestPositionsReplaceWholeSetsWithIndependentPublicationAndAvailability(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "baseline", equityRow("U1", "holding", "100"))

	snapshot, _ := s.Snapshot(t.Context(), application.SnapshotCondition{})
	initial := readPositions(t, s)
	if initial.Status != "unavailable" || initial.Positions != nil || *initial.Reason != "not_reported" {
		t.Fatal(initial)
	}

	acquireRealized(
		t,
		s,
		reportedPositionsXML(
			"2026-11-01",
			"<OpenPositions>"+holdingXML("2026-11-01", "10.00000000000000100", "0.0000")+"</OpenPositions>",
		),
	)

	first := readPositions(t, s)
	if first.Status != "available" || first.AsOf.At != nil || len(first.Positions) != 1 ||
		first.Positions[0].Quantity.Value != "10.00000000000000100" ||
		first.Positions[0].CostBasis.Amount != "0.0000" ||
		first.Version == initial.Version {
		t.Fatal(first)
	}

	unchanged, err := s.Snapshot(t.Context(), application.MatchingSnapshotVersions(snapshot.Snapshot.Version))
	if err != nil || !unchanged.Unchanged {
		t.Fatal("positions changed activity snapshot", err)
	}

	if read, err := s.Positions(
		t.Context(),
		"account",
		application.MatchingSnapshotVersions(first.Version),
	); err != nil ||
		!read.Unchanged {
		t.Fatal(read, err)
	}

	if _, err = s.Resource(t.Context(), "instruments", first.Positions[0].InstrumentID); err != nil {
		t.Fatal(err)
	}

	changes, remove := s.SubscribePublications()
	defer remove()

	acquireRealized(
		t,
		s,
		reportedPositionsXML(
			"2026-11-01",
			"<OpenPositions>"+holdingXML(
				"2026-11-01",
				"10.00000000000000100",
				"0.0000",
			)+"</OpenPositions><Ignored/>",
		),
	)

	if readPositions(t, s).Version != first.Version || len(changes) != 0 {
		t.Fatal("repeat changed positions")
	}

	acquireRealized(
		t,
		s,
		reportedPositionsXML(
			"2026-11-02",
			"<OpenPositions>"+holdingXML("2026-11-02", "-2.0000", "")+"</OpenPositions>",
		),
	)

	next := readPositions(t, s)
	if next.Version == first.Version || next.Positions[0].Side != "short" ||
		next.Positions[0].CostBasis != nil ||
		len(changes) != 1 {
		t.Fatal(next)
	}

	acquireRealized(t, s, reportedPositionsXML("2026-11-03", "<OpenPositions/>"))

	empty := readPositions(t, s)
	if empty.Status != "available" || empty.Positions == nil || len(empty.Positions) != 0 {
		t.Fatal(empty)
	}

	acquireRealized(
		t,
		s,
		reportedPositionsXML(
			"2026-11-04",
			"<OpenPositions>"+holdingXML("2026-11-04", "not-a-number", "")+"</OpenPositions>",
		),
	)

	unavailable := readPositions(t, s)
	if unavailable.Status != "unavailable" || unavailable.Positions != nil ||
		*unavailable.Reason != "incomplete" {
		t.Fatal(unavailable)
	}

	acquireRealized(t, s, reportedPositionsXML("2026-11-05", "<Trades/>"))

	missing := readPositions(t, s)
	if missing.Status != "unavailable" || *missing.Reason != "not_reported" {
		t.Fatal(missing)
	}
	// Late acquisition of older evidence cannot restore a newer unavailable set.
	acquireRealized(
		t,
		s,
		reportedPositionsXML(
			"2026-11-02",
			"<OpenPositions>"+holdingXML("2026-11-02", "99", "")+"</OpenPositions>",
		),
	)

	if readPositions(t, s).Version != missing.Version {
		t.Fatal("fell back to old report")
	}

	if _, err = s.Positions(
		t.Context(),
		"unknown",
		application.SnapshotCondition{},
	); err != application.ErrNotFound {
		t.Fatal(err)
	}
}

func TestPositionsConflictsRollbackIdentityChangesAndRestart(t *testing.T) {
	s, profile := testStore(t)
	// Reuse the explicit synthetic account authority setup.
	_, err := s.db.Exec(
		`INSERT INTO connections(id,provider) VALUES('synthetic','ibkr-flex'); INSERT INTO artifacts(id,connection_id,stream,operation,format,media_type,retrieved,hash,content) VALUES('discovery','synthetic','confirmations','discover','fixture','text/plain','2026-10-20T00:00:00Z','fixture',X'00'); INSERT INTO accounts(id,singleton,name,currency,connection_id,provider,authority,kind,scope,value,artifact_id) VALUES('account',1,'Synthetic','','synthetic','ibkr-flex','ibkr','account','','U1','discovery');`,
	)
	if err != nil {
		t.Fatal(err)
	}

	acquireRealized(
		t,
		s,
		reportedPositionsXML(
			"2026-11-01",
			"<OpenPositions>"+holdingXML("2026-11-01", "10", "1.0000")+"</OpenPositions>",
		),
	)
	first := readPositions(t, s)
	conflict := strings.ReplaceAll(
		holdingXML("2026-11-01", "10", "1.0000"),
		`costBasisMoney="1.0000"`,
		`costBasisMoney="2"`,
	)
	acquireRealized(t, s, reportedPositionsXML("2026-11-01", "<OpenPositions>"+conflict+"</OpenPositions>"))

	if v := readPositions(t, s); v.Status != "unavailable" || *v.Reason != "conflict" {
		t.Fatal(v)
	}

	acquireRealized(
		t,
		s,
		reportedPositionsXML(
			"2026-11-02",
			"<OpenPositions>"+holdingXML("2026-11-02", "5", "2")+"</OpenPositions>",
		),
	)
	before := readPositions(t, s)

	changes, remove := s.SubscribePublications()
	defer remove()

	id := retainRealizedReport(t, s, "synthetic", reportedPositionsXML("2026-11-03", "<OpenPositions/>"))

	_, err = s.db.Exec(
		`CREATE TRIGGER reject_positions BEFORE UPDATE ON positions_current BEGIN SELECT RAISE(ABORT,'injected'); END;`,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = s.Process(t.Context(), id, ibkrflex.Version); err == nil {
		t.Fatal("position publication failure committed")
	}

	if readPositions(t, s).Version != before.Version || len(changes) != 0 {
		t.Fatal("rollback leaked")
	}

	_, err = s.db.Exec(`DROP TRIGGER reject_positions`)
	if err != nil {
		t.Fatal(err)
	}
	// An instrument combination must advance the positions version and its IDs.
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	q := s.queries.WithTx(tx)

	root, err := q.Instrument(t.Context(), first.Positions[0].InstrumentID)
	if err != nil {
		t.Fatal(err)
	}

	var d instrumentData
	if err = json.Unmarshal(root.Reported, &d); err != nil {
		t.Fatal(err)
	}

	original, err := d.instrument()
	if err != nil {
		t.Fatal(err)
	}

	f := original.Fields()
	f.Reference, _ = source.NewReference("ibkr", "contract-id", "", "new")

	instrument, err := source.NewInstrument(f)
	if err != nil {
		t.Fatal(err)
	}

	target, _, err := acceptInstrument(t.Context(), q, "combined", "new", instrument, "")
	if err != nil {
		t.Fatal(err)
	}

	if err = aliasResource(t.Context(), q, "instruments", root.ID, target); err != nil {
		t.Fatal(err)
	}

	if err = s.commit(t.Context(), tx); err != nil {
		t.Fatal(err)
	}

	after := readPositions(t, s)
	if after.Version == before.Version || after.Positions[0].InstrumentID != target {
		t.Fatal(after)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(t.Context(), testProfile(profile), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	if readPositions(t, reopened).Version != after.Version {
		t.Fatal("restart changed positions version")
	}
}

func TestPositionsHonorAuthorityAndTimestampPrecision(t *testing.T) {
	s := coverageStore(t)
	account, _ := source.NewReference("ibkr", "account", "", "U1")
	ref, _ := source.NewReference("broker", "security", "", "independent")
	i, _ := source.NewInstrument(source.InstrumentInput{Reference: ref, Kind: source.Equity})
	q, _ := financial.NewQuantity("1.0000")
	p, _ := source.NewPosition(i, q, financial.Money{})
	date, _ := financial.ParseDate("2026-11-04")
	at, _ := financial.ParseTimestamp("2026-11-04T12:00:00.1200Z")
	f := source.PositionSetInput{
		Location:  "set",
		Account:   account,
		Date:      date,
		At:        at,
		Complete:  true,
		Positions: []source.Position{p},
	}
	admit := func(id string, input source.PositionSetInput) {
		coverageClaims(
			t,
			s,
			id,
			claimNormalizer(
				func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
					o, e := source.NewPositionSetObservation(input)
					if e != nil {
						return provider.Result{}, e
					}

					if e = c.PutPositionSet(ctx, o); e != nil {
						return provider.Result{}, e
					}

					return provider.NewResult(provider.Counts{Rows: 2, PositionSets: 1, Instruments: 1})
				},
			),
		)
	}
	admit("generic", f)

	before := readPositions(t, s)
	if before.AsOf.At == nil || *before.AsOf.At != "2026-11-04T12:00:00.1200Z" {
		t.Fatal(before)
	}

	f.Account, _ = source.NewReference("ibkr", "account", "", "OTHER")
	f.Date, _ = financial.ParseDate("2026-11-05")
	admit("unauthorized", f)

	if readPositions(t, s).Version != before.Version {
		t.Fatal("unauthorized set published")
	}
}

func TestPositionsWithholdDisputedInstrumentBindings(t *testing.T) {
	for _, change := range []string{"reported conflict", "reference conflict", "incompatible family"} {
		t.Run(change, func(t *testing.T) {
			s := coverageStore(t)
			coverageReport(t, s, "baseline", equityRow("U1", "holding", "100"))
			acquireRealized(
				t,
				s,
				reportedPositionsXML(
					"2026-11-01",
					"<OpenPositions>"+holdingXML("2026-11-01", "10", "")+"</OpenPositions>",
				),
			)

			before := readPositions(t, s)
			if before.Status != "available" {
				t.Fatal(before)
			}

			changes, remove := s.SubscribePublications()
			defer remove()

			if change == "reported conflict" {
				row := strings.ReplaceAll(syntheticOption, `conid="200"`, `conid="100"`)
				row = strings.ReplaceAll(row, `underlyingConid="100"`, `underlyingConid="101"`)
				coverageReport(t, s, "incompatible-activity", row)
			} else {
				tx, err := s.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}

				statement := `UPDATE instruments SET reference_status='conflict' WHERE id=?`
				if change == "incompatible family" {
					statement = `UPDATE instruments SET kind='option' WHERE id=?`
				}

				if _, err = tx.Exec(statement, before.Positions[0].InstrumentID); err != nil {
					t.Fatal(err)
				}

				if err = s.commit(t.Context(), tx); err != nil {
					t.Fatal(err)
				}
			}

			after := readPositions(t, s)
			if after.Status != "unavailable" || after.Reason == nil || *after.Reason != "identity_unresolved" ||
				after.Version == before.Version ||
				len(changes) != 1 {
				t.Fatal("disputed instrument binding remained available", after, len(changes))
			}
		})
	}
}

func TestPositionRevisionCycleCannotHideNewerTargetAndCanBeReplaced(t *testing.T) {
	s := coverageStore(t)
	account, _ := source.NewReference("ibkr", "account", "", "U1")
	ref := func(id string) source.Reference {
		r, _ := source.NewReference("broker", "snapshot", "U1", id)
		return r
	}
	admit := func(id, date string, supersedes ...string) {
		t.Helper()

		d, _ := financial.ParseDate(date)

		input := source.PositionSetInput{
			Location:  "set",
			Account:   account,
			Reference: ref(id),
			Date:      d,
			Complete:  true,
		}
		for _, id := range supersedes {
			input.Supersedes = append(input.Supersedes, ref(id))
		}

		coverageClaims(
			t,
			s,
			id,
			claimNormalizer(
				func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
					o, err := source.NewPositionSetObservation(input)
					if err != nil {
						return provider.Result{}, err
					}

					if err = c.PutPositionSet(ctx, o); err != nil {
						return provider.Result{}, err
					}

					return provider.NewResult(provider.Counts{Rows: 1, PositionSets: 1})
				},
			),
		)
	}
	admit("a", "2026-11-01", "b", "newer")
	admit("b", "2026-11-01", "a")
	admit("newer", "2026-11-04")
	admit("independent", "2026-11-02")

	if v := readPositions(
		t,
		s,
	); v.Status != "unavailable" || v.Reason == nil || *v.Reason != "conflict" ||
		v.AsOf.Date != "2026-11-04" {
		t.Fatal("revision filtering hid the newer target", v)
	}

	admit("replacement", "2026-11-01", "a", "b", "newer", "independent")

	if v := readPositions(t, s); v.Status != "available" || v.AsOf.Date != "2026-11-01" {
		t.Fatal("authoritative replacement did not retire cycle", v)
	}
}

func TestPositionRevisionAuthorityCanCorrectEffectiveDateAndRejectCycles(t *testing.T) {
	s := coverageStore(t)

	account, _ := source.NewReference("ibkr", "account", "", "U1")
	refs := map[string]source.Reference{}
	for _, id := range []string{"future", "middle", "correction", "cycle-a", "cycle-b", "later"} {
		refs[id], _ = source.NewReference("broker", "snapshot", "U1", id)
	}

	admit := func(id, date string, supersedes ...string) {
		t.Helper()

		d, _ := financial.ParseDate(date)

		f := source.PositionSetInput{
			Location:  "set",
			Account:   account,
			Reference: refs[id],
			Date:      d,
			Complete:  true,
		}
		for _, ref := range supersedes {
			f.Supersedes = append(f.Supersedes, refs[ref])
		}

		coverageClaims(
			t,
			s,
			id,
			claimNormalizer(
				func(ctx context.Context, _ provider.Artifact, c provider.Claims) (provider.Result, error) {
					o, e := source.NewPositionSetObservation(f)
					if e != nil {
						return provider.Result{}, e
					}

					if e = c.PutPositionSet(ctx, o); e != nil {
						return provider.Result{}, e
					}

					return provider.NewResult(provider.Counts{Rows: 1, PositionSets: 1})
				},
			),
		)
	}
	admit("future", "2026-11-03")
	admit("middle", "2026-11-02")
	admit("correction", "2026-11-01", "future")

	if v := readPositions(t, s); v.Status != "available" || v.AsOf.Date != "2026-11-02" {
		t.Fatal("lost a current set when correcting another effective date", v)
	}

	admit("cycle-a", "2026-11-04", "cycle-b")
	admit("cycle-b", "2026-11-04", "cycle-a")

	if v := readPositions(t, s); v.Status != "unavailable" || v.Reason == nil || *v.Reason != "conflict" {
		t.Fatal("cyclic revision authority published", v)
	}

	admit("later", "2026-11-05")

	if v := readPositions(t, s); v.Status != "available" || v.AsOf.Date != "2026-11-05" {
		t.Fatal("obsolete disagreement blocked a new complete set", v)
	}
}

func TestPositionsInterpretationUpgradeUsesRetainedEvidenceWithoutRefetch(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "existing-position-instrument", equityRow("U1", "fill", "100"))

	executions, err := s.queries.Executions(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	retainDecision(t, s, "activity", executions[0].ID, "price", "99.0000", "USD", "set")

	body := reportedPositionsXML(
		"2026-11-01",
		"<OpenPositions>"+holdingXML("2026-11-01", "10.0000", "0.0000")+"</OpenPositions>",
	)
	// Normalizing with the legacy interpretation retains the original report but no positions.
	artifact, err := provider.NewArtifact(ibkrflex.ActivityFormat, "application/xml", time.Now(), []byte(body))
	if err != nil {
		t.Fatal(err)
	}

	key := host.WorkKey{Connection: "synthetic", Stream: ibkrflex.Activity, Operation: "acquire"}

	cp, err := s.Checkpoint(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}

	step, _ := provider.NewStep(provider.State{}, artifact, time.Now().Add(time.Hour))

	id, err := s.Accept(t.Context(), key, cp.Revision, step, nil, provider.Progress{})
	if err != nil {
		t.Fatal(err)
	}

	old := claimNormalizer(func(context.Context, provider.Artifact, provider.Claims) (provider.Result, error) {
		return provider.NewResult(provider.Counts{Rows: 1, Ignored: 1})
	})
	if _, err = s.Normalize(t.Context(), id, "old-position-interpretation", old); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(t.Context(), id, "old-position-interpretation"); err != nil {
		t.Fatal(err)
	}

	before := readPositions(t, s)
	pending, err := s.PendingArtifacts(
		t.Context(),
		time.Now(),
		map[string]string{"ibkr-flex": ibkrflex.Version},
	)
	if err != nil || len(pending) != 1 || pending[0].ID != id {
		t.Fatal(pending, err)
	}

	reg, _ := ibkrflex.Registration(ibkrflex.Options{})
	if _, err = s.Normalize(t.Context(), id, ibkrflex.Version, reg.Normalizer()); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(t.Context(), id, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	after := readPositions(t, s)
	if after.Status != "available" || after.Version == before.Version ||
		realizedActivity(t, s, executions[0].ID).Activity.Price.Amount != "99.0000" {
		t.Fatal(after)
	}

	if err = s.Process(t.Context(), id, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	if readPositions(t, s).Version != after.Version {
		t.Fatal("repeat interpretation republished positions")
	}
}
