// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"testing"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/source"
)

func TestSourceReaderLinksPreserveReportReadingContext(t *testing.T) {
	key := application.SourceLocation{
		Artifact: "synthetic-report",
		Version:  "ibkr-flex/1",
		Location: "row[1]&x=y",
	}
	query := url.Values{
		"q": {"buy & sell"}, "sort": {"report"}, "dir": {"asc"}, "page": {"3"}, "size": {"25"},
		"selected": {"saved-entry"}, "receipt": {"selected-receipt"},
		"entry": {"old-entry"}, "version": {"old-version"}, "field": {"old-field"}, "next": {"//outside.invalid"},
	}
	for _, tc := range []struct {
		address, fragment string
		view              url.Values
	}{
		{sourceEntryURL(key, "price", query), "source-entry", url.Values{
			"version": {key.Version}, "entry": {key.Location}, "field": {"price"},
		}},
		{reportIndexURL(key.Artifact, query), "report-entries", url.Values{}},
	} {
		u, err := url.Parse(tc.address)
		if err != nil || u.Path != "/evidence/synthetic-report" || u.Fragment != tc.fragment {
			t.Fatal("incorrect report navigation address", u, err)
		}

		want := tc.view
		for _, name := range []string{"q", "sort", "dir", "page", "size", "selected", "receipt"} {
			want.Set(name, query.Get(name))
		}

		if got := u.Query().Encode(); got != want.Encode() {
			t.Fatal("report browsing state was lost or stale entry state escaped", got, want.Encode())
		}
	}

	info := application.EvidenceContext{ReportedFrom: "2026-10-20", ReportedTo: "2026-10-20"}
	if got := evidencePeriod(info); got != "20 Oct 2026" {
		t.Fatal("unexpected retained report period", got)
	}

	info.SelectedReceipt = &application.AcquisitionReceipt{ReportedTo: "2026-10-21"}
	if got := evidencePeriod(info); got != "Through 21 Oct 2026" {
		t.Fatal("selected receipt did not supply its own period", got)
	}
}

func TestSourceReaderTradeAttributionRequiresTheExactEntry(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	price, _ := financial.ParsePrice("1.2345678900", usd)
	quantity, _ := financial.ParseQuantity("2.00")
	date, _ := financial.ParseDate("2026-10-20")
	account, _ := source.NewReference("ibkr", "account", "", "synthetic")
	event, _ := source.NewReference("ibkr", "execution-id", "synthetic", "fill")
	instrument, _ := source.NewInstrument(
		source.InstrumentInput{Kind: source.Equity, Ticker: "SYN", Name: "Synthetic equity"},
	)

	trade, err := source.NewTrade(source.TradeInput{
		Location: "row[1]", Account: account, Event: event, EventDistinct: true,
		Date: date, Side: source.Buy, Instrument: instrument, Quantity: quantity, Price: price,
	})
	if err != nil {
		t.Fatal(err)
	}

	state, _ := calculation.NewActivityState(calculation.Accepted, 0, calculation.AutomaticSelection)

	execution, err := application.NewExecution(
		"synthetic-activity",
		"account",
		"security",
		"",
		"test-rule",
		state,
		trade,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, version, focus, fragment  string
		corrected, contributed, partial bool
	}{
		{"supporting row", "rule/1", "price", "activity-price", false, true, false},
		{"same locator in another version", "rule/2", "price", "activity-price", false, false, false},
		{"corrected record", "rule/1", "price", "activity-price", true, false, false},
		{"partial record without a price", "rule/1", "", "accepted-activity", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := trade.Observation().Fields()
			if tc.partial {
				input.Fields = slices.DeleteFunc(input.Fields, func(field source.ActivityField) bool {
					return field.Name() == source.FieldTradeKind || field.Name() == source.FieldPrice
				})
			}

			reported, err := source.NewActivityObservation(input)
			if err != nil {
				t.Fatal(err)
			}

			store := sourceEntryReadStore{detail: application.ExecutionDetail{
				Execution: execution,
				Context: application.ActivityContext{
					InstrumentTicker: "SYN",
					PriceCorrected:   tc.corrected,
					PriceSources: []application.SourceLocation{
						{Artifact: "report", Version: "rule/1", Location: "row[1]"},
					},
				},
			}}
			workflow := application.New(store, nil, nil, nil, nil, nil)
			entry := sourceEvidence(t.Context(), workflow, host.Retained{}, application.SourceObservation{
				Observation: application.Observation{
					Artifact:   "report",
					ActivityID: execution.ID(),
					Version:    tc.version,
					Location:   "row[1]",
					Reported:   reported,
				},
			}, "price")

			if entry.RecordLabel != "Buy SYN" || entry.RecordURL != "/executions/synthetic-activity#"+tc.fragment ||
				entry.Focus != tc.focus || entry.PriceCorrected != tc.corrected || entry.PriceContributed != tc.contributed {
				t.Fatalf("unexpected related price or provenance: %+v", entry)
			}

			if tc.partial {
				if entry.Price != "" || !slices.ContainsFunc(entry.Facts, func(fact sourceFact) bool {
					return fact.Key == source.FieldQuantity && fact.Value == "2.00 shares" &&
						fact.Accepted == fact.Value
				}) {
					t.Fatal("a partial fill should pair quantity by its distinct execution ID", entry)
				}
			} else if entry.ReportedPrice != "1.2345678900 USD" || entry.Price != entry.ReportedPrice {
				t.Fatal("retained or accepted price lost exact decimal text", entry)
			}
		})
	}
}

