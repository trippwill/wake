// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/persistence/sqlite/sqlc"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
	"go.trippwill.dev/wake/source"
)

const testRow = `<TradeConfirm accountId="%s" assetCategory="STK" subCategory="COMMON" symbol="SYN" description="Synthetic equity" conid="100" isin="US0000000001" listingExchange="NASDAQ" tradeID="T%s" execID="%s" orderID="O1" transactionType="ExchTrade" levelOfDetail="EXECUTION" buySell="BUY" quantity="0.000000000123" price="%s" currency="USD" dateTime="2026-10-20;09:30:00.120 EDT" tradeDate="2026-10-20" settleDate="2026-10-21" amount="0.000000049323000000123" proceeds="-0.000000049323000000123" netCash="-0.000000049324000000123" commission="-0.000000000001" commissionCurrency="USD" code="O"/>`

func equityRow(account, event, price string) string {
	return fmt.Sprintf(testRow, account, event, event, price)
}

func confirmationReport(rows string) string {
	return `<FlexQueryResponse type="TCF"><FlexStatements><FlexStatement accountId="U1" fromDate="2026-10-20" toDate="2026-10-21"><TradeConfirms>` + rows + `</TradeConfirms></FlexStatement><FlexStatement accountId="U2" fromDate="2026-10-20" toDate="2026-10-21" acctAlias="Other" baseCurrency="GBP"><TradeConfirms>` + equityRow(
		"U2",
		"excluded",
		"1",
	) + `</TradeConfirms></FlexStatement></FlexStatements></FlexQueryResponse>`
}

func testStore(t testing.TB) (*Store, string) {
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

func testProfile(warehouse string) Profile {
	return Profile{
		WarehousePath:   warehouse,
		ConnectionsPath: filepath.Join(filepath.Dir(warehouse), "connections.db"),
	}
}

func confirmationSettings(query string) provider.Settings {
	settings, _ := provider.NewSettings(map[string]string{"query_id": query})
	return settings
}

// Synthetic reports use a disposable account, never operator data or credentials.
func newStoreWithSyntheticAccount(t testing.TB) *Store {
	t.Helper()
	s, _ := testStore(t)

	_, err := s.db.Exec(`INSERT INTO connections(id,provider) VALUES('synthetic','ibkr-flex');
INSERT INTO artifacts(id,connection_id,stream,operation,format,media_type,retrieved,hash,content) VALUES('discovery','synthetic','confirmations','discover','ibkr-confirmation-v1','application/xml','2026-10-20T00:00:00Z','synthetic',X'00');
INSERT INTO accounts(id,name,currency,connection_id,provider,authority,kind,scope,value,artifact_id) VALUES('account','Synthetic account','','synthetic','ibkr-flex','ibkr','account','','U1','discovery');`)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func processConfirmationReport(t *testing.T, s *Store, id, rows string) {
	t.Helper()
	normalizeConfirmationReport(t, s, id, rows)

	if err := s.Process(t.Context(), id, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}
}

func normalizeConfirmationReport(t *testing.T, s *Store, id, rows string) {
	t.Helper()

	ctx := context.Background()
	body := []byte(confirmationReport(rows))
	artifact, _ := provider.NewArtifact(ibkrflex.ConfirmationFormat, "application/xml", time.Now(), body)

	r, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	err = s.queries.PutArtifact(
		ctx,
		sqlc.PutArtifactParams{
			ID:           id,
			ConnectionID: "synthetic",
			Stream:       "confirmations",
			Operation:    "acquire",
			Format:       ibkrflex.ConfirmationFormat,
			MediaType:    "application/xml",
			Retrieved:    time.Now().UTC().Format(time.RFC3339Nano),
			Hash:         artifact.Hash(),
			Content:      body,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	recordTestReceipt(t, s, id)

	if _, err = s.Normalize(ctx, id, ibkrflex.Version, r.Normalizer()); err != nil {
		t.Fatal(err)
	}
}

const syntheticOption = `<TradeConfirm accountId="U1" assetCategory="OPT" symbol="SYN 261120C00100000" description="Synthetic option" conid="200" underlyingConid="100" underlyingSymbol="SYN" listingExchange="CBOE" tradeID="option-trade" execID="option-fill" orderID="order-1" transactionType="ExchTrade" levelOfDetail="EXECUTION" buySell="BUY" quantity="2" price="1.234567890123456789" currency="USD" tradeDate="2026-10-20" strike="100.00000000001" expiry="2026-11-20" putCall="C" multiplier="10"/>`

// processSyntheticClaims admits explicitly labelled provider-neutral claims against retained
// synthetic evidence, for facts the Confirmation format does not supply.
func processSyntheticClaims(t *testing.T, s *Store, id string, n provider.Normalizer) {
	t.Helper()
	processConfirmationReport(t, s, id, "")

	if _, err := s.Normalize(context.Background(), id, "synthetic-claims/1", n); err != nil {
		t.Fatal(err)
	}

	if err := s.Process(context.Background(), id, "synthetic-claims/1"); err != nil {
		t.Fatal(err)
	}
}

type claimNormalizer func(context.Context, provider.Artifact, provider.Claims) (provider.Result, error)

func (n claimNormalizer) Normalize(
	ctx context.Context,
	a provider.Artifact,
	c provider.Claims,
) (provider.Result, error) {
	return n(ctx, a, c)
}

const syntheticDebt = `<TradeConfirm accountId="U1" assetCategory="BOND" subCategory="Govt" symbol="SYN TREASURY" description="Synthetic Treasury missing terms" conid="debt-1" issuerCountryCode="US" transactionType="ExchTrade" levelOfDetail="EXECUTION" execID="debt-fill" buySell="BUY" quantity="10000" price="99.123456789" currency="USD" tradeDate="2026-10-20" accruedInt="-12.3456789" amount="9912.35" proceeds="-9912.35" netCash="-9925.6956789"/>`

const syntheticFX = `<TradeConfirm accountId="U1" assetCategory="CASH" symbol="EUR.USD" description="EUR.USD" transactionType="ExchTrade" levelOfDetail="EXECUTION" execID="fx-fill" tradeID="fx-trade" buySell="SELL" quantity="-100.123456789" price="1.234567890123" currency="USD" tradeDate="2026-10-20" proceeds="123.45" netCash="123.25" commission="-0.2" commissionCurrency="GBP"/>`

func observedTrade(t testing.TB, o source.ActivityObservation) source.Trade {
	t.Helper()

	v, err := o.Trade()
	if err != nil {
		t.Fatal(err)
	}

	return v
}
