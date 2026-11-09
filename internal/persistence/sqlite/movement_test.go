// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"encoding/json"
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

func TestMovementPartialObservationsCollectivelyEstablishRequiredTerms(t *testing.T) {
	account, _ := source.NewReference("ibkr", "account", "", "U1")
	event, _ := source.NewReference("fixture", "movement", "U1", "partial")
	instrument, _ := source.NewInstrument(source.InstrumentInput{Kind: source.Equity, Ticker: "SYN"})
	quantity, _ := financial.NewQuantity("1.2300")
	date, _ := financial.ParseDate("2026-10-20")
	currency, _ := financial.ParseCurrency("USD")
	amount, _ := financial.NewMoney("-12.3400", currency)

	transfer, _ := source.NewSecurityTransfer(
		source.SecurityTransferInput{
			Location:      "row",
			Account:       account,
			Event:         event,
			EventDistinct: true,
			Instrument:    instrument,
			Direction:     source.TransferIn,
			Quantity:      quantity,
			Date:          date,
		},
	)
	cash, _ := source.NewCashActivity(
		source.CashActivityInput{
			Location:      "row",
			Account:       account,
			Event:         event,
			EventDistinct: true,
			Category:      source.CashFee,
			Amount:        amount,
			Date:          date,
		},
	)
	for _, tc := range []struct {
		name     string
		value    source.ActivityObservation
		required source.ActivityFieldName
	}{{"security-transfer", transfer.Observation(), source.FieldQuantity}, {"cash", cash.Observation(), source.FieldCashAmount}} {
		t.Run(tc.name, func(t *testing.T) {
			s := coverageStore(t)

			subject := tc.value.Fields()
			base := []source.ActivityField{}

			var missing source.ActivityField
			for _, field := range subject.Fields {
				if field.Name() == tc.required {
					missing = field
				} else {
					base = append(base, field)
				}
			}

			processPartial(t, s, "incomplete", partialObservation(t, subject, base...))

			if tableCount(t, s, "executions") != 0 {
				t.Fatal("incomplete movement accepted")
			}

			processPartial(t, s, "completion", partialObservation(t, subject, missing))

			if tableCount(t, s, "executions") != 1 {
				t.Fatal("compatible partials did not establish movement")
			}

			before, _ := snapshotState(t.Context(), s)
			processPartial(t, s, "repeat-completion", partialObservation(t, subject, missing))

			after, _ := snapshotState(t.Context(), s)
			if before.Version != after.Version {
				t.Fatal("repeated partial changed publication")
			}
		})
	}
}

const transferRow = `<Transfer accountId="U1" transactionID="transfer1" assetCategory="STK" subCategory="COMMON" conid="100" symbol="TEST" description="Synthetic security" currency="USD" levelOfDetail="TRANSFER" type="ACATS" direction="IN" quantity="12.0000000000001" date="2026-10-20" dateTime="2026-10-20" reportDate="2026-10-21" settleDate="2026-10-22" transferPrice="123.4500" positionAmount="1481.400000" cost="999.0000" pnlAmount="0.0000" deliveringBroker="Synthetic delivering broker" company="Synthetic broker" account="SYNTHETIC"/>`

func movementReport(section, rows string) string {
	return `<FlexQueryResponse type="AF"><FlexStatements><FlexStatement accountId="U1" fromDate="2026-10-20" toDate="2026-10-21"><` + section + `>` + rows + `</` + section + `></FlexStatement></FlexStatements></FlexQueryResponse>`
}

const cashRow = `<CashTransaction accountId="U1" transactionID="cash1" actionID="action1" levelOfDetail="DETAIL" type="Dividends" amount="12.34000000000000001" currency="USD" dateTime="2026-10-20" reportDate="2026-10-21" availableForTradingDate="2026-10-23" description="Synthetic dividend"/>`