type sourceMovementReadStore struct {
	workspaceStore

	updating bool
	activity *application.PublishedActivity
}

func (s sourceMovementReadStore) HistoryUpdating() bool { return s.updating }

func (s sourceMovementReadStore) Resource(
	context.Context,
	string,
	string,
) (application.CanonicalResource, error) {
	if s.activity == nil {
		return application.CanonicalResource{}, application.ErrNotFound
	}

	return application.CanonicalResource{Activity: s.activity}, nil
}

func TestSourceReaderCashMovementPreservesEvidenceAcrossRecordAvailability(t *testing.T) {
	usd, _ := financial.ParseCurrency("USD")
	cash, _ := financial.ParseMoney("7.123456700", usd)
	account, _ := source.NewReference("ibkr", "account", "", "synthetic")
	event, _ := source.NewReference("ibkr", "transaction-id", "synthetic", "dividend-1")
	amount, _ := source.NewActivityField(source.ActivityFieldInput{Name: source.FieldCashAmount, Money: cash})
	family, _ := source.NewActivityField(source.ActivityFieldInput{Name: source.FieldFamily, Text: "cash"})
	description, _ := source.NewActivityField(
		source.ActivityFieldInput{Name: source.FieldDescription, Text: "Synthetic dividend"},
	)

	value, err := source.NewActivityObservation(source.ActivityObservationInput{
		Location: "row[1]",
		Account:  account,
		Event:    event,
		Fields:   []source.ActivityField{family, description, amount},
	})
	if err != nil {
		t.Fatal(err)
	}

	observation := application.SourceObservation{Observation: application.Observation{
		Artifact: "synthetic-report", ActivityID: "movement", Location: "row[1]", Reported: value,
	}}
	record := &application.PublishedActivity{
		PublishedActivityHeader: application.PublishedActivityHeader{Family: "cash"},
		Cash: &application.PublishedCash{
			Category:    "dividend",
			Description: new("Synthetic dividend"),
		},
	}
	for _, tc := range []struct {
		name, wantLink string
		store          sourceMovementReadStore
	}{
		{"linked cash movement", "/activity/movement#accepted-activity", sourceMovementReadStore{activity: record}},
		{"accepted movement unavailable", "", sourceMovementReadStore{}},
		{"history replay", "", sourceMovementReadStore{updating: true, activity: record}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workflow := application.New(tc.store, nil, nil, nil, nil, nil)
			entry := sourceEvidence(t.Context(), workflow, host.Retained{}, observation, "")

			var html strings.Builder
			if err := RetainedSourceEntry(entry).Render(t.Context(), &html); err != nil {
				t.Fatal(err)
			}

			if entry.RecordURL != tc.wantLink || entry.Title != "Synthetic dividend" ||
				!strings.Contains(html.String(), "7.123456700 USD") ||
				!strings.Contains(html.String(), "Source ID dividend-1") {
				t.Fatalf("source evidence or related record unexpectedly changed: %+v", entry)
			}
		})
	}
}
