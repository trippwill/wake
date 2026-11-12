// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package admin

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"go.trippwill.dev/wake/internal/application"
)

const (
	lifecycleLabelSeparator   = " · "
	movementLabelSeparator    = " · "
	transferLabelPrefix       = "Transfer "
	notApplicableValue        = "Not applicable"
	unknownValue              = "Unknown"
	conversionPairSeparator   = " → "
	conversionLabelPrefix     = "Convert "
	conversionAmountSeparator = " · "
	conversionSent            = "sent"
	conversionReceived        = "received"
)

func activitySearchText() application.ActivityText {
	return application.ActivityText{
		Unknown:            unknownValue,
		LifecycleSeparator: lifecycleLabelSeparator,
		MovementSeparator:  movementLabelSeparator,
		TransferPrefix:     transferLabelPrefix,
		ConversionPrefix:   conversionLabelPrefix,
		PairSeparator:      conversionPairSeparator,
		AmountSeparator:    conversionAmountSeparator,
		Sent:               conversionSent,
		Received:           conversionReceived,
	}
}

func conversionQuantity(sent, received string) string {
	return sent + " " + conversionSent + conversionAmountSeparator + received + " " + conversionReceived
}

type (
	sourceView struct {
		application.Prerequisites
		application.PageInfo
		Connections           []application.ConnectionView
		MoreConnections       bool
		MissingRecoveryAccess bool
	}
	overviewView struct {
		sourceView
		Reviews                        []reviewItem
		HistoryUpdate                  application.HistoryUpdateStatus
		ReviewPage                     application.PageInfo
		ActivityCount, InstrumentCount int64
	}
	instrumentItem struct {
		ID, Name, Ticker, Kind string
		Status                 application.ReferenceStatus
		Authority              application.SelectionAuthority
	}
	activityView struct {
		application.PageInfo
		Items       []activityItem
		Instruments []instrumentItem
		Reviews     []reviewItem
	}
)

func statusView(v application.SystemStatus) sourceView {
	return sourceView{
		Prerequisites:   v.Prerequisites,
		Connections:     v.Connections,
		MoreConnections: v.MoreConnections,
	}
}

func overview(v application.OverviewView) overviewView {
	return overviewView{
		sourceView:      statusView(v.Status),
		Reviews:         reviewItems(v.Review),
		HistoryUpdate:   v.Status.HistoryUpdate,
		ReviewPage:      v.Review.PageInfo,
		ActivityCount:   v.ActivityCount,
		InstrumentCount: v.InstrumentCount,
	}
}

func sourcePage(v application.SourcePage) sourceView {
	return sourceView{Prerequisites: v.Prerequisites, PageInfo: v.PageInfo, Connections: v.Connections}
}

func sourceDetail(v application.SourceDetail) sourceView {
	return sourceView{
		Prerequisites: v.Prerequisites,
		PageInfo:      v.PageInfo,
		Connections:   []application.ConnectionView{v.Connection},
	}
}

func text(p *string) string {
	if p == nil {
		return ""
	}

	return *p
}

func instrumentTitle(ticker, name string) string {
	if ticker != "" {
		return ticker
	}

	return known(name)
}

func instrumentDescription(ticker, name string) string {
	if name == instrumentTitle(ticker, name) {
		return ""
	}

	return name
}

func describe(description, context string) string {
	if description == "" {
		return context
	}

	return description + " · " + context
}

func referenceLabel(status application.ReferenceStatus, authority application.SelectionAuthority) string {
	label := string(status)
	if authority == application.OperatorSelection {
		label += "; operator decision"
	}

	return label
}

func quotationLabel(basis string) string {
	switch basis {
	case "per_share":
		return "per share"
	case "per_underlying_unit":
		return "premium per underlying unit"
	default:
		return ""
	}
}

func publishedMoney(v *application.PublishedMoney) string {
	if v == nil {
		return unknownValue
	}

	return v.Amount + " " + v.Currency
}

func publishedAmount(v *application.PublishedAmount) string {
	if v == nil {
		return unknownValue
	}

	return known(text(v.Amount)) + " " + known(v.Currency)
}