func TestCashInstrumentEvidenceChoiceBindsAndReleasesAtomically(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		for _, scope := range []string{"evidence/instrument", "observation"} {
			t.Run(map[bool]string{false: "unresolved/", true: "automatic/"}[automatic]+scope, func(t *testing.T) {
				s := coverageStore(t)
				acquireRealized(t, s, movementReport("CashTransactions", cashRow))
				rows, _ := s.queries.Executions(t.Context())
				id := rows[0].ID
				retainDecision(t, s, "activity", id, "cash_amount", "13.0000", "USD", "set")

				a := strings.Replace(
					cashRow,
					"<CashTransaction ",
					`<CashTransaction assetCategory="STK" subCategory="COMMON" conid="100" symbol="A" `,
					1,
				)
				if automatic {
					acquireRealized(t, s, movementReport("CashTransactions", a))
				}

				b := strings.NewReplacer(`conid="100"`, `conid="101"`, `symbol="A"`, `symbol="B"`).Replace(a)
				artifact := acquireRealized(t, s, movementReport("CashTransactions", a+b))
				base, _ := s.queries.ActivityBase(t.Context(), id)
				automaticID := base.InstrumentID.String

				records, err := s.Records(t.Context(), artifact, ibkrflex.Version)
				if err != nil {
					t.Fatal(err)
				}

				key := ""
				for _, record := range records {
					field, ok := record.Observation.Field(source.FieldInstrument)
					if ok && field.Fields().Instrument.Reference().Value() == "101" {
						data, _ := json.Marshal([3]string{artifact, ibkrflex.Version, record.Location})
						key = string(data)
					}
				}

				if key == "" {
					t.Fatal("selected source evidence missing")
				}

				form, err := s.CorrectionForm(t.Context(), "activity", id)
				if err != nil {
					t.Fatal(err)
				}

				d, err := application.NewDecision(
					application.DecisionInput{
						TargetKind:       "activity",
						TargetID:         id,
						Scope:            scope,
						Text:             key,
						Action:           "set",
						Submission:       "select-cash-instrument",
						ExpectedRevision: form.Revision,
						Rationale:        "Choose retained contract B",
					},
				)
				if err != nil {
					t.Fatal(err)
				}

				before, _ := snapshotState(t.Context(), s)
				roots := tableCount(t, s, "instruments")
				if err = s.CheckCorrection(t.Context(), d); err != nil {
					t.Fatal(err)
				}

				after, _ := snapshotState(t.Context(), s)
				if before.Version != after.Version || tableCount(t, s, "instruments") != roots {
					t.Fatal("preflight materialized an instrument")
				}

				if !automatic && scope == "evidence/instrument" {
					if _, err = s.db.Exec(
						`CREATE TRIGGER fail_selected_cash BEFORE UPDATE ON executions WHEN NEW.instrument_id IS NOT NULL BEGIN SELECT RAISE(ABORT,'synthetic binding failure'); END`,
					); err != nil {
						t.Fatal(err)
					}

					if err = s.SaveDecision(t.Context(), d, "synthetic", time.Now()); err == nil {
						t.Fatal("failed binding was accepted")
					}

					after, _ = snapshotState(t.Context(), s)
					if after.Version != before.Version || tableCount(t, s, "instruments") != roots {
						t.Fatal("failed selection leaked a root or publication")
					}

					if _, err = s.db.Exec(`DROP TRIGGER fail_selected_cash`); err != nil {
						t.Fatal(err)
					}
				}

				if err = s.SaveDecision(t.Context(), d, "synthetic", time.Now()); err != nil {
					t.Fatal(err)
				}

				value := realizedActivity(t, s, id).Activity
				if value.InstrumentID == nil || value.Cash.Amount.Amount != "13.0000" {
					t.Fatal("evidence did not bind the posting or lost another decision", value)
				}

				root, err := s.queries.Instrument(t.Context(), *value.InstrumentID)
				if err != nil || root.Ticker != "B" {
					t.Fatal("selected a different instrument", root, err)
				}

				base, _ = s.queries.ActivityBase(t.Context(), id)
				if base.InstrumentID.String != automaticID {
					t.Fatal("operator choice replaced the automatic baseline")
				}

				before, _ = snapshotState(t.Context(), s)
				if err = s.SaveDecision(t.Context(), d, "synthetic", time.Now()); err != nil {
					t.Fatal(err)
				}

				after, _ = snapshotState(t.Context(), s)
				if before.Version != after.Version {
					t.Fatal("repeated selection republished")
				}

				explicitID := automaticID
				if explicitID == "" {
					explicitID = root.ID
				}

				retainDecision(t, s, "activity", id, "instrument", explicitID, "", "set")

				if v := realizedActivity(t, s, id).Activity.InstrumentID; v == nil || *v != explicitID {
					t.Fatal("evidence displaced an explicit link decision")
				}

				retainDecision(t, s, "activity", id, "instrument", "", "", "release")

				if v := realizedActivity(t, s, id).Activity.InstrumentID; v == nil || *v != root.ID {
					t.Fatal("link release lost active instrument evidence")
				}

				retainDecision(t, s, "activity", id, scope, "", "", "release")

				value = realizedActivity(t, s, id).Activity
				if automaticID == "" && value.InstrumentID != nil ||
					automaticID != "" && (value.InstrumentID == nil || *value.InstrumentID != automaticID) ||
					value.Cash.Amount.Amount != "13.0000" {
					t.Fatal("release did not restore automatic binding and independent decisions", value)
				}
			})
		}
	}
}

