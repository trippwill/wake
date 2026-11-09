// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package ibkrflex

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

const realizedRow = `<Trade accountId="U1" assetCategory="STK" conid="100" tradeID="trade" ibExecID="fill" transactionID="transaction" transactionType="ExchTrade" levelOfDetail="EXECUTION" quantity="-0.0000000012300" currency="USD" tradeDate="2026-10-20" reportDate="2026-10-21" cost="-99.00000000001" fifoPnlRealized="1.2300000000000000001"/>`

func TestActivityPreservesReportedStockSubtype(t *testing.T) {
	reg, err := Registration(Options{})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		subtype string
		want    source.InstrumentKind
	}{
		{"ETF", source.Fund}, {"COMMON", source.Equity}, {"", source.UnknownInstrument},
	} {
		row := strings.Replace(
			realizedRow,
			`assetCategory="STK"`,
			`assetCategory="STK" subCategory="`+tc.subtype+`"`,
			1,
		)

		artifact, _ := provider.NewArtifact(
			ActivityFormat,
			"application/xml",
			time.Now(),
			[]byte(activityReport(row)),
		)
		claims := &testClaims{}
		if _, err = reg.Normalizer().
			Normalize(t.Context(), artifact, claims); err != nil ||
			len(claims.realized) != 1 {
			t.Fatal(claims, err)
		}

		instrument, _ := claims.realized[0].Field(source.FieldInstrument)
		if instrument.Fields().Instrument.Kind() != tc.want {
			t.Errorf("subtype %q: got %s, want %s", tc.subtype, instrument.Fields().Instrument.Kind(), tc.want)
		}
	}
}

func activityReport(rows string) string {
	return `<FlexQueryResponse type="AF"><FlexStatements><FlexStatement accountId="U1" fromDate="2026-10-20" toDate="2026-10-21"><Trades>` + rows + `</Trades><FIFOPerformanceSummaryInBase><FIFOPerformanceSummaryUnderlying totalRealizedPnl="999999"/></FIFOPerformanceSummaryInBase><OpenPositions><OpenPosition costBasisMoney="invalid"/></OpenPositions></FlexStatement></FlexStatements></FlexQueryResponse>`
}

