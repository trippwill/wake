// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package admin

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/source"
)

type sourceEvidenceEntry struct {
	application.SourceObservation
	Facts                                           []sourceFact
	References                                      []string
	Raw                                             sourceExcerpt
	Title, Focus, ReportedPrice, ReportedPriceBasis string
	RecordLabel, RecordURL, Price, PriceBasis       string
	PriceCorrected, PriceContributed                bool
}

type sourceFact struct {
	Key                    source.ActivityFieldName
	Label, Value, Accepted string
}

func sourceEntryURL(key application.SourceLocation, field string, query url.Values) string {
	params := reportReadingQuery(query)
	params.Set("version", key.Version)
	params.Set("entry", key.Location)

	if field != "" {
		params.Set("field", field)
	}

	return (&url.URL{Path: "/evidence/" + key.Artifact, RawQuery: params.Encode(), Fragment: "source-entry"}).String()
}

func observationLocation(v application.Observation) application.SourceLocation {
	return application.SourceLocation{Artifact: v.Artifact, Version: v.Version, Location: v.Location}
}

func reportIndexURL(id string, query url.Values) string {
	return (&url.URL{
		Path: "/evidence/" + id, RawQuery: reportReadingQuery(query).Encode(), Fragment: "report-entries",
	}).String()
}

func reportReadingQuery(query url.Values) url.Values {
	params := make(url.Values)
	for _, name := range []string{"q", "sort", "dir", "page", "size", "selected", "receipt"} {
		if value := query.Get(name); value != "" {
			params.Set(name, value)
		}
	}

	return params
}

func evidencePeriod(info application.EvidenceContext) string {
	if r := info.SelectedReceipt; r != nil {
		return reportPeriod(r.ReportedFrom, r.ReportedTo)
	}

	return reportPeriod(info.ReportedFrom, info.ReportedTo)
}

func sourceEvidence(
	ctx context.Context,
	workflow *application.Workflow,
	retained host.Retained,
	observation application.SourceObservation,
	field string,
) sourceEvidenceEntry {
	fields := observation.Reported.Fields()
	v := sourceEvidenceEntry{
		SourceObservation: observation,
		Title:             sourceObservationTitle(observation.Reported),
		Facts:             sourceObservationFacts(observation.Reported),
		References:        brokerEventLabels(fields.Event, fields.EventAliases),
		Raw:               retainedSourceExcerpt(ctx, retained.Artifact, observation.Location),
	}
	reportedPrice, knownPrice := observation.Reported.Field(source.FieldPrice)
	if knownPrice {
		price := reportedPrice.Fields()
		instrument, _ := observation.Reported.Field(source.FieldInstrument)
		v.ReportedPrice = priceText(price.Price)
		v.ReportedPriceBasis = tradePriceBasis(instrument.Fields().Instrument, price.QuotationBasis)
	}

	if field == string(source.FieldPrice) && knownPrice && reportedPrice.Known() {
		v.Focus = field
	}

	if observation.ActivityID == "" || workflow.HistoryUpdating() {
		return v
	}

	family, _ := observation.Reported.Field(source.FieldFamily)
	if knownPrice || family.Fields().Text == "trade" {
		detail, err := workflow.ExecutionDetail(ctx, observation.ActivityID)
		if err == nil {
			terms := detail.Execution.Terms().Fields()
			v.RecordURL = "/executions/" + url.PathEscape(observation.ActivityID) + "#accepted-activity"
			v.RecordLabel = stringTitle(
				string(terms.Side),
			) + " " + instrumentTitle(
				detail.Context.InstrumentTicker,
				detail.Context.InstrumentName,
			)
			if knownPrice {
				v.RecordURL = "/executions/" + url.PathEscape(observation.ActivityID) + "#activity-price"
				v.Price = priceText(terms.Price)
				v.PriceBasis = tradePriceBasis(terms.Instrument, detail.Execution.Terms().QuotationBasis())
				v.PriceCorrected = detail.Context.PriceCorrected
				v.PriceContributed = !v.PriceCorrected &&
					slices.Contains(detail.Context.PriceSources, observationLocation(observation.Observation))
			}

			v.pairSourceFacts(detail.Execution.Terms(), detail.Context.Realized)

			return v
		}
	}

	record, err := workflow.Resource(ctx, "activity", observation.ActivityID)
	if err != nil || record.Activity == nil {
		return v
	}

	instrument, _ := observation.Reported.Field(source.FieldInstrument)
	v.RecordURL = "/activity/" + url.PathEscape(observation.ActivityID) + "#accepted-activity"
	v.RecordLabel = activityLabel(
		record.Activity,
		instrumentTitle(instrument.Fields().Instrument.Ticker(), instrument.Fields().Instrument.Name()),
	)
	v.pairSourceFacts(source.Trade{}, record.Activity.ReportedRealizedPnL)

	return v
}