func TestCashInstrumentEvidenceChoiceRetainsOptionUnderlierClaims(t *testing.T) {
	s := coverageStore(t)
	artifact := acquireRealized(t, s, movementReport("CashTransactions", cashRow))

	records, err := s.Records(t.Context(), artifact, ibkrflex.Version)
	if err != nil {
		t.Fatal(err)
	}

	cash, err := records[0].Observation.CashActivity()
	if err != nil {
		t.Fatal(err)
	}

	rows, _ := s.queries.Executions(t.Context())
	id := rows[0].ID

	for _, name := range []string{"A", "B"} {
		underlierRef, _ := source.NewReference("synthetic", "instrument", "", "underlier-"+name)
		underlier, _ := source.NewInstrument(
			source.InstrumentInput{Kind: source.Equity, Reference: underlierRef, Ticker: "U" + name},
		)
		terms, _ := source.NewOptionTerms(source.OptionInput{Underlier: underlier})
		ref, _ := source.NewReference("synthetic", "instrument", "", "option-"+name)
		instrument, _ := source.NewInstrument(
			source.InstrumentInput{Kind: source.Option, Reference: ref, Ticker: "O" + name, Option: terms},
		)

		fields := cash.Fields()
		fields.Instrument = instrument
		fields.Location = "selected-option"
		if name == "B" {
			partialInstrument, _ := source.NewInstrument(
				source.InstrumentInput{Kind: source.Option, Reference: ref, Ticker: "O" + name},
			)
			fields.Instrument = partialInstrument

			partial, e := source.NewCashActivity(fields)
			if e != nil {
				t.Fatal(e)
			}

			processPartial(t, s, "option-B-partial", partial.Observation())

			fields.Instrument = instrument
		}

		value, e := source.NewCashActivity(fields)
		if e != nil {
			t.Fatal(e)
		}

		processPartial(t, s, "option-"+name, value.Observation())
	}

	base, _ := s.queries.ActivityBase(t.Context(), id)

	records, err = s.Records(t.Context(), "option-B-partial", partialVersion)
	if err != nil {
		t.Fatal(err)
	}

	key, _ := json.Marshal([3]string{"option-B-partial", partialVersion, records[0].Location})

	form, err := s.CorrectionForm(t.Context(), "activity", id)
	if err != nil {
		t.Fatal("form", err)
	}

	decision, err := application.NewDecision(
		application.DecisionInput{
			TargetKind:       "activity",
			TargetID:         id,
			Scope:            "evidence/instrument",
			Text:             string(key),
			Action:           "set",
			Submission:       "select-option-instrument",
			ExpectedRevision: form.Revision,
			Rationale:        "Choose retained option B",
		},
	)
	if err != nil {
		t.Fatal("decision", err)
	}

	if err = s.CheckCorrection(t.Context(), decision); err != nil {
		t.Fatal("preflight", err)
	}

	if err = s.SaveDecision(t.Context(), decision, "synthetic", time.Now()); err != nil {
		t.Fatal("save", err)
	}

	selected := realizedActivity(t, s, id).Activity.InstrumentID
	if selected == nil {
		t.Fatal("selected option was not bound")
	}

	root, err := s.queries.Instrument(t.Context(), *selected)
	if err != nil || root.Ticker != "OB" || !root.UnderlierID.Valid {
		t.Fatal("selected option lost its underlier", root, err)
	}

	underlier, err := s.queries.Instrument(t.Context(), root.UnderlierID.String)
	if err != nil || underlier.Ticker != "UB" {
		t.Fatal("selected nested instrument was not projected", underlier, err)
	}

	retainDecision(t, s, "activity", id, "evidence/instrument", "", "", "release")

	value := realizedActivity(t, s, id).Activity
	if value.InstrumentID == nil || *value.InstrumentID != base.InstrumentID.String {
		t.Fatal("release did not restore the original automatic option", value)
	}
}

