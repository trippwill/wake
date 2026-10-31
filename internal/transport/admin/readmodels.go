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
	notApplicableValue        = "Not applicable"
	unknownValue              = "Unknown"
	conversionPairSeparator   = " → "
	conversionLabelPrefix     = "Convert "
	conversionAmountSeparator = " · "
	conversionSent            = "sent"
	conversionReceived        = "received"
)

func conversionSearchText() application.ConversionText {
	return application.ConversionText{
		Unknown:         unknownValue,
		LabelPrefix:     conversionLabelPrefix,
		PairSeparator:   conversionPairSeparator,
		AmountSeparator: conversionAmountSeparator,
		Sent:            conversionSent,
		Received:        conversionReceived,
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
		Recovery              []application.RecoveryJob
		MoreConnections       bool
		MissingRecoveryAccess bool
	}
	overviewView struct {
		sourceView
		Reviews                        []reviewItem
		ReviewPage                     application.PageInfo
		ActivityCount, InstrumentCount int64
	}
	instrumentItem struct {
		ID, Name, Ticker, Kind string
		Status                 application.ReferenceStatus
		Authority              application.SelectionAuthority
	}
	activityView struct {
		Account application.Account
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
		Recovery:        v.Recovery,
		MoreConnections: v.MoreConnections,
	}
}

func overview(v application.OverviewView) overviewView {
	return overviewView{
		sourceView:      statusView(v.Status),
		Reviews:         reviewItems(v.Review),
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

func referenceLabel(status application.ReferenceStatus, authority application.SelectionAuthority) string {
	label := string(status)
	if authority == application.OperatorSelection {
		label += "; operator decision"
	}

	return label
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
	out := activityView{Account: v.Account, PageInfo: v.PageInfo}
	for _, item := range v.Items {
		r := item.Resource

		a := r.Activity
		name := known(item.InstrumentName)
		row := activityItem{
			ID:             r.ID,
			Href:           "/activity/" + r.ID,
			Date:           a.Date,
			Kind:           a.Family,
			Instrument:     name,
			InstrumentID:   text(a.InstrumentID),
			Reference:      referenceLabel(item.Reference.Status, item.Reference.Authority),
			State:          activityStatus(item.State),
			FinancialState: item.State,
		}
		switch a.Family {
		case "trade":
			row.Href = "/executions/" + r.ID
			row.Label = activityLabel(a, name)
			row.Quantity = known("")
			if a.Quantity != nil {
				row.Quantity = a.Quantity.Value + " " + a.Quantity.Unit
			}

			row.Price = publishedMoney(a.Price)
			row.Basis = text(a.PriceBasis)
		case "lifecycle":
			row.Label = activityLabel(a, name)
			row.Price = notApplicableValue
			if a.Quantity != nil {
				row.Quantity = a.Quantity.Value + " " + a.Quantity.Unit
			}
		case "conversion":
			row.Label = activityLabel(a, name)
			row.Quantity = conversionQuantity(publishedAmount(a.Sent), publishedAmount(a.Received))
			row.Price = publishedMoney(a.Rate)
			if a.Rate != nil {
				row.Basis = "per " + known(text(a.RateBase))
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
				Name:   known(text(i.Name)),
				Ticker: known(text(i.Ticker)),
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
			if r.Activity != nil {
				item.Label = activityLabel(r.Activity, known(r.Name))
			}

			item.Href = "/activity/" + r.ID + "/correct?mode=evidence"
		case application.ReviewInstrument:
			item.Href = "/instruments/" + r.ID + "/correct?mode=evidence"
		case application.ReviewInstruction:
			item.Label = stringTitle(r.Name) + " instruction"
			item.Href = "/evidence/" + r.Artifact + "?location=" + url.QueryEscape(r.Location)
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

	if scope == application.SourceScope || scope == application.ReviewScope {
		options.Kind = ""
		options.State = ""
		options.Sort = ""
		options.Direction = ""
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
			options.LabelSearch = search
			for _, action := range []string{"assignment", "exercise", "expiration"} {
				if name, ok := strings.CutPrefix(search, action+" · "); ok {
					options.LabelSearch = action + " " + name
				}
			}

			for _, action := range []string{"buy", "sell", "assignment", "exercise", "expiration"} {
				if options.LabelSearch == action+" "+strings.ToLower(known("")) {
					options.DisplayMatches.MissingInstrumentAction = action
				}
			}

			options.ConversionText = conversionSearchText()

			for kind, label := range map[application.ActivityKind]string{application.TradeActivity: "Trade", application.LifecycleActivity: "Option outcome", application.ConversionActivity: "Convert"} {
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

func listResult(p application.PageInfo) string {
	return strconv.FormatInt(p.Matched, 10) + " of " + strconv.FormatInt(p.Total, 10) + " results"
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
	if kind == "lifecycle" {
		return "Option outcome"
	}

	return stringTitle(kind)
}

func activityLabel(a *application.PublishedActivity, name string) string {
	switch a.Family {
	case "trade":
		return stringTitle(text(a.Side)) + " " + name
	case "lifecycle":
		return stringTitle(text(a.LifecycleKind)) + " · " + name
	case "conversion":
		sent, received := unknownValue, unknownValue
		if a.Sent != nil {
			sent = known(a.Sent.Currency)
		}

		if a.Received != nil {
			received = known(a.Received.Currency)
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