func TestActivityNormalizesOnlyExecutionRealizedResults(t *testing.T) {
	reg, err := Registration(Options{})
	if err != nil {
		t.Fatal(err)
	}

	rows := realizedRow + strings.ReplaceAll(
		realizedRow,
		`levelOfDetail="EXECUTION"`,
		`levelOfDetail="CLOSED_LOT"`,
	) +
		strings.ReplaceAll(
			realizedRow,
			`fifoPnlRealized="1.2300000000000000001"`,
			`fifoPnlRealized="0.0000"`,
		) +
		strings.ReplaceAll(
			realizedRow,
			`accountId="U1"`,
			`accountId="OTHER"`,
		) +
		strings.ReplaceAll(
			realizedRow,
			`fifoPnlRealized="1.2300000000000000001"`,
			`fifoPnlRealized="invalid"`,
		)
	artifact, _ := provider.NewArtifact(
		ActivityFormat,
		"application/xml",
		time.Now(),
		[]byte(activityReport(rows)),
	)

	claims := &testClaims{}
	result, err := reg.Normalizer().Normalize(t.Context(), artifact, claims)
	if err != nil || result.Counts().Activities != 2 || result.Ignored() != 1 || result.Rejected() != 2 ||
		len(claims.trades) != 0 {
		t.Fatal(result, err)
	}

	value := claims.realized[0]
	amount, _ := value.Field(source.FieldRealizedPnL)

	cost, _ := value.Field(source.FieldReportedCostBasis)
	quantity, _ := value.Field(source.FieldQuantity)
	zero, _ := claims.realized[1].Field(source.FieldRealizedPnL)
	if amount.Fields().Money.Amount() != "1.2300000000000000001" ||
		cost.Fields().Money.Amount() != "-99.00000000001" ||
		quantity.Fields().Quantity.Value() != "0.0000000012300" ||
		len(value.Fields().EventAliases) != 2 ||
		amount.Fields().ReportedOn.String() != "2026-10-21" ||
		zero.Fields().Money.Amount() != "0.0000" {
		t.Fatal(value)
	}

	claims.failure = provider.ErrConflict
	if _, err = reg.Normalizer().
		Normalize(t.Context(), artifact, claims); !errors.Is(
		err,
		provider.ErrConflict,
	) {
		t.Fatal("sink failure swallowed", err)
	}

	for _, row := range []string{
		strings.ReplaceAll(realizedRow, `assetCategory="STK"`, `assetCategory="CASH"`),
		strings.ReplaceAll(strings.ReplaceAll(realizedRow, `assetCategory="STK"`, `assetCategory="OPT"`), `transactionType="ExchTrade"`, `transactionType="BookTrade"`),
		strings.ReplaceAll(realizedRow, `fifoPnlRealized="1.2300000000000000001"`, `fifoPnlRealized=""`),
	} {
		input, _ := provider.NewArtifact(
			ActivityFormat,
			"application/xml",
			time.Now(),
			[]byte(activityReport(row)),
		)
		if _, err = reg.Normalizer().Normalize(t.Context(), input, &testClaims{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestActivityAcquisitionSharesTokenPacingButPreservesReportDates(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	ready, calls := false, 0

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++

		if r.URL.Query().Get("t") != "synthetic-token" {
			t.Error("wrong token")
		}

		switch {
		case strings.HasSuffix(r.URL.Path, "SendRequest"):
			if r.URL.Query().Get("q") != "456" || r.URL.Query().Get("p") != "365" {
				t.Error("wrong Activity query or initial coverage")
			}

			_, _ = fmt.Fprint(
				w,
				`<FlexStatementResponse><Status>Success</Status><ReferenceCode>generation</ReferenceCode></FlexStatementResponse>`,
			)
		case !ready:
			_, _ = fmt.Fprint(
				w,
				`<FlexStatementResponse><Status>Fail</Status><ErrorCode>1006</ErrorCode></FlexStatementResponse>`,
			)
		default:
			_, _ = fmt.Fprint(w, activityReport(realizedRow))
		}
	}))
	defer server.Close()

	reg, err := Registration(Options{BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}

	settings, _ := provider.NewSettings(map[string]string{"query_id": "123", "activity_query_id": "456"})
	if _, err = reg.PrepareSettings(settings); err != nil {
		t.Fatal(err)
	}

	configured, err := reg.ConfigureStreams(settings)
	if err != nil || len(configured) != 2 {
		t.Fatal("configured Activity missing")
	}

	onlyConfirmation, _ := provider.NewSettings(map[string]string{"query_id": "123"})

	configured, err = reg.ConfigureStreams(onlyConfirmation)
	if err != nil || len(configured) != 1 {
		t.Fatal("unconfigured Activity enabled")
	}

	request := provider.Request{Stream: Activity, Settings: settings, Now: now}
	access := &testAccess{token: []byte("synthetic-token"), now: now}

	step, err := reg.Acquirer().Acquire(t.Context(), request, access)
	if err != nil {
		t.Fatal(err)
	}

	request.State, request.Now = step.State(), step.NextEligibleAt()
	access.now = request.Now

	step, err = reg.Acquirer().Acquire(t.Context(), request, access)
	if err != nil || !step.Artifact().IsZero() || step.FailureCode() != "ibkr-1006" {
		t.Fatal("incomplete P&L acknowledged", step, err)
	}

	request.State, request.Now = step.State(), step.NextEligibleAt()
	access.now, ready = request.Now, true
	step, err = reg.Acquirer().Acquire(t.Context(), request, access)
	if err != nil || step.Artifact().Format() != ActivityFormat ||
		!step.PollCompleted() || !step.NextEligibleAt().Equal(request.Now) {
		t.Fatal(step, err)
	}

	policy, _ := step.Artifact().Equivalence()
	if policy != "exact-bytes/1" {
		t.Fatal("Activity dates ignored in report comparison")
	}

	confirmation := provider.Request{Stream: Confirmations, Settings: settings, Now: request.Now}
	before := calls
	if _, err = reg.Acquirer().
		Acquire(t.Context(), confirmation, access); err != nil || calls != before ||
		access.progress != "paced" {
		t.Fatal("streams bypassed shared budget", err)
	}

	state, err := readContinuation(step.State(), "456")
	if err != nil {
		t.Fatal(err)
	}

	state.choose(now.Add(24*time.Hour), Activity, 0)

	if state.Period != 7 {
		t.Fatal("Activity recent overlap", state.Period)
	}
}