func TestMovementSearchMatchesRenderedLabels(t *testing.T) {
	s := coverageStore(t)
	acquireRealized(
		t,
		s,
		movementReport("Transfers", strings.Replace(transferRow, `direction="IN"`, `direction="OUT"`, 1)),
	)
	cash := strings.NewReplacer(`type="Dividends"`, `type="Payment In Lieu Of Dividends"`, "Synthetic dividend", "Synthetic distribution").
		Replace(cashRow)
	acquireRealized(t, s, movementReport("CashTransactions", cash))
	acquireRealized(
		t,
		s,
		movementReport(
			"CashTransactions",
			strings.Replace(cash, `amount="12.34000000000000001"`, `amount="13"`, 1),
		),
	)
	transfer := strings.NewReplacer(`direction="IN"`, `direction="OUT"`, `quantity="12.0000000000001"`, `quantity="13"`).
		Replace(transferRow)
	acquireRealized(t, s, movementReport("Transfers", transfer))

	for _, tc := range []struct {
		search string
		want   int
	}{
		{"Transfer out", 1},
		{"Transfer out · TEST", 1},
		{"Transfer in", 0},
		{"Payment in lieu · Synthetic distribution", 1},
		{"payment in lieu", 1},
		{"Synthetic distribution", 1},
		{"Payment in lieu · Unreported", 0},
	} {
		query, _ := application.NewListQuery(application.ListOptions{
			Search: tc.search,
			ActivityText: application.ActivityText{
				Unknown:            "Unknown",
				LifecycleSeparator: " · ",
				MovementSeparator:  " · ",
				TransferPrefix:     "Transfer ",
			},
		}, application.ActivityScope)
		list, err := s.ActivityList(t.Context(), query)
		if err != nil || len(list.Items) != tc.want || list.Matched != int64(tc.want) {
			t.Errorf("rendered label %q: %d rows, %d matched, %v", tc.search, len(list.Items), list.Matched, err)
		}

		reviewQuery, _ := application.NewListQuery(
			application.ListOptions{Search: tc.search, ActivityText: query.Options().ActivityText},
			application.ReviewScope,
		)
		review, err := s.ReviewList(t.Context(), reviewQuery)
		if err != nil || len(review.Items) != tc.want || review.Matched != int64(tc.want) {
			t.Errorf("review label %q: %d rows, %d matched, %v", tc.search, len(review.Items), review.Matched, err)
		}
	}

	unknown := strings.NewReplacer(`transactionID="cash1"`, `transactionID="unknown-description"`, `description="Synthetic distribution"`, `description=""`).
		Replace(cash)
	acquireRealized(t, s, movementReport("CashTransactions", unknown))
	acquireRealized(
		t,
		s,
		movementReport(
			"CashTransactions",
			strings.Replace(unknown, `amount="12.34000000000000001"`, `amount="13"`, 1),
		),
	)

	options := application.ListOptions{
		Search:         "Unknown",
		DisplayMatches: application.DisplayMatches{MissingValues: true},
	}
	for _, scope := range []application.ListScope{application.ActivityScope, application.ReviewScope} {
		query, _ := application.NewListQuery(options, scope)
		if scope == application.ReviewScope {
			list, err := s.ReviewList(t.Context(), query)
			if err != nil || list.Matched != 1 {
				t.Fatal("review Unknown search used an invisible instrument name", list.Matched, err)
			}
		} else {
			list, err := s.ActivityList(t.Context(), query)
			if err != nil || list.Matched != 2 {
				t.Fatal("activity Unknown search omitted the cash description", list.Matched, err)
			}
		}
	}
}