// A link establishes which record to compare. Only recorded field provenance supports attribution.
func (v *sourceEvidenceEntry) pairSourceFacts(
	trade source.Trade,
	realized *application.PublishedRealizedPnL,
) {
	accepted := trade.Fields()
	instrument, _ := v.Reported.Field(source.FieldInstrument)

	kind, _ := v.Reported.Field(source.FieldTradeKind)
	sameTradeGrain := kind.Known() && string(accepted.Kind) == kind.Fields().Text
	if !kind.Known() && accepted.Kind == source.ExchangeExecution {
		identity := v.Reported.Fields()
		sameTradeGrain = identity.EventDistinct && identity.Event.Kind() == "execution-id" &&
			identity.Event == accepted.Event
	}

	sameInstrumentKind := instrument.Known() &&
		accepted.Instrument.Kind() == instrument.Fields().Instrument.Kind()

	for i := range v.Facts {
		fact := &v.Facts[i]
		reported, _ := v.Reported.Field(fact.Key)

		if !reported.Known() {
			continue
		}

		input := reported.Fields()

		switch fact.Key {
		case source.FieldQuantity:
			if sameTradeGrain && sameInstrumentKind && input.QuantityUnit == trade.QuantityUnit() {
				fact.Accepted = accepted.Quantity.Value() + " " + trade.QuantityUnit()
			}
		case source.FieldGrossAmount:
			if sameTradeGrain && sameInstrumentKind && accepted.GrossAmount.Amount() != "" &&
				input.Money.Currency() == accepted.GrossAmount.Currency() {
				fact.Accepted = moneyText(accepted.GrossAmount)
			}
		case source.FieldDate:
			if sameTradeGrain {
				fact.Accepted = accepted.Date.String()
			}
		case source.FieldRealizedPnL, source.FieldReportedCostBasis:
			amount, on := acceptedRealizedAmount(realized, fact.Key)
			if amount != nil && amount.Amount != "" && input.Money.Currency().String() == amount.Currency {
				if !input.ReportedOn.IsZero() {
					fact.Value = describe(fact.Value, input.ReportedOn.String())
				}

				fact.Accepted = publishedMoney(amount)
				if on != nil && *on != "" {
					fact.Accepted = describe(fact.Accepted, *on)
				}
			}
		}
	}
}

func acceptedRealizedAmount(
	realized *application.PublishedRealizedPnL,
	field source.ActivityFieldName,
) (*application.PublishedMoney, *string) {
	if realized == nil {
		return nil, nil
	}

	if field == source.FieldRealizedPnL {
		return realized.Amount, realized.ReportDate
	}

	return realized.CostBasis, realized.CostBasisReportDate
}

func (v sourceEvidenceEntry) hasPairedFacts() bool {
	return slices.ContainsFunc(v.Facts, func(fact sourceFact) bool { return fact.Accepted != "" })
}

