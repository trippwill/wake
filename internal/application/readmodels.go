// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/source"
)

// ReportTitle names a report stream in the reader and its searchable index.
func ReportTitle(stream string) string {
	switch stream {
	case "confirmations":
		return "Trade confirmations"
	case "activity", "activities":
		return "Activity report"
	case "positions":
		return "Positions report"
	case "":
		return "Source report"
	default:
		return strings.ToUpper(stream[:1]) + strings.ReplaceAll(stream[1:], "-", " ") + " report"
	}
}

type ListOptions struct {
	Search, Kind, State, Sort, Direction string
	Page, PageSize                       int
	AccountID                            string
	KindMatches                          []calculation.ActivityKind
	StateMatches                         []calculation.ActivityState
	ReferenceMatches                     []ReferenceMatch
	ActivityText                         ActivityText
	DisplayMatches                       DisplayMatches
	ReviewMatches                        []ReviewMatch
	ObservationMatches                   []calculation.ObservationOutcome
	ObservationLabels                    map[calculation.ObservationOutcome]string
}
type (
	ReviewMatch struct {
		Kind       ReviewKind                   `json:"kind"`
		Status     calculation.CorrectionStatus `json:"status"`
		CountKnown bool                         `json:"count_known"`
		Count      int64                        `json:"count"`
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
		MovementSeparator  string `json:"movement_separator"`
		TransferPrefix     string `json:"transfer_prefix"`
		ConversionPrefix   string `json:"conversion_prefix"`
		PairSeparator      string `json:"pair_separator"`
		AmountSeparator    string `json:"amount_separator"`
		Sent               string `json:"sent"`
		Received           string `json:"received"`
	}
	ReferenceMatch struct {
		Status    calculation.ReferenceStatus
		Authority calculation.SelectionAuthority
	}
	ListScope uint8
)