func TestMovementListUsesDisplayedLabelsAndPlaceholders(t *testing.T) {
	s := coverageStore(t)
	sell := strings.NewReplacer(`buySell="BUY"`, `buySell="SELL"`, `conid="100"`, `conid="300"`).
		Replace(equityRow("U1", "sell", "10"))
	coverageReport(t, s, "sell", sell)

	for _, name := range []string{"alpha", "Zulu"} {
		cash := strings.NewReplacer(`transactionID="cash1"`, `transactionID="cash-`+name+`"`, `type="Dividends"`, `type="Other Fees"`, "Synthetic dividend", name).
			Replace(cashRow)
		acquireRealized(t, s, movementReport("CashTransactions", cash))
	}

	for i, name := range []string{"Synthetic security", "alpha", "Zulu"} {
		row := strings.NewReplacer(`transactionID="transfer1"`, `transactionID="transfer-`+name+`"`, `conid="100"`, `conid="10`+string(rune('0'+i))+`"`, `symbol="TEST"`, `symbol="`+strings.ToUpper(name[:1])+`YN"`, "Synthetic security", name).
			Replace(transferRow)
		if i > 0 {
			row = strings.Replace(row, `direction="IN"`, `direction="OUT"`, 1)
		}

		acquireRealized(t, s, movementReport("Transfers", row))
	}

	want := []string{
		"cash/alpha",
		"cash/Zulu",
		"trade/sell",
		"security-transfer/in/Synthetic security",
		"security-transfer/out/alpha",
		"security-transfer/out/Zulu",
	}
	for _, direction := range []string{"asc", "desc"} {
		query, _ := application.NewListQuery(
			application.ListOptions{
				Sort:      "label",
				Direction: direction,
				ActivityText: application.ActivityText{
					Unknown:           "Unknown",
					MovementSeparator: " · ",
					TransferPrefix:    "Transfer ",
				},
			},
			application.ActivityScope,
		)

		list, err := s.ActivityList(t.Context(), query)
		if err != nil || len(list.Items) != len(want) {
			t.Fatal(list, err)
		}

		for i, item := range list.Items {
			a := item.Resource.Activity
			label := a.Family
			switch a.Family {
			case "cash":
				label += "/" + *a.Cash.Description
			case "trade":
				label += "/" + *a.Side
			case "security-transfer":
				label += "/" + a.Transfer.Direction + "/" + item.InstrumentName
			}

			position := i
			if direction == "desc" {
				position = len(want) - 1 - i
			}

			if label != want[position] {
				t.Errorf("%s row %d: %q, want %q", direction, i, label, want[position])
			}
		}
	}

	query, _ := application.NewListQuery(
		application.ListOptions{
			Search:         "not applicable",
			DisplayMatches: application.DisplayMatches{InapplicableValues: true},
			PageSize:       2,
		},
		application.ActivityScope,
	)

	list, err := s.ActivityList(t.Context(), query)
	if err != nil || list.Matched != 6 || len(list.Items) != 2 {
		t.Fatal("visible Not applicable cells missing", list, err)
	}
}

func TestUnclassifiedCashInstrumentReconcilesWithReportedFamily(t *testing.T) {
	for _, tc := range []struct{ family, contract, trade string }{
		{"debt", "debt-1", syntheticDebt}, {"option", "200", syntheticOption},
	} {
		for _, cashFirst := range []bool{false, true} {
			t.Run(
				tc.family+map[bool]string{false: "/trade-first", true: "/cash-first"}[cashFirst],
				func(t *testing.T) {
					s := coverageStore(t)
					cash := strings.Replace(
						cashRow,
						"<CashTransaction ",
						`<CashTransaction conid="`+tc.contract+`" `,
						1,
					)
					if cashFirst {
						acquireRealized(t, s, movementReport("CashTransactions", cash))
					}

					coverageReport(t, s, "typed-instrument", tc.trade)

					if !cashFirst {
						acquireRealized(t, s, movementReport("CashTransactions", cash))
					}

					rows, err := s.queries.Executions(t.Context())
					if err != nil || len(rows) != 2 {
						t.Fatal(rows, err)
					}

					if !rows[0].InstrumentID.Valid || rows[0].InstrumentID != rows[1].InstrumentID {
						t.Fatal("shared contract split roots", rows)
					}

					root, err := s.queries.Instrument(t.Context(), rows[0].InstrumentID.String)
					if err != nil || root.Kind != tc.family || root.ReferenceStatus == "conflict" {
						t.Fatal("unclassified cash contradicted known family", root, err)
					}

					var cashID string
					for _, row := range rows {
						if row.Kind == "cash" {
							cashID = row.ID
						}
					}

					form, err := s.CorrectionForm(t.Context(), "activity", cashID)
					if err != nil {
						t.Fatal(err)
					}

					found := false
					for _, link := range form.Links {
						if link.Scope == "instrument" && link.Value == root.ID {
							found = true
						}
					}

					if !found {
						t.Fatal("bound instrument family missing from correction choices", form.Links)
					}

					decision, err := application.NewDecision(application.DecisionInput{
						TargetKind: "activity", TargetID: cashID, Scope: "instrument", Text: root.ID,
						Action: "set", Submission: "unclassified-link", ExpectedRevision: form.Revision,
					})
					if err != nil {
						t.Fatal(err)
					}

					if err = s.CheckCorrection(t.Context(), decision); err != nil {
						t.Fatal("offered link failed preflight", err)
					}

					if err = s.SaveDecision(t.Context(), decision, "synthetic", time.Now()); err != nil {
						t.Fatal(err)
					}

					retainDecision(t, s, "activity", cashID, "instrument", "", "", "release")

					value := realizedActivity(t, s, cashID).Activity
					if value.InstrumentID == nil || *value.InstrumentID != root.ID {
						t.Fatal("release lost the original family binding", value)
					}
				},
			)
		}
	}
}