func activityList(v application.ActivityList) activityView {
	out := activityView{PageInfo: v.PageInfo}
	for _, item := range v.Items {
		r := item.Resource

		a := r.Activity
		name := instrumentTitle(item.InstrumentTicker, item.InstrumentName)

		row := activityItem{
			ID:             r.ID,
			Href:           "/activity/" + r.ID,
			Date:           a.Date,
			AccountName:    item.AccountName,
			Kind:           a.Family,
			Instrument:     name,
			Description:    instrumentDescription(item.InstrumentTicker, item.InstrumentName),
			InstrumentID:   text(a.InstrumentID()),
			Reference:      referenceLabel(item.Reference.Status, item.Reference.Authority),
			State:          activityStatus(item.State),
			FinancialState: item.State,
			Realized:       known(""),
			Amount:         notApplicableValue,
		}
		if a.ReportedRealizedPnL != nil {
			row.Realized = publishedMoney(a.ReportedRealizedPnL.Amount)
			row.RealizedConflict = a.ReportedRealizedPnL.Conflict
		}

		switch a.Family {
		case "cash":
			row.Label = activityLabel(a, name)
			row.Amount = publishedMoney(&a.Cash.Amount)
			row.Quantity, row.Price = notApplicableValue, notApplicableValue
			if a.ReportedRealizedPnL == nil {
				row.Realized = notApplicableValue
			}
		case "trade":
			row.Href = "/executions/" + r.ID
			row.Label = activityLabel(a, name)
			row.Quantity = known("")
			if a.Trade.Quantity != nil {
				row.Quantity = a.Trade.Quantity.Value + " " + a.Trade.Quantity.Unit
			}

			row.Price = publishedMoney(a.Trade.Price)
			row.Basis = quotationLabel(text(a.Trade.QuotationBasis))
		case "security-transfer":
			row.Label = activityLabel(a, name)
			if a.Transfer.Quantity != nil {
				row.Quantity = a.Transfer.Quantity.Value + " " + a.Transfer.Quantity.Unit
			}

			row.Price = publishedMoney(a.Transfer.Price)
			row.Basis = "reported transfer price"
		case "lifecycle":
			row.Label = activityLabel(a, name)
			row.Price = notApplicableValue
			if a.Lifecycle.Quantity != nil {
				row.Quantity = a.Lifecycle.Quantity.Value + " " + a.Lifecycle.Quantity.Unit
			}
		case "conversion":
			row.Label = activityLabel(a, name)
			row.Quantity = conversionQuantity(
				publishedAmount(a.Conversion.Sent),
				publishedAmount(a.Conversion.Received),
			)
			row.Price = publishedMoney(a.Conversion.Rate)
			if a.Conversion.Rate != nil {
				row.Basis = "per " + known(text(a.Conversion.RateBase))
			}
		}

		out.Items = append(out.Items, row)
	}

	return out
}

func instrumentList(v application.InstrumentList) activityView {
	out := activityView{PageInfo: v.PageInfo}
	for _, item := range v.Items {
		i := item.Resource.Instrument
		out.Instruments = append(
			out.Instruments,
			instrumentItem{
				ID:     item.Resource.ID,
				Name:   text(i.Name),
				Ticker: text(i.Ticker),
				Kind:   i.Family,
				Status: application.ReferenceStatus(i.ReferenceStatus), Authority: item.Authority,
			},
		)
	}

	return out
}

func reviewItems(v application.ReviewList) []reviewItem {
	out := make([]reviewItem, 0, len(v.Items))
	for _, r := range v.Items {
		kind, reason := reviewText(r)
		item := reviewItem{Key: string(r.Kind) + "/" + r.ID, Label: known(r.Name), Kind: kind, Reason: reason}
		switch r.Kind {
		case application.ReviewActivity:
			item.Description = instrumentDescription(r.Ticker, r.Name)
			if r.Activity != nil {
				item.Label = activityLabel(r.Activity, instrumentTitle(r.Ticker, r.Name))
			}

			item.Href = "/activity/" + r.ID + "/correct?mode=evidence"
		case application.ReviewInstrument:
			item.Label = instrumentTitle(r.Ticker, r.Name)
			item.Description = instrumentDescription(r.Ticker, r.Name)
			item.Href = "/instruments/" + r.ID + "/correct?mode=evidence"
		case application.ReviewInstruction:
			item.Label = stringTitle(r.Name) + " instruction"
			item.Href = "/evidence/" + r.Artifact
		case application.ReviewIdentity:
			item.Href = "/evidence/" + r.Artifact
		}

		out = append(out, item)
	}

	return out
}