const (
	ActivityScope ListScope = iota
	InstrumentScope
	SourceScope
	ReviewScope
	ObservationScope
	PositionsScope
	ReportsScope
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
		len(options.Search) > 256 || len(options.AccountID) > 256 {
		return ListQuery{}, calculation.ErrInvalid
	}

	if options.Direction == "" {
		options.Direction = "desc"
		if scope != ActivityScope && scope != ObservationScope && scope != ReportsScope {
			options.Direction = "asc"
		}
	}

	if options.Direction != "asc" && options.Direction != "desc" ||
		scope == ReportsScope && options.Direction != "desc" {
		return ListQuery{}, calculation.ErrInvalid
	}

	sorts := []string{"date", "label", "quantity", "price", "amount", "realized", "state", "reference"}
	states := []string{"", "accepted", "cancelled", "superseded", "review"}
	kinds := []string{"", "trade", "lifecycle", "conversion", "security-transfer", "cash"}
	if scope == InstrumentScope {
		sorts = []string{"ticker", "name", "kind", "state"}
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
		sorts = []string{"report", "outcome", "location", "rule", "activity", "date"}
		states = []string{""}
		kinds = []string{""}
	}

	if scope == PositionsScope {
		sorts = []string{"name", "side", "quantity", "cost"}
		states = []string{"", "long", "short"}
		kinds = []string{"", "equity", "fund", "option", "debt"}
	}

	if scope == ReportsScope {
		sorts = []string{"received"}
		states = []string{""}
		kinds = []string{""}
	}

	if scope > ReportsScope {
		return ListQuery{}, calculation.ErrInvalid
	}

	if options.Sort == "" {
		options.Sort = sorts[0]
	}

	if !slices.Contains(sorts, options.Sort) || !slices.Contains(states, options.State) ||
		!slices.Contains(kinds, options.Kind) {
		return ListQuery{}, calculation.ErrInvalid
	}

	for _, kind := range options.KindMatches {
		if scope != ActivityScope && scope != ReviewScope ||
			!slices.Contains(
				[]calculation.ActivityKind{
					calculation.TradeActivity,
					calculation.LifecycleActivity,
					calculation.ConversionActivity,
					calculation.SecurityTransferActivity,
					calculation.CashActivity,
				},
				kind,
			) {
			return ListQuery{}, calculation.ErrInvalid
		}
	}

	for _, text := range []string{options.ActivityText.LifecycleSeparator, options.ActivityText.ConversionPrefix, options.ActivityText.Unknown, options.ActivityText.PairSeparator, options.ActivityText.AmountSeparator, options.ActivityText.Sent, options.ActivityText.Received} {
		if len(text) > 64 {
			return ListQuery{}, calculation.ErrInvalid
		}
	}

	for _, match := range options.ReviewMatches {
		if scope != ReviewScope ||
			!slices.Contains(
				[]ReviewKind{ReviewActivity, ReviewInstrument, ReviewInstruction, ReviewIdentity},
				match.Kind,
			) ||
			match.Status != "" &&
				(match.Kind != ReviewInstruction || match.Status != calculation.CorrectionUnresolved && match.Status != calculation.CorrectionNeedsReview) ||
			match.CountKnown && (match.Kind != ReviewIdentity || match.Count < 0) {
			return ListQuery{}, calculation.ErrInvalid
		}
	}

	for _, outcome := range options.ObservationMatches {
		if scope != ObservationScope || !knownObservationOutcome(outcome) {
			return ListQuery{}, calculation.ErrInvalid
		}
	}

	for outcome, label := range options.ObservationLabels {
		if scope != ObservationScope || !knownObservationOutcome(outcome) || strings.TrimSpace(label) == "" ||
			len(label) > 256 {
			return ListQuery{}, calculation.ErrInvalid
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
		AccountCount     int64
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
	HistoryUpdateStatus struct {
		Rule, Phase string
		Completed   int64
		RetryAt     time.Time
	}
	SystemStatus struct {
		Prerequisites
		Connections     []ConnectionView
		MoreConnections bool
		HistoryUpdate   HistoryUpdateStatus
	}
	ActivityListItem struct {
		Resource                         CanonicalResource
		State                            calculation.ActivityState
		InstrumentName, InstrumentTicker string
		AccountName, AccountProvider     string
		Reference                        ReferenceMatch
	}
	ActivityList struct {
		PageInfo
		Items []ActivityListItem
	}
	InstrumentListItem struct {
		Resource  CanonicalResource
		Authority calculation.SelectionAuthority
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

type ReportProcessing uint8

const (
	ReportProcessingUnavailable ReportProcessing = iota
	ReportProcessingPending
	ReportProcessingFailed
	ReportProcessingComplete
)

type (
	ReviewEntry struct {
		Kind                                 ReviewKind
		ID, Name, Ticker, Artifact, Location string
		Activity                             *PublishedActivity
		Status                               calculation.CorrectionStatus
		Count                                int64
	}
	ObservationList struct {
		PageInfo
		Items      []Observation
		Processing ReportProcessing
	}
	ReviewList struct {
		PageInfo
		Items []ReviewEntry
	}
	OverviewView struct {
		Status                         SystemStatus
		Review                         ReviewList
		Reports                        []ReportEntry
		PendingReport                  PendingReport
		PositionsAsOf                  *PositionTime
		PositionsAvailable             int64
		ActivityCount, InstrumentCount int64
		CanRequestSync                 bool
	}
	ReportEntry struct {
		ProcessingStatus
		ConnectionID, ConnectionName, Provider, Operation string
		Received                                          time.Time
		ProviderAvailable, CanNormalize, ReusedBody       bool
		Accepted, Enriched, Repeated, PositionSets        int64
	}
	ReportList struct {
		PageInfo
		Items []ReportEntry
	}
	PendingReport struct {
		Artifact, ConnectionID, ConnectionName string
		AccountConfirmed                       bool
		ProcessingFailed                       bool
	}
	ReadQueries interface {
		Resource(context.Context, string, string) (CanonicalResource, error)
		Prerequisites(context.Context) (Prerequisites, error)
		SourcePage(context.Context, ListQuery) (SourcePage, error)
		SourceAccessPage(context.Context, ListQuery) (SourcePage, error)
		SourceAccess(context.Context) (AccessSnapshot, error)
		SourceDetail(context.Context, string, ListQuery) (SourceDetail, error)
		SourceCandidate(context.Context, string, string, source.Reference) (Candidate, error)
		SystemStatus(context.Context) (SystemStatus, error)
		Overview(context.Context) (OverviewView, error)
		ReportList(context.Context, ListQuery) (ReportList, error)
		Accounts(context.Context) ([]Account, error)
		ActivityList(context.Context, ListQuery) (ActivityList, error)
		InstrumentList(context.Context, ListQuery) (InstrumentList, error)
		PositionsList(context.Context, ListQuery) (PositionsList, error)
		ReviewList(context.Context, ListQuery) (ReviewList, error)
		ObservationList(context.Context, string, string, ListQuery) (ObservationList, error)
	}
)

func knownObservationOutcome(outcome calculation.ObservationOutcome) bool {
	switch outcome {
	case calculation.ObservationUnselected,
		calculation.ObservationIncomplete,
		calculation.ObservationIncompleteConflict,
		calculation.ObservationIdentityReview,
		calculation.ObservationNativeReview,
		calculation.ObservationKindConflict,
		calculation.ObservationTermsConflict,
		calculation.ObservationEnriched,
		calculation.ObservationNoContribution,
		calculation.ObservationAccepted:
		return true
	default:
		return false
	}
}

type (
	DecisionRecord struct {
		ID, Scope, Text, Currency, Action, Actor, At, Rationale, Supersedes string
		Active                                                              bool
	}
	CorrectionValue     struct{ Value, Currency, Unit, Label string }
	CorrectionCandidate struct {
		Value, Label, Scope string
		Fields              map[string]CorrectionValue
	}
	CorrectionDraft struct{ Scope, Value, Currency, Rationale, Action string }

	CorrectionForm struct {
		Links           []CorrectionCandidate
		Kind, ID, Label string
		Revision        int64
		Values          map[string]CorrectionValue
		AccountName     string
		Draft           CorrectionDraft
		Message         string
		Stale           bool
		Fields          []string
		Candidates      []CorrectionCandidate
		Decisions       []DecisionRecord
	}
)