func TestCashInstrumentEnrichmentPreservesPostingAndDecisions(t *testing.T) {
	for _, tc := range []struct {
		name, attributes string
		ambiguous        bool
	}{
		{"compatible", `assetCategory="STK" subCategory="COMMON" conid="100" symbol="TEST" `, false},
		{"symbol-only", `symbol="TEST" `, false},
		{"ambiguous", `assetCategory="STK" subCategory="COMMON" conid="100" symbol="TEST" `, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := coverageStore(t)
			acquireRealized(t, s, movementReport("CashTransactions", cashRow))
			rows, _ := s.queries.Executions(t.Context())
			id := rows[0].ID
			retainDecision(t, s, "activity", id, "cash_amount", "13.0000", "USD", "set")

			enriched := strings.Replace(
				cashRow,
				"<CashTransaction ",
				`<CashTransaction `+tc.attributes,
				1,
			)
			if tc.ambiguous {
				enriched += strings.Replace(enriched, `conid="100"`, `conid="101"`, 1)
			}

			acquireRealized(t, s, movementReport("CashTransactions", enriched))

			rows, _ = s.queries.Executions(t.Context())
			value := realizedActivity(t, s, id).Activity
			if len(rows) != 1 || rows[0].ID != id || value.Cash.Amount.Amount != "13.0000" {
				t.Fatal("enrichment changed posting identity or decision", rows, value)
			}

			if tc.ambiguous {
				if value.InstrumentID != nil || !value.Conflict || tableCount(t, s, "instruments") != 0 {
					t.Fatal("ambiguous instruments established an automatic link", value)
				}

				return
			}

			if value.InstrumentID == nil || tableCount(t, s, "instrument_claims") == 0 {
				t.Fatal("later instrument evidence was not bound", value)
			}

			instrument, err := s.Resource(t.Context(), "instruments", *value.InstrumentID)
			if err != nil || instrument.Instrument == nil || instrument.Instrument.Ticker == nil ||
				*instrument.Instrument.Ticker != "TEST" {
				t.Fatal("instrument claims were not published", instrument, err)
			}

			before, _ := snapshotState(t.Context(), s)
			acquireRealized(t, s, movementReport("CashTransactions", enriched))

			after, _ := snapshotState(t.Context(), s)
			if before.Version != after.Version {
				t.Fatal("repeated enrichment republished")
			}

			retainDecision(t, s, "activity", id, "cash_amount", "", "", "release")

			value = realizedActivity(t, s, id).Activity
			if value.InstrumentID == nil || value.Cash.Amount.Amount != "12.34000000000000001" {
				t.Fatal("release lost the enriched link or original amount", value)
			}
		})
	}
}

