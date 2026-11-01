// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"maps"
	"math"
	"slices"
	"strings"

	"go.trippwill.dev/wake/source"
)

type ListOptions struct {
	Search, Kind, State, Sort, Direction string
	Page, PageSize                       int
	KindMatches                          []ActivityKind
	StateMatches                         []ActivityState
	ReferenceMatches                     []ReferenceMatch
	ActivityText                         ActivityText
	DisplayMatches                       DisplayMatches
	ReviewMatches                        []ReviewMatch
	ObservationMatches                   []ObservationOutcome
	ObservationLabels                    map[ObservationOutcome]string
}
type (
	ReviewMatch struct {
		Kind       ReviewKind       `json:"kind"`
		Status     CorrectionStatus `json:"status"`
		CountKnown bool             `json:"count_known"`
		Count      int64            `json:"count"`
	}
	DisplayMatches struct {
		MissingValues      bool
		InapplicableValues bool
	}
	// ActivityText is presentation-owned wording for searchable activity cells.
	// Storage combines it with exact typed values; it never derives policy from it.
	ActivityText struct {
		Unknown            string `json:"unknown"`
		LifecycleSeparator string `json:"lifecycle_separator"`
		ConversionPrefix   string `json:"conversion_prefix"`
		PairSeparator      string `json:"pair_separator"`
		AmountSeparator    string `json:"amount_separator"`
		Sent               string `json:"sent"`
		Received           string `json:"received"`
	}
	ReferenceMatch struct {
		Status    ReferenceStatus
		Authority SelectionAuthority
	}
	ListScope uint8
)

const (
	ActivityScope ListScope = iota
	InstrumentScope
	SourceScope
	ReviewScope
	ObservationScope
)

type ListQuery struct {
	options ListOptions
	scope   ListScope
}

func NewListQuery(options ListOptions, scope ListScope) (ListQuery, error) {
	if options.Page == 0 {
		options.Page = 1
	}

	if options.PageSize == 0 {
		options.PageSize = 50
	}

	if options.Page < 1 || options.PageSize < 1 || options.PageSize > 200 ||
		options.Page > math.MaxInt/options.PageSize ||
		len(options.Search) > 256 {
		return ListQuery{}, ErrInvalid
	}

	if options.Direction == "" {
		options.Direction = "desc"
		if scope != ActivityScope && scope != ObservationScope {
			options.Direction = "asc"
		}
	}

	if options.Direction != "asc" && options.Direction != "desc" {
		return ListQuery{}, ErrInvalid
	}

	sorts := []string{"date", "label", "quantity", "price", "state", "reference"}
	states := []string{"", "accepted", "cancelled", "superseded", "review"}
	kinds := []string{"", "trade", "lifecycle", "conversion"}
	if scope == InstrumentScope {
		sorts = []string{"name", "ticker", "kind", "state"}
		states = []string{
			"",
			"gaps",
			"review",
			"resolved",
			"provisional",
			"no-match",
			"temporary-failure",
			"throttled",
			"ambiguous",
			"conflict",
		}
		kinds = []string{"", "equity", "fund", "option", "debt", "unknown"}
	}

	if scope == SourceScope || scope == ReviewScope {
		sorts = []string{"name"}
		states = []string{""}
		kinds = []string{""}
	}

	if scope == ObservationScope {
		sorts = []string{"report", "outcome", "location", "rule"}
		states = []string{""}
		kinds = []string{""}
	}

	if scope > ObservationScope {
		return ListQuery{}, ErrInvalid
	}

	if options.Sort == "" {
		options.Sort = sorts[0]
	}

	if !slices.Contains(sorts, options.Sort) || !slices.Contains(states, options.State) ||
		!slices.Contains(kinds, options.Kind) {
		return ListQuery{}, ErrInvalid
	}

	for _, kind := range options.KindMatches {
		if scope != ActivityScope && scope != ReviewScope ||
			!slices.Contains([]ActivityKind{TradeActivity, LifecycleActivity, ConversionActivity}, kind) {
			return ListQuery{}, ErrInvalid
		}
	}

	for _, text := range []string{options.ActivityText.LifecycleSeparator, options.ActivityText.ConversionPrefix, options.ActivityText.Unknown, options.ActivityText.PairSeparator, options.ActivityText.AmountSeparator, options.ActivityText.Sent, options.ActivityText.Received} {
		if len(text) > 64 {
			return ListQuery{}, ErrInvalid
		}
	}

	for _, match := range options.ReviewMatches {
		if scope != ReviewScope ||
			!slices.Contains(
				[]ReviewKind{ReviewActivity, ReviewInstrument, ReviewInstruction, ReviewIdentity},
				match.Kind,
			) ||
			match.Status != "" &&
				(match.Kind != ReviewInstruction || match.Status != CorrectionUnresolved && match.Status != CorrectionNeedsReview) ||
			match.CountKnown && (match.Kind != ReviewIdentity || match.Count < 0) {
			return ListQuery{}, ErrInvalid
		}
	}

	for _, outcome := range options.ObservationMatches {
		if scope != ObservationScope || !knownObservationOutcome(outcome) {
			return ListQuery{}, ErrInvalid
		}
	}

	for outcome, label := range options.ObservationLabels {
		if scope != ObservationScope || !knownObservationOutcome(outcome) || strings.TrimSpace(label) == "" ||
			len(label) > 256 {
			return ListQuery{}, ErrInvalid
		}
	}

	options.ObservationLabels = maps.Clone(options.ObservationLabels)

	options.ObservationMatches = slices.Clone(options.ObservationMatches)
	options.ReviewMatches = slices.Clone(options.ReviewMatches)
	options.KindMatches = slices.Clone(options.KindMatches)
	options.Search = strings.ToLower(options.Search)
	options.StateMatches = slices.Clone(options.StateMatches)
	options.ReferenceMatches = slices.Clone(options.ReferenceMatches)

	return ListQuery{options: options, scope: scope}, nil
}
func (q ListQuery) Scope() ListScope { return q.scope }
func (q ListQuery) Options() ListOptions {
	out := q.options
	out.ObservationMatches = slices.Clone(out.ObservationMatches)
	out.ObservationLabels = maps.Clone(out.ObservationLabels)
	out.ReviewMatches = slices.Clone(out.ReviewMatches)
	out.KindMatches = slices.Clone(out.KindMatches)
	out.StateMatches = slices.Clone(out.StateMatches)
	out.ReferenceMatches = slices.Clone(out.ReferenceMatches)

	return out
}
func (q ListQuery) Offset() int64 { return int64((q.options.Page - 1) * q.options.PageSize) }

