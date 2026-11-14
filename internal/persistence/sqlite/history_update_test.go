// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/transport/financialapi"
	"go.trippwill.dev/wake/source"
)

func TestHistoryUpdateResumesAndPublishesRetainedClaims(t *testing.T) {
	const futureRule application.FinancialRule = "retained-claims-and-decisions/2"

	ctx := t.Context()
	now := time.Now().UTC()
	store := newStoreWithSyntheticAccount(t)
	profile := testProfile(store.owners[0].Name())

	var rows strings.Builder
	rows.WriteString(syntheticOption)

	for i := range historyBatchSize + 1 {
		rows.WriteString(equityRow("U1", fmt.Sprintf("history-%d", i), "2.34000000000000100"))
	}

	processConfirmationReport(t, store, "history", rows.String())

	view, err := store.Dashboard(ctx)
	if err != nil || len(view.Executions) != historyBatchSize+2 {
		t.Fatal(len(view.Executions), err)
	}

	var dateOnly, timestamped, instrument string
	for _, item := range view.Executions {
		if item.Terms().Fields().At.IsZero() {
			dateOnly = item.ID()
			instrument = item.Instrument()
		} else if timestamped == "" {
			timestamped = item.ID()
		}
	}

	decision := retainDecision(t, store, "activity", dateOnly, "price", "3.12000000000000100", "USD", "set")
	retainDecision(t, store, "instrument", instrument, "name", "Reviewed option name", "", "set")

	// The fixture represents a prior rule that rounded one accepted price. Its
	// original claims and decision records were retained at full precision.
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(
		ctx,
		`UPDATE executions SET accepted=CAST(json_set(accepted,'$.Price','2.34') AS BLOB) WHERE id=?`,
		timestamped,
	); err != nil {
		t.Fatal(err)
	}

	if err = store.commit(ctx, tx); err != nil {
		t.Fatal(err)
	}

	before, err := store.Resource(ctx, "activity", timestamped)
	if err != nil || before.Activity.Trade.Price.Amount != "2.34" {
		t.Fatal(before, err)
	}

	originalExplanation, err := store.Explanation(ctx, "activity", timestamped, before.Revision)
	if err != nil || originalExplanation.CalculationRule != string(application.RetainedClaimsRule) {
		t.Fatal(originalExplanation, err)
	}

	baseline, err := store.Snapshot(ctx, application.AnySnapshotVersion())
	if err != nil {
		t.Fatal(err)
	}

	preserved := func(s *Store) string {
		t.Helper()

		var value string
		if e := s.db.QueryRowContext(ctx, `SELECT json_array(
   (SELECT group_concat(hex(content)) FROM artifacts),
   (SELECT group_concat(id) FROM source_claims),
   (SELECT group_concat(id) FROM operator_decisions),
   (SELECT group_concat(artifact_id||version) FROM normalization_receipts),
   (SELECT group_concat(artifact_id||version) FROM processing_receipts))`).Scan(&value); e != nil {
			t.Fatal(e)
		}

		return value
	}
	immutable := preserved(store)
	reopen := func(previous *Store) *Store {
		t.Helper()

		if e := previous.Close(); e != nil {
			t.Fatal(e)
		}

		baseline := releaseBaseline()
		baseline.rule = futureRule
		baseline.notes = map[int64]string{2: "Preserve the full reported precision in the rebuilt fixture."}
		profile.UnattendedUpgrade = true

		s, e := openStore(ctx, profile, nil, baseline, upgradeOptions{})
		if e != nil {
			t.Fatal(e)
		}

		t.Cleanup(func() { _ = s.Close() })

		return s
	}

	store = reopen(store)
	if !store.HistoryUpdating() {
		t.Fatal("startup did not enqueue history update")
	}

	socketResponse := func(s *Store, path string) *httptest.ResponseRecorder {
		t.Helper()

		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("If-None-Match", `"`+baseline.Snapshot.Version+`"`)

		response := httptest.NewRecorder()
		financialapi.NewHandler(s, nil, nil).ServeHTTP(response, request)

		return response
	}
	checkGated := func(s *Store) {
		t.Helper()

		for _, path := range []string{
			"/v1/snapshot", "/v1/events", "/v1/accounts/account/positions",
			"/v1/activity/" + timestamped, "/v1/instruments/" + instrument,
		} {
			response := socketResponse(s, path)
			if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") == "" ||
				response.Header().Get("ETag") != "" || !strings.Contains(response.Body.String(), "history_updating") {
				t.Fatal(path, response.Code, response.Header(), response.Body.String())
			}
		}

		if response := socketResponse(s, "/v1/accounts/account"); response.Code != http.StatusOK {
			t.Fatal("account-only read", response.Code)
		}
	}
	checkGated(store)

	if err = store.CheckCorrection(ctx, decision); !errors.Is(err, application.ErrHistoryUpdating) {
		t.Fatal("correction validation during history update", err)
	}

	if err = store.SaveDecision(
		ctx,
		decision,
		"admin/synthetic-session",
		now,
	); !errors.Is(
		err,
		application.ErrHistoryUpdating,
	) {
		t.Fatal("correction write during history update", err)
	}

	if _, err = store.Confirm(
		ctx,
		host.Connection{},
		"",
		source.Reference{},
		application.AccountDetails{},
	); !errors.Is(
		err,
		application.ErrHistoryUpdating,
	) {
		t.Fatal("account confirmation during history update", err)
	}

	run := func(s *Store, at time.Time) {
		t.Helper()

		if e := application.New(s, nil, nil, nil, nil).RunOnce(ctx, at); e != nil {
			t.Fatal(e)
		}
	}
	state := func(s *Store) (int64, string) {
		t.Helper()

		update, e := s.queries.HistoryUpdate(ctx)
		if e != nil {
			t.Fatal(e)
		}

		return update.Completed, update.RetryAt
	}

	run(store, now)

	if count, _ := state(store); count != historyBatchSize {
		t.Fatal("unexpected first batch", count)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	if _, err = store.RunHistoryUpdate(cancelled, now); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled batch", err)
	}

	if count, retry := state(store); count != historyBatchSize || retry != "" {
		t.Fatal("cancellation advanced checkpoint or set failure", count, retry)
	}

	// A storage failure that prevents saving both the checkpoint and its retry
	// must reach the scheduler and leave the current batch eligible to retry.
	if _, err = store.db.ExecContext(
		ctx,
		`CREATE TRIGGER reject_history_checkpoint BEFORE UPDATE ON history_update
  BEGIN SELECT RAISE(ABORT,'injected checkpoint failure'); END`,
	); err != nil {
		t.Fatal(err)
	}

	workflow := application.New(store, nil, nil, nil, nil)
	if err = workflow.RunOnce(ctx, now); err == nil {
		t.Fatal("workflow swallowed failure to record the retry")
	}

	if count, retry := state(store); count != historyBatchSize || retry != "" {
		t.Fatal("failed checkpoint advanced progress or recorded a retry", count, retry)
	}

	if _, err = store.db.ExecContext(ctx, `DROP TRIGGER reject_history_checkpoint`); err != nil {
		t.Fatal(err)
	}

	if _, err = store.db.ExecContext(
		ctx,
		`CREATE TRIGGER reject_history_publish BEFORE UPDATE ON published_resources
  BEGIN SELECT RAISE(ABORT,'injected publication failure'); END`,
	); err != nil {
		t.Fatal(err)
	}

	run(store, now)

	if count, retry := state(
		store,
	); count != historyBatchSize ||
		retry != now.Add(time.Minute).Format(time.RFC3339Nano) {
		t.Fatal("publication failure advanced checkpoint or lost retry", count, retry)
	}

	var rebuilt int
	if err = store.db.QueryRowContext(ctx, `SELECT count(*) FROM executions WHERE calculation_rule=?`, futureRule).
		Scan(&rebuilt); err != nil ||
		rebuilt != historyBatchSize {
		t.Fatal("publication failure leaked derived writes", rebuilt, err)
	}

	if _, err = store.db.ExecContext(ctx, `DROP TRIGGER reject_history_publish`); err != nil {
		t.Fatal(err)
	}

	store = reopen(store)
	checkGated(store)
	run(store, now)

	if count, _ := state(store); count != historyBatchSize {
		t.Fatal("retry ran before deadline", count)
	}

	overview, err := store.Overview(ctx)
	if err != nil || overview.Status.HistoryUpdate.Completed != historyBatchSize ||
		overview.Status.HistoryUpdate.Rule != string(futureRule) ||
		!overview.Status.HistoryUpdate.RetryAt.Equal(now.Add(time.Minute)) {
		t.Fatal("overview lost durable progress and retry", overview.Status.HistoryUpdate, err)
	}

	if _, err = store.db.ExecContext(
		ctx,
		`INSERT INTO positions_dirty(account_id) VALUES('account')`,
	); err != nil {
		t.Fatal(err)
	}

	for range 8 {
		if !store.HistoryUpdating() {
			break
		}

		run(store, now.Add(time.Minute))
	}

	if store.HistoryUpdating() {
		t.Fatal("history update did not complete")
	}

	var dirty int
	if err = store.db.QueryRowContext(ctx, `SELECT count(*) FROM positions_dirty`).
		Scan(&dirty); err != nil ||
		dirty != 0 {
		t.Fatal("completed update left dirty positions", dirty, err)
	}

	after, err := store.Resource(ctx, "activity", timestamped)
	if err != nil || after.Revision <= before.Revision ||
		after.Activity.Trade.Price.Amount != "2.34000000000000100" ||
		after.Activity.At == nil ||
		*after.Activity.At != *before.Activity.At {
		t.Fatal("retained trade was not rebuilt", after, err)
	}

	explanation, err := store.Explanation(ctx, "activity", timestamped, after.Revision)
	if err != nil || explanation.CalculationRule != string(futureRule) {
		t.Fatal(explanation, err)
	}

	option, err := store.Resource(ctx, "activity", dateOnly)
	if err != nil || option.Activity.At != nil || option.Activity.Trade.Price.Amount != "3.12000000000000100" {
		t.Fatal(option, err)
	}

	root, err := store.Resource(ctx, "instruments", instrument)
	if err != nil || root.Instrument.Name == nil || *root.Instrument.Name != "Reviewed option name" {
		t.Fatal(root, err)
	}

	instrumentExplanation, err := store.Explanation(ctx, "instruments", instrument, root.Revision)
	if err != nil || instrumentExplanation.CalculationRule != string(futureRule) {
		t.Fatal(instrumentExplanation, err)
	}

	if preserved(store) != immutable {
		t.Fatal("retained inputs or receipts changed")
	}

	if err = store.SaveDecision(ctx, decision, "admin/synthetic-session", now); err != nil {
		t.Fatal(err)
	}

	final, err := store.Snapshot(ctx, application.AnySnapshotVersion())
	if err != nil || final.Snapshot.WarehouseID != baseline.Snapshot.WarehouseID ||
		final.Snapshot.Version == baseline.Snapshot.Version ||
		len(final.Snapshot.Resources) != len(baseline.Snapshot.Resources) {
		t.Fatal("publication lost identity or resources", err)
	}

	stream := httptest.NewServer(financialapi.NewHandler(store, nil, nil))
	defer stream.Close()

	streamCtx, stopStream := context.WithTimeout(ctx, 5*time.Second)
	defer stopStream()

	request, err := http.NewRequestWithContext(streamCtx, http.MethodGet, stream.URL+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}

	response, err := stream.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(response.Body)

	event, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}

	data, err := reader.ReadString('\n')

	if closeErr := response.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	if err != nil || response.StatusCode != http.StatusOK || event != "event: publication\n" ||
		!strings.Contains(data, final.Snapshot.Version) {
		t.Fatal("reconnect did not announce the completed publication", event, data, err)
	}

	store = reopen(store)
	run(store, now.Add(time.Minute))

	repeated, err := store.Snapshot(ctx, application.AnySnapshotVersion())
	if err != nil || store.HistoryUpdating() || repeated.Snapshot.Version != final.Snapshot.Version {
		t.Fatal("applied rule replayed", err)
	}
}

func TestHistoryUpdateRefusesIncompatibleVersions(t *testing.T) {
	for _, seed := range []string{
		"UPDATE history_update SET applied=applied+1",
		"UPDATE history_update SET target=applied+1, phase='activity'",
	} {
		t.Run(seed, func(t *testing.T) {
			profile, store := profileFixture(t)
			if _, err := store.db.ExecContext(t.Context(), seed); err != nil {
				t.Fatal(err)
			}

			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			if _, err := Open(t.Context(), profile, nil); !errors.Is(err, ErrHistoryVersion) {
				t.Fatal(err)
			}
		})
	}
}