func listQuery(ctx context.Context, scope application.ListScope) (application.ListQuery, error) {
	params := page(ctx).Query
	options := application.ListOptions{
		Search:    params.Get("q"),
		Kind:      params.Get("kind"),
		State:     params.Get("state"),
		Sort:      params.Get("sort"),
		Direction: params.Get("dir"),
	}
	if value := params.Get("page"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil {
			return application.ListQuery{}, application.ErrInvalid
		}

		options.Page = n
	}

	if value := params.Get("size"); value != "" {
		size, err := strconv.Atoi(value)
		if err != nil {
			return application.ListQuery{}, application.ErrInvalid
		}

		options.PageSize = size
	}

	if scope == application.SourceScope || scope == application.ReviewScope {
		options.Kind = ""
		options.State = ""
		options.Sort = ""
		options.Direction = ""
	}

	if scope == application.PositionsScope || scope == application.ActivityScope {
		options.AccountID = params.Get("account")
	}

	if scope == application.PositionsScope {
		return application.NewListQuery(options, scope)
	}

	if scope == application.ObservationScope {
		options.Kind = ""
		options.State = ""
		options.ObservationLabels = map[application.ObservationOutcome]string{}
		for _, outcome := range []application.ObservationOutcome{application.ObservationUnselected, application.ObservationIncomplete, application.ObservationIncompleteConflict, application.ObservationIdentityReview, application.ObservationNativeReview, application.ObservationKindConflict, application.ObservationTermsConflict, application.ObservationEnriched, application.ObservationNoContribution, application.ObservationAccepted} {
			options.ObservationLabels[outcome] = observationStatus(outcome)
			if strings.Contains(strings.ToLower(observationStatus(outcome)), strings.ToLower(options.Search)) {
				options.ObservationMatches = append(options.ObservationMatches, outcome)
			}
		}
	}

	search := strings.ToLower(options.Search)
	if search != "" {
		if scope == application.ReviewScope {
			options.ReviewMatches = reviewSearchMatches(search)
		}

		if scope == application.ActivityScope || scope == application.InstrumentScope ||
			scope == application.ReviewScope {
			options.DisplayMatches.MissingValues = strings.Contains(strings.ToLower(known("")), search)
		}

		if scope == application.ActivityScope {
			options.DisplayMatches.InapplicableValues = strings.Contains(
				strings.ToLower(notApplicableValue),
				search,
			)
		}

		if scope == application.ActivityScope || scope == application.ReviewScope {
			options.ActivityText = activitySearchText()

			for kind, label := range map[application.ActivityKind]string{application.CashActivity: "Cash", application.SecurityTransferActivity: "Security transfer", application.TradeActivity: "Trade", application.LifecycleActivity: "Option outcome", application.ConversionActivity: "Convert"} {
				if strings.Contains(strings.ToLower(label), search) ||
					strings.Contains(strings.ToLower(activityKindLabel(string(kind))), search) {
					options.KindMatches = append(options.KindMatches, kind)
				}
			}
		}

		for _, lifecycle := range []application.LifecycleState{application.Accepted, application.Cancelled, application.Superseded} {
			for reasons := application.ReviewReasons(0); reasons <= application.ConflictingClaims|application.AmbiguousCorrection; reasons++ {
				for _, authority := range []application.SelectionAuthority{application.AutomaticSelection, application.OperatorSelection} {
					state, err := application.NewActivityState(lifecycle, reasons, authority)
					if err == nil && strings.Contains(activityStatus(state), search) {
						options.StateMatches = append(options.StateMatches, state)
					}
				}
			}
		}

		for _, status := range []application.ReferenceStatus{"resolved", "provisional", "no-match", "temporary-failure", "throttled", "ambiguous", "conflict"} {
			for _, authority := range []application.SelectionAuthority{application.AutomaticSelection, application.OperatorSelection} {
				if strings.Contains(referenceLabel(status, authority), search) {
					options.ReferenceMatches = append(
						options.ReferenceMatches,
						application.ReferenceMatch{Status: status, Authority: authority},
					)
				}
			}
		}
	}

	return application.NewListQuery(options, scope)
}

func pageURL(ctx context.Context, n int) string {
	state := page(ctx)
	query := url.Values{}
	for key, values := range state.Query {
		query[key] = append([]string(nil), values...)
	}

	query.Set("page", strconv.Itoa(n))

	path, _, _ := strings.Cut(state.URL, "?")

	return path + "?" + query.Encode()
}
func pageHasNext(p application.PageInfo) bool { return int64(p.Page)*int64(p.PageSize) < p.Matched }