type (
	PageInfo struct {
		Page, PageSize int
		Total, Matched int64
	}
	Prerequisites struct {
		Storage          StorageStatus
		Account          Account
		ReferenceKey     bool
		ReferenceFailure string
		ConnectionCount  int64
	}
	SourcePage struct {
		Prerequisites
		PageInfo
		Connections []ConnectionView
	}
	AccessSnapshot struct {
		Prerequisites
		Connections []ConnectionView
	}
	SourceDetail struct {
		Prerequisites
		PageInfo
		Connection ConnectionView
	}
	SystemStatus struct {
		Prerequisites
		Connections     []ConnectionView
		MoreConnections bool
	}
	ActivityListItem struct {
		Resource       CanonicalResource
		State          ActivityState
		InstrumentName string
		Reference      ReferenceMatch
	}
	ActivityList struct {
		Account Account
		PageInfo
		Items []ActivityListItem
	}
	InstrumentListItem struct {
		Resource  CanonicalResource
		Authority SelectionAuthority
	}
	InstrumentList struct {
		PageInfo
		Items []InstrumentListItem
	}
	ReviewKind string
)

const (
	ReviewActivity    ReviewKind = "activity"
	ReviewInstrument  ReviewKind = "instrument"
	ReviewInstruction ReviewKind = "instruction"
	ReviewIdentity    ReviewKind = "identity"
)

type (
	ReviewEntry struct {
		Kind                         ReviewKind
		ID, Name, Artifact, Location string
		Activity                     *PublishedActivity
		Status                       CorrectionStatus
		Count                        int64
	}
	ObservationList struct {
		PageInfo
		Items []Observation
	}
	ReviewList struct {
		PageInfo
		Items []ReviewEntry
	}
	OverviewView struct {
		Status                         SystemStatus
		Review                         ReviewList
		ActivityCount, InstrumentCount int64
	}
	ReadQueries interface {
		Prerequisites(context.Context) (Prerequisites, error)
		SourcePage(context.Context, ListQuery) (SourcePage, error)
		SourceAccessPage(context.Context, ListQuery) (SourcePage, error)
		SourceAccess(context.Context) (AccessSnapshot, error)
		SourceDetail(context.Context, string, ListQuery) (SourceDetail, error)
		SourceCandidate(context.Context, string, string, source.Reference) (Candidate, error)
		SystemStatus(context.Context) (SystemStatus, error)
		Overview(context.Context) (OverviewView, error)
		ActivityList(context.Context, ListQuery) (ActivityList, error)
		InstrumentList(context.Context, ListQuery) (InstrumentList, error)
		ReviewList(context.Context, ListQuery) (ReviewList, error)
		ObservationList(context.Context, string, string, ListQuery) (ObservationList, error)
	}
)

func knownObservationOutcome(outcome ObservationOutcome) bool {
	switch outcome {
	case ObservationUnselected,
		ObservationIdentityReview,
		ObservationNativeReview,
		ObservationKindConflict,
		ObservationTermsConflict,
		ObservationCorroborated,
		ObservationAccepted:
		return true
	default:
		return false
	}
}