func TestCashPostingsStayDistinctAndResolveTaxRelationships(t *testing.T) {
	s := coverageStore(t)
	tax := `<SalesTax accountId="U1" transactionID="tax1" taxableTransactionID="cash1" salesTax="-0.2300" currency="USD" date="2026-10-20"/>`
	acquireRealized(t, s, movementReport("SalesTaxes", tax))

	rows, _ := s.queries.Executions(t.Context())
	taxID := rows[0].ID
	if realizedActivity(t, s, taxID).Activity.Relationships[0].TargetID != nil {
		t.Fatal("invented missing tax target")
	}

	withholding := strings.NewReplacer(`transactionID="cash1"`, `transactionID="cash2"`, `type="Dividends"`, `type="Withholding Tax"`, `amount="12.34000000000000001"`, `amount="-1.2300"`).
		Replace(cashRow)
	zero := strings.NewReplacer(`transactionID="cash1"`, `transactionID="cash3"`, `amount="12.34000000000000001"`, `amount="0.0000"`).
		Replace(cashRow)
	acquireRealized(t, s, movementReport("CashTransactions", cashRow+withholding+zero))

	rows, _ = s.queries.Executions(t.Context())
	if len(rows) != 4 {
		t.Fatal("shared action collapsed cash entries", len(rows))
	}

	taxValue := realizedActivity(t, s, taxID)
	if taxValue.Activity.Relationships[0].TargetID == nil {
		t.Fatal("tax link did not resolve")
	}

	id := *taxValue.Activity.Relationships[0].TargetID
	v := realizedActivity(t, s, id)
	if v.Activity.Cash.Amount.Amount != "12.34000000000000001" || v.Activity.InstrumentID != nil ||
		v.Activity.At != nil || v.Activity.Cash.AvailableForTradingDate == nil || *v.Activity.Cash.AvailableForTradingDate != "2026-10-23" {
		t.Fatal(v)
	}

	retainDecision(t, s, "activity", id, "cash_amount", "13.0000", "USD", "set")

	if realizedActivity(t, s, id).Activity.Cash.Amount.Amount != "13.0000" {
		t.Fatal("cash correction failed")
	}

	retainDecision(t, s, "activity", id, "cash_amount", "", "", "release")

	if realizedActivity(t, s, id).Activity.Cash.Amount.Amount != "12.34000000000000001" {
		t.Fatal("cash release failed")
	}

	query, _ := application.NewListQuery(
		application.ListOptions{Kind: "cash", Sort: "amount", Direction: "asc"},
		application.ActivityScope,
	)

	list, err := s.ActivityList(t.Context(), query)
	if err != nil || len(list.Items) != 4 || list.Items[0].Resource.Activity.Cash.Amount.Amount != "-1.2300" ||
		list.Items[3].Resource.Activity.Cash.Amount.Amount != "12.34000000000000001" {
		t.Fatal(list, err)
	}

	query, _ = application.NewListQuery(
		application.ListOptions{Search: "Synthetic dividend"},
		application.ActivityScope,
	)

	list, err = s.ActivityList(t.Context(), query)
	if err != nil || len(list.Items) != 3 {
		t.Fatal("cash description search", list, err)
	}

	euro := strings.NewReplacer(`transactionID="cash1"`, `transactionID="cash-eur"`, `currency="USD"`, `currency="EUR"`, `amount="12.34000000000000001"`, `amount="1000.0000"`).
		Replace(cashRow)
	acquireRealized(t, s, movementReport("CashTransactions", euro))

	query, _ = application.NewListQuery(
		application.ListOptions{Kind: "cash", Sort: "amount", Direction: "asc"},
		application.ActivityScope,
	)

	list, err = s.ActivityList(t.Context(), query)
	if err != nil || len(list.Items) != 5 || list.Items[0].Resource.Activity.Cash.Amount.Currency != "EUR" ||
		list.Items[1].Resource.Activity.Cash.Amount.Amount != "-1.2300" {
		t.Fatal("cash ordering compared unrelated currencies", list, err)
	}
}