func activityKindLabel(kind string) string {
	if kind == "security-transfer" {
		return "Security transfer"
	}

	if kind == "lifecycle" {
		return "Option outcome"
	}

	return stringTitle(kind)
}

func activityLabel(a *application.PublishedActivity, name string) string {
	switch a.Family {
	case "cash":
		if a.Cash != nil {
			return stringTitle(
				strings.ReplaceAll(a.Cash.Category, "-", " "),
			) + movementLabelSeparator + known(
				text(a.Cash.Description),
			)
		}
	case "trade":
		return stringTitle(text(a.Trade.Side)) + " " + name
	case "security-transfer":
		if a.Transfer != nil {
			return transferLabelPrefix + a.Transfer.Direction + movementLabelSeparator + name
		}
	case "lifecycle":
		return stringTitle(text(a.Lifecycle.LifecycleKind)) + lifecycleLabelSeparator + name
	case "conversion":
		sent, received := unknownValue, unknownValue
		if a.Conversion.Sent != nil {
			sent = known(a.Conversion.Sent.Currency)
		}

		if a.Conversion.Received != nil {
			received = known(a.Conversion.Received.Currency)
		}

		return conversionLabelPrefix + sent + conversionPairSeparator + received
	}

	return name
}

func reviewText(r application.ReviewEntry) (string, string) {
	switch r.Kind {
	case application.ReviewActivity:
		return "Activity", "Reported values need review"
	case application.ReviewInstrument:
		return "Identity", "Instrument identity needs review"
	case application.ReviewInstruction:
		return "Source instruction", correctionStatus(r.Status)
	case application.ReviewIdentity:
		return "Source claims", strconv.FormatInt(r.Count, 10) + " source claims have unresolved identity."
	}

	return "", ""
}

func reviewSearchMatches(search string) []application.ReviewMatch {
	var matches []application.ReviewMatch
	for _, kind := range []application.ReviewKind{application.ReviewActivity, application.ReviewInstrument, application.ReviewIdentity, application.ReviewInstruction} {
		statuses := []application.CorrectionStatus{""}
		if kind == application.ReviewInstruction {
			statuses = []application.CorrectionStatus{
				application.CorrectionUnresolved,
				application.CorrectionNeedsReview,
			}
		}

		for _, status := range statuses {
			category, reason := reviewText(application.ReviewEntry{Kind: kind, Status: status})
			if kind == application.ReviewIdentity {
				reason = strings.TrimPrefix(reason, "0 ")
			}

			if strings.Contains(strings.ToLower(category), search) ||
				strings.Contains(strings.ToLower(reason), search) {
				matches = append(matches, application.ReviewMatch{Kind: kind, Status: status})
			}
		}

		if kind == application.ReviewIdentity {
			number, _, _ := strings.Cut(search, " ")
			if count, err := strconv.ParseInt(number, 10, 64); err == nil && count >= 0 {
				_, reason := reviewText(application.ReviewEntry{Kind: kind, Count: count})
				if strings.Contains(strings.ToLower(reason), search) {
					matches = append(matches, application.ReviewMatch{Kind: kind, CountKnown: true, Count: count})
				}
			}
		}
	}

	return matches
}

func movementSettlementDate(a *application.PublishedActivity) *string {
	if a.Cash != nil {
		return a.Cash.SettlementDate
	}

	if a.Transfer != nil {
		return a.Transfer.SettlementDate
	}

	return nil
}

func fieldDetailLabel(path string) string {
	return stringTitle(strings.NewReplacer("_", " ", "/", " / ").Replace(strings.TrimPrefix(path, "/")))
}

func fieldDetailText(field application.PublishedFieldDetail) string {
	switch field.Reason {
	case application.FieldNotApplicable:
		return notApplicableValue
	case application.FieldReportedUnavailable:
		return "Reported as unavailable"
	case application.FieldUnresolved:
		return "Unavailable because source claims could not be reconciled"
	case application.FieldOperatorWithheld:
		return "Withheld by operator selection"
	}

	if field.Expectation != nil {
		switch string(*field.Expectation) {
		case "not_expected":
			return "Not expected from this source"
		case "expected":
			return "Expected but not provided"
		}
	}

	return "Not provided"
}