func sourceObservationTitle(v source.ActivityObservation) string {
	instrument, _ := v.Field(source.FieldInstrument)

	i := instrument.Fields().Instrument
	name := i.Ticker()
	if name == "" {
		name = i.Name()
	}

	if name != "" {
		for _, key := range []source.ActivityFieldName{source.FieldSide, source.FieldKind} {
			field, ok := v.Field(key)
			if ok && field.Known() {
				return stringTitle(field.Fields().Text) + " " + name
			}
		}

		return name
	}

	for _, key := range []source.ActivityFieldName{source.FieldDescription, source.FieldReportedType} {
		f, ok := v.Field(key)
		if ok && f.Known() {
			return f.Fields().Text
		}
	}

	f, _ := v.Field(source.FieldFamily)
	if f.Known() {
		return activityKindLabel(f.Fields().Text)
	}

	return "Source entry"
}

func brokerEventLabels(event source.Reference, aliases []source.Reference) []string {
	var labels []string
	for _, reference := range append([]source.Reference{event}, aliases...) {
		switch reference.Kind() {
		case "trade-id":
			labels = append(labels, "Broker trade ID "+reference.Value())
		case "execution-id":
			labels = append(labels, "Execution ID "+reference.Value())
		}
	}

	if len(labels) == 0 && !event.IsZero() {
		labels = append(labels, "Source ID "+event.Value())
	}

	return labels
}

func tradePriceBasis(instrument source.Instrument, basis source.QuotationBasis) string {
	return tradeQuotationLabel(
		instrument.Debt().Fields().Type,
		instrument.Reference().Authority() == "ibkr",
		string(basis),
	)
}

func tradeQuotationLabel(debtType source.DebtType, ibkr bool, basis string) string {
	if ibkr && debtType == source.TreasuryBill {
		return "per 100 face value"
	}

	return quotationLabel(basis)
}

func sourceObservationFacts(v source.ActivityObservation) []sourceFact {
	instrument, _ := v.Field(source.FieldInstrument)
	i := instrument.Fields().Instrument

	grossLabel := "Reported gross amount"
	if i.Kind() == source.Debt {
		grossLabel = "Reported principal"
	}

	var facts []sourceFact
	for _, f := range []struct {
		key   source.ActivityFieldName
		label string
	}{
		{source.FieldInstrument, "Reported instrument"},
		{source.FieldQuantity, "Quantity"},
		{source.FieldPrice, "Reported price"},
		{source.FieldCashAmount, "Cash amount"},
		{source.FieldTransferPrice, "Transfer price"},
		{source.FieldRate, "Quoted rate"},
		{source.FieldRealizedPnL, "Realized gain / loss"},
		{source.FieldReportedCostBasis, "Allocated cost"},
		{source.FieldGrossAmount, grossLabel},
		{source.FieldDate, "Date"},
		{source.FieldReportDate, "Report date"},
	} {
		field, ok := v.Field(f.key)
		if !ok {
			continue
		}

		value := unknownValue
		if field.Known() {
			x := field.Fields()
			switch f.key {
			case source.FieldInstrument:
				value = x.Instrument.Ticker()
				name := x.Instrument.Name()
				if value == "" {
					value = name
				} else if name != "" && name != value {
					value = describe(value, name)
				}
			case source.FieldPrice, source.FieldTransferPrice, source.FieldRate:
				value = priceText(x.Price)
				if f.key == source.FieldPrice {
					if basis := tradePriceBasis(i, x.QuotationBasis); basis != "" {
						value += " " + basis
					}
				}
			case source.FieldCashAmount,
				source.FieldGrossAmount,
				source.FieldRealizedPnL,
				source.FieldReportedCostBasis:
				value = moneyText(x.Money)
			case source.FieldQuantity:
				value = strings.TrimSpace(x.Quantity.Value() + " " + x.QuantityUnit)
			default:
				value = x.Date.String()
			}
		}

		if value != "" {
			facts = append(facts, sourceFact{Key: f.key, Label: f.label, Value: value})
		}
	}

	return facts
}
