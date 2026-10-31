// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

const testRow = `<TradeConfirm accountId="%s" assetCategory="STK" subCategory="COMMON" symbol="SYN" description="Synthetic equity" conid="100" isin="US0000000001" listingExchange="NASDAQ" tradeID="T%s" execID="%s" orderID="O1" transactionType="ExchTrade" levelOfDetail="EXECUTION" buySell="BUY" quantity="0.000000000123" price="%s" currency="USD" dateTime="2026-10-20;09:30:00.120 EDT" tradeDate="2026-10-20" settleDate="2026-10-21" amount="0.000000049323000000123" proceeds="-0.000000049323000000123" netCash="-0.000000049324000000123" commission="-0.000000000001" commissionCurrency="USD" code="O"/>`

func equityRow(account, event, price string) string {
	return fmt.Sprintf(testRow, account, event, event, price)
}

func workflowReport(rows string) string {
	return `<FlexQueryResponse type="TCF"><FlexStatements><FlexStatement accountId="U1" fromDate="2026-10-20" toDate="2026-10-21"><TradeConfirms>` + rows + `</TradeConfirms></FlexStatement><FlexStatement accountId="U2" fromDate="2026-10-20" toDate="2026-10-21" acctAlias="Other" baseCurrency="GBP"><TradeConfirms>` + equityRow(
		"U2",
		"excluded",
		"1",
	) + `</TradeConfirms></FlexStatement></FlexStatements></FlexQueryResponse>`
}

func testStore(t *testing.T) (*Store, string) {
	t.Helper()

	dir := t.TempDir()
	//nolint:gosec // A private test directory needs owner traversal permission.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "wake.db")

	s, err := Open(
		context.Background(),
		testProfile(path),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = s.Close() })

	return s, path
}