func TestMovementInterpretationUpgradeRetriesAtomicallyFromRetainedReports(t *testing.T) {
	s := coverageStore(t)
	coverageReport(t, s, "existing", equityRow("U1", "fill", "100"))
	rows, _ := s.queries.Executions(t.Context())
	existing := rows[0].ID
	retainDecision(t, s, "activity", existing, "price", "99.0000", "USD", "set")

	body := strings.Replace(
		movementReport("Transfers", transferRow),
		"</FlexStatement>",
		"<CashTransactions>"+cashRow+"</CashTransactions></FlexStatement>",
		1,
	)
	artifact, _ := provider.NewArtifact(ibkrflex.ActivityFormat, "application/xml", time.Now(), []byte(body))
	key := host.WorkKey{Connection: "synthetic", Stream: ibkrflex.Activity, Operation: "acquire"}
	cp, _ := s.Checkpoint(t.Context(), key)
	step, _ := provider.NewStep(provider.State{}, artifact, time.Now().Add(time.Hour))

	id, err := s.Accept(t.Context(), key, cp.Revision, step, nil, provider.Progress{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	old := claimNormalizer(func(context.Context, provider.Artifact, provider.Claims) (provider.Result, error) {
		return provider.NewResult(provider.Counts{Rows: 2, Ignored: 2})
	})
	if _, err = s.Normalize(t.Context(), id, "old-movements", old); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(t.Context(), id, "old-movements"); err != nil {
		t.Fatal(err)
	}

	before, _ := snapshotState(t.Context(), s)
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

	if _, err = s.db.Exec(
		`CREATE TRIGGER fail_cash_upgrade BEFORE INSERT ON executions WHEN NEW.kind='cash' BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`,
	); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(t.Context(), id, ibkrflex.Version); err == nil {
		t.Fatal("upgrade failure acknowledged")
	}

	after, _ := snapshotState(t.Context(), s)
	if after.Version != before.Version || tableCount(t, s, "executions") != 1 {
		t.Fatal("partial upgrade escaped transaction")
	}

	if _, err = s.db.Exec(`DROP TRIGGER fail_cash_upgrade`); err != nil {
		t.Fatal(err)
	}

	if err = s.Process(t.Context(), id, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	if tableCount(t, s, "executions") != 3 ||
		realizedActivity(t, s, existing).Activity.Price.Amount != "99.0000" {
		t.Fatal("upgrade lost identities or decisions")
	}

	after, _ = snapshotState(t.Context(), s)
	if after.Version == before.Version {
		t.Fatal("upgrade did not publish")
	}

	if err = s.Process(t.Context(), id, ibkrflex.Version); err != nil {
		t.Fatal(err)
	}

	again, _ := snapshotState(t.Context(), s)
	if again.Version != after.Version {
		t.Fatal("repeat upgrade republished")
	}

	pending, err = s.PendingArtifacts(t.Context(), time.Now(), map[string]string{"ibkr-flex": ibkrflex.Version})
	if err != nil || len(pending) != 0 {
		t.Fatal("completed update requeued", pending, err)
	}
}

func TestSecurityTransfersReconcileCorrectAndPublish(t *testing.T) {
	s := coverageStore(t)
	lot := strings.ReplaceAll(
		strings.ReplaceAll(transferRow, "<Transfer ", "<TransferLot "),
		`levelOfDetail="TRANSFER"`,
		`levelOfDetail="LOT"`,
	)
	acquireRealized(t, s, movementReport("Transfers", transferRow+lot))

	rows, err := s.queries.Executions(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}

	id := rows[0].ID

	v := realizedActivity(t, s, id)
	if v.Activity.Family != "security-transfer" || v.Activity.Quantity.Value != "12.0000000000001" ||
		v.Activity.Transfer.ReportedCost.Amount != "999.0000" ||
		v.Activity.Price != nil || v.Activity.Transfer.DeliveringBroker == nil || *v.Activity.Transfer.DeliveringBroker != "Synthetic delivering broker" {
		t.Fatal(v)
	}

	before, _ := snapshotState(t.Context(), s)
	acquireRealized(t, s, movementReport("Transfers", transferRow+lot))

	after, _ := snapshotState(t.Context(), s)
	if before.Version != after.Version {
		t.Fatal("repeat changed publication")
	}

	retainDecision(t, s, "activity", id, "direction", "out", "", "set")

	if realizedActivity(t, s, id).Activity.Transfer.Direction != "out" {
		t.Fatal("direction correction missing")
	}

	retainDecision(t, s, "activity", id, "direction", "", "", "release")

	if realizedActivity(t, s, id).Activity.Transfer.Direction != "in" {
		t.Fatal("direction release missing")
	}

	changed := strings.ReplaceAll(transferRow, `quantity="12.0000000000001"`, `quantity="13"`)
	acquireRealized(t, s, movementReport("Transfers", changed))

	v = realizedActivity(t, s, id)
	if !v.Activity.Conflict || v.Activity.Quantity.Value != "12.0000000000001" {
		t.Fatal("conflict replaced baseline", v)
	}

	q, _ := application.NewListQuery(
		application.ListOptions{Kind: "security-transfer"},
		application.ActivityScope,
	)

	list, err := s.ActivityList(t.Context(), q)
	if err != nil || len(list.Items) != 1 {
		t.Fatal(list, err)
	}

	for _, search := range []string{"123.4500 USD", "123.4500 EUR"} {
		query, _ := application.NewListQuery(application.ListOptions{Search: search}, application.ActivityScope)
		list, err = s.ActivityList(t.Context(), query)

		want := 0
		if search == "123.4500 USD" {
			want = 1
		}

		if err != nil || len(list.Items) != want || list.Matched != int64(want) {
			t.Fatal("transfer price search lost amount/currency or count consistency", search, list, err)
		}
	}
}