func testEnvelope(t *testing.T) *credential.Envelope {
	t.Helper()

	key, err := credential.NewMasterKey(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer key.Clear()

	e, err := credential.NewEnvelope(key)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(e.Clear)

	return e
}

func TestCompleteAccountWorkflowAndFinancialReplay(t *testing.T) {
	ctx := context.Background()

	var diagnostics bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&diagnostics, &slog.HandlerOptions{Level: slog.LevelDebug}))
	defer func() {
		text := diagnostics.String()
		for _, private := range []string{"synthetic-private-token", "replacement-token", "not-numeric", "Repaired configuration", "401.000000001", "0.000000000123", "US0000000001", "REFERENCE EQUITY", "My account"} {
			if strings.Contains(text, private) {
				t.Error("private workflow input leaked", private)
			}
		}

		decoder := json.NewDecoder(strings.NewReader(text))
		seen := map[string]bool{}
		normalization := map[string]string{}
		for decoder.More() {
			var entry map[string]any
			if err := decoder.Decode(&entry); err != nil {
				t.Fatal(err)
			}

			event, _ := entry["event"].(string)
			seen[event] = true
			if event == "normalize_finished" && entry["outcome"] == "completed" {
				normalization[entry["artifact_id"].(string)] = entry["operation_id"].(string)
			}

			if event == "process_finished" &&
				normalization[entry["artifact_id"].(string)] != entry["operation_id"] {
				t.Error("processing lost normalization correlation")
			}
		}

		for _, event := range []string{"discover_finished", "acquire_finished", "normalize_finished", "process_finished", "reference_finished", "account_confirm_finished", "processing_deferred", "connection_access_repair_finished"} {
			if !seen[event] {
				t.Error("missing workflow diagnostic", event)
			}
		}
	}()

	s, path := testStore(t)
	envelope := testEnvelope(t)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	report := workflowReport(
		equityRow("U1", "fill-1", "401.000000001") + equityRow("U1", "fill-2", "401.000000001"),
	)

	var (
		mu       sync.Mutex
		requests []url.Values
	)

	figiCalls := 0
	validToken := "synthetic-private-token"

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if r.URL.Path == "/mapping" {
			figiCalls++

			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(
				w,
				`[{"data":[{"figi":"BBG000000001","shareClassFIGI":"BBG000000002","name":"REFERENCE EQUITY","marketSector":"Equity","ticker":"SYN"}]}]`,
			)

			return
		}

		requests = append(requests, r.URL.Query())
		if r.URL.Query().Get("t") != validToken {
			_, _ = fmt.Fprint(
				w,
				`<FlexStatementResponse><Status>Fail</Status><ErrorCode>1012</ErrorCode></FlexStatementResponse>`,
			)

			return
		}

		if strings.HasSuffix(r.URL.Path, "SendRequest") {
			_, _ = fmt.Fprint(
				w,
				`<FlexStatementResponse><Status>Success</Status><ReferenceCode>generation-1</ReferenceCode></FlexStatementResponse>`,
			)
		} else {
			_, _ = fmt.Fprint(w, report)
		}
	}))
	defer server.Close()

	registration, err := ibkrflex.Registration(
		ibkrflex.Options{HTTPClient: server.Client(), BaseURL: server.URL},
	)
	if err != nil {
		t.Fatal(err)
	}

	h, err := host.New(ctx, s, envelope, logger, func() time.Time { return now }, registration)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	refs, err := reference.New(
		s,
		envelope,
		server.Client(),
		server.URL+"/mapping",
		func() time.Time { return now }, logger,
	)
	if err != nil {
		t.Fatal(err)
	}

	workflow := application.New(s, h, refs, "ibkr-flex", ibkrflex.Confirmations, ibkrflex.Version, nil, logger)

	token := credential.NewSecret([]byte("synthetic-private-token"))
	defer token.Clear()

	if _, err = workflow.CreateConnection(ctx, "Test", "not-numeric", token); err == nil {
		t.Fatal("accepted invalid provider settings")
	}

	id, err := workflow.CreateConnection(ctx, "Test", "123", token)
	if err != nil {
		t.Fatal(err)
	}

	run := func() {
		t.Helper()

		if err = workflow.RunOnce(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	run()

	now = now.Add(10 * time.Second)

	run()

	view, err := workflow.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if view.Account.ID() != "" || len(view.Executions) != 0 || len(view.Connections) != 1 ||
		len(view.Connections[0].Candidates) != 2 {
		t.Fatalf("discovery granted financial authority: %+v", view)
	}

	candidate := view.Connections[0].Candidates[0]
	if candidate.Candidate.Name() != "" || candidate.Candidate.Currency().String() == "USD" {
		t.Fatal("inferred missing account profile")
	}

	before := len(requests)
	now = now.Add(24 * time.Hour)

	run()

	if len(requests) != before {
		t.Fatal("discovery became recurring")
	}

	details, err := application.NewAccountDetails("My account", "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = application.NewAccountDetails("", ""); err == nil {
		t.Fatal("accepted missing display name")
	}
	// Failed confirmation must leave neither an account nor acquisition intent.
	if _, err = s.db.Exec(
		`CREATE TRIGGER reject_account BEFORE INSERT ON scheduled WHEN NEW.operation='acquire' BEGIN SELECT RAISE(ABORT,'injected'); END`,
	); err != nil {
		t.Fatal(err)
	}

	if _, err = workflow.Confirm(
		ctx,
		id,
		candidate.Artifact,
		candidate.Candidate.Reference(),
		details,
	); err == nil {
		t.Fatal("confirmation storage fault ignored")
	}

	if strings.Contains(diagnostics.String(), `"event":"account_confirm_finished","outcome":"completed"`) {
		t.Fatal("logged rolled-back confirmation as completed")
	}

	view, err = s.Dashboard(ctx)
	if err != nil || view.Account.ID() != "" {
		t.Fatal("partial confirmation", err)
	}

	if _, err = s.db.Exec(`DROP TRIGGER reject_account`); err != nil {
		t.Fatal(err)
	}

	account, err := workflow.Confirm(ctx, id, candidate.Artifact, candidate.Candidate.Reference(), details)
	if err != nil {
		t.Fatal(err)
	}

	repeated, err := workflow.Confirm(ctx, id, candidate.Artifact, candidate.Candidate.Reference(), details)
	if err != nil || account.ID() != repeated.ID() {
		t.Fatal("repeat confirmation", err)
	}

	other := view.Connections[0].Candidates[1]
	if _, err = workflow.Confirm(
		ctx,
		id,
		other.Artifact,
		other.Candidate.Reference(),
		details,
	); !errors.Is(
		err,
		application.ErrAccountLimit,
	) {
		t.Fatal("activated another account", err)
	}

	run()

	now = now.Add(10 * time.Second)

	run()

	if len(requests) != 4 || requests[0].Get("p") != "365" || requests[2].Get("p") != "365" {
		t.Fatal("independent initial requests", requests)
	}

	view, err = workflow.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(view.Executions) != 2 || len(view.Instruments) != 1 || view.Instruments[0].Status() != "resolved" ||
		figiCalls != 1 {
		t.Fatalf("acceptance/reference: %+v, calls %d", view, figiCalls)
	}

	fields := view.Executions[0].Terms().Fields()
	if fields.Quantity.Value() != "0.000000000123" || fields.Price.Amount() != "401.000000001" ||
		fields.ExecutedAt.String() != "2026-10-20T13:30:00.120Z" ||
		fields.PositionEffect != "opening" ||
		fields.Charges[0].Money().Amount() != "-0.000000000001" ||
		fields.Proceeds.Amount() != "-0.000000049323000000123" {
		t.Fatal("lost exact source terms", fields)
	}

	if account.Details().Currency() != "" {
		t.Fatal("inferred account currency")
	}

	processing := view.Connections[0].Processing[0]
	if processing.RequestedFrom == "" || processing.ReportedFrom != "2026-10-20" ||
		processing.RequestedFrom == processing.ReportedFrom {
		t.Fatal("requested/returned coverage conflated", processing)
	}

	acceptedID := view.Executions[1].ID()
	instrumentID := view.Instruments[0].ID()
	// A late observation adds a new fill; a changed term preserves the accepted fill.
	mu.Lock()
	report = workflowReport(
		equityRow(
			"U1",
			"fill-1",
			"999",
		) + equityRow(
			"U1",
			"fill-2",
			"401.000000001",
		) + equityRow(
			"U1",
			"late-fill",
			"401.000000001",
		),
	)
	mu.Unlock()

	now = now.Add(5 * time.Minute)

	run()

	now = now.Add(10 * time.Second)

	run()

	if requests[4].Get("p") != "2" {
		t.Fatal("rolling window", requests[4])
	}

	view, err = s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 3 || view.Instruments[0].ID() != instrumentID {
		t.Fatal("late/overlapping identity", err, view)
	}

	detail, err := s.ExecutionDetail(ctx, acceptedID)
	if err != nil || !detail.Execution.State().NeedsReview() ||
		detail.Execution.Terms().Fields().Price.Amount() != "401.000000001" ||
		len(detail.Observations) != 2 {
		t.Fatal("conflict overwrote accepted values", detail, err)
	}

	now = now.Add(5 * time.Minute)

	run()

	now = now.Add(10 * time.Second)

	run()

	view, err = s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 3 || figiCalls != 1 {
		t.Fatal("overlap duplicated financial state", err)
	}

	// Projection writes and completion roll back together under a storage fault.
	mu.Lock()
	report = workflowReport(
		strings.Replace(
			equityRow("U1", "storage-retry", "401.000000001"),
			`tradeID="Tstorage-retry"`,
			`tradeID="Tfill-1"`,
			1,
		),
	)
	mu.Unlock()

	if _, err = s.db.Exec(
		`CREATE TRIGGER reject_projection BEFORE INSERT ON execution_observations BEGIN SELECT RAISE(ABORT,'injected'); END`,
	); err != nil {
		t.Fatal(err)
	}

	now = now.Add(5 * time.Minute)

	run()

	now = now.Add(10 * time.Second)

	run()

	view, err = s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 3 || view.Connections[0].Processing[0].Complete {
		t.Fatal("partial projection or acknowledged storage fault", err)
	}

	if _, err = s.db.Exec(`DROP TRIGGER reject_projection`); err != nil {
		t.Fatal(err)
	}

	artifactID := view.Connections[0].Processing[0].Artifact
	if err = s.Process(ctx, artifactID, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(ctx, artifactID, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	view, err = s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 4 {
		t.Fatal("projection retry duplicated work", err)
	}
	// Persisted credential failures pause work until explicit repair, preserving identity.
	mu.Lock()
	validToken = "replacement-token"
	mu.Unlock()

	now = now.Add(5 * time.Minute)

	run()

	callsBeforeRepair := len(requests)
	now = now.Add(time.Minute)

	run()

	if len(requests) != callsBeforeRepair {
		t.Fatal("invalid credentials did not pause work")
	}

	replacement := credential.NewSecret([]byte("replacement-token"))
	defer replacement.Clear()

	if err = workflow.ReplaceToken(ctx, id, replacement); err != nil {
		t.Fatal(err)
	}

	run()

	now = now.Add(10 * time.Second)

	run()

	view, err = s.Dashboard(ctx)
	if err != nil || len(view.Executions) != 4 || view.Account.ID() != account.ID() {
		t.Fatal("credential repair changed financial identity", err)
	}

	if err = workflow.UpdateConnection(ctx, id, "Repaired configuration", "456"); err != nil {
		t.Fatal(err)
	}

	now = now.Add(6 * time.Second)

	run()

	now = now.Add(10 * time.Second)

	run()

	if requests[len(requests)-2].Get("q") != "456" || requests[len(requests)-2].Get("p") != "365" {
		t.Fatal("query repair did not restart bounded protocol")
	}

	h.Close()

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // Read only this test synthetic SQLite file.
	bytesOnDisk, err := os.ReadFile(path)
	if err != nil || bytes.Contains(bytesOnDisk, []byte("synthetic-private-token")) {
		t.Fatal("plaintext token at rest", err)
	}

	reopened, err := Open(
		ctx,
		testProfile(path),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = reopened.Close() }()

	view, err = reopened.Dashboard(ctx)
	if err != nil || view.Account.ID() != account.ID() || len(view.Executions) != 4 {
		t.Fatal("restart lost projection/authority", err)
	}
}

func testProfile(warehouse string) Profile {
	return Profile{
		WarehousePath:   warehouse,
		ConnectionsPath: filepath.Join(filepath.Dir(warehouse), "connections.db"),
		OperationalPath: filepath.Join(filepath.Dir(warehouse), "operational.db"),
	}
}

func TestIdleWorkflowDoesNotLog(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)

	var output bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))

	h, err := host.New(ctx, s, nil, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	refs, err := reference.New(s, nil, nil, "", nil, logger)
	if err != nil {
		t.Fatal(err)
	}

	workflow := application.New(s, h, refs, "", "", "", nil, logger)
	for range 100 {
		if err := workflow.RunOnce(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	if output.Len() != 0 {
		t.Fatal("idle polling produced logs", output.String())
	}
}
