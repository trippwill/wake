// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package application owns financial authority, acceptance and operator workflows.
package application

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/source"
)

type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrInvalid      Error = "invalid account details"
	ErrAccountLimit Error = "one account is already activated"
	ErrAccessReview Error = "repair missing connection access and review the reference setting before resuming upstream work"
	ErrCandidate    Error = "account was not discovered through this connection"
)

type AccountDetails struct {
	name     string
	currency financial.Currency
}

func NewAccountDetails(name, currency string) (AccountDetails, error) {
	if name == "" || strings.TrimSpace(name) != name || len(name) > 256 || !utf8.ValidString(name) ||
		strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return AccountDetails{}, ErrInvalid
	}

	c := financial.Currency{}

	var err error
	if currency != "" {
		c, err = financial.ParseCurrency(currency)
		if err != nil {
			return AccountDetails{}, ErrInvalid
		}
	}

	return AccountDetails{name, c}, nil
}
func (d AccountDetails) Name() string { return d.name }
func (d AccountDetails) Currency() string {
	if d.currency == (financial.Currency{}) {
		return ""
	}

	return d.currency.String()
}

type Account struct {
	id, connection, provider, stream string
	rule                             string
	reference                        source.Reference
	details                          AccountDetails
}

func NewAccount(
	id, connection, provider, stream string,
	reference source.Reference,
	details AccountDetails,
	rule string,
) (Account, error) {
	if id == "" || connection == "" || provider == "" || stream == "" || reference.IsZero() ||
		details.name == "" || rule == "" {
		return Account{}, ErrInvalid
	}

	return Account{
		id:         id,
		connection: connection,
		provider:   provider,
		stream:     stream,
		reference:  reference,
		details:    details,
		rule:       rule,
	}, nil
}
func (a Account) ID() string                  { return a.id }
func (a Account) Connection() string          { return a.connection }
func (a Account) Provider() string            { return a.provider }
func (a Account) Stream() string              { return a.stream }
func (a Account) Reference() source.Reference { return a.reference }
func (a Account) Details() AccountDetails     { return a.details }
func (a Account) Rule() string                { return a.rule }

type ReferenceMetadata struct{ Allocation, Reason string }

type Instrument struct {
	metadata                               ReferenceMetadata
	id, name, ticker, kind, figi, evidence string
	status                                 ReferenceStatus
	authority                              SelectionAuthority
	rule                                   string
	reported                               source.Instrument
	revision                               int64
}

func NewInstrument(
	id, name, ticker, kind, status, figi, evidence string,
	reported source.Instrument,
	rule string, revision int64, authority SelectionAuthority, metadata ReferenceMetadata,
) (Instrument, error) {
	if revision < 1 || rule == "" || id == "" || reported.Kind() == "" ||
		(authority != AutomaticSelection && authority != OperatorSelection) ||
		(kind != string(source.Equity) && kind != string(source.Fund) && kind != string(source.Option) && kind != string(source.Debt) && kind != string(source.UnknownInstrument)) {
		return Instrument{}, ErrInvalid
	}

	return Instrument{
		metadata:  metadata,
		revision:  revision,
		id:        id,
		name:      name,
		ticker:    ticker,
		kind:      kind,
		status:    ReferenceStatus(status),
		authority: authority,
		figi:      figi,
		evidence:  evidence,
		reported:  reported,
		rule:      rule,
	}, nil
}
func (i Instrument) ReferenceMetadata() ReferenceMetadata { return i.metadata }
func (i Instrument) ID() string                           { return i.id }
func (i Instrument) Name() string                         { return i.name }
func (i Instrument) Ticker() string                       { return i.ticker }
func (i Instrument) Kind() string                         { return i.kind }
func (i Instrument) Status() ReferenceStatus              { return i.status }
func (i Instrument) Authority() SelectionAuthority        { return i.authority }
func (i Instrument) FIGI() string                         { return i.figi }
func (i Instrument) Evidence() string                     { return i.evidence }
func (i Instrument) Reported() source.Instrument          { return i.reported }
func (i Instrument) Rule() string                         { return i.rule }

type Listing struct{ id, instrument, venue, ticker, currency, figi string }

func NewListing(id, instrument, venue, ticker, currency, figi string) (Listing, error) {
	if id == "" || instrument == "" {
		return Listing{}, ErrInvalid
	}

	if _, err := financial.ParseCurrency(currency); err != nil {
		return Listing{}, ErrInvalid
	}

	return Listing{id, instrument, venue, ticker, currency, figi}, nil
}
func (l Listing) ID() string         { return l.id }
func (l Listing) Instrument() string { return l.instrument }
func (l Listing) Venue() string      { return l.venue }
func (l Listing) Ticker() string     { return l.ticker }
func (l Listing) Currency() string   { return l.currency }
func (l Listing) FIGI() string       { return l.figi }

type Execution struct {
	id, account, instrument, listing, rule string
	state                                  ActivityState
	terms                                  source.Trade
	revision                               int64
}

func NewExecution(
	id, account, instrument, listing, rule string,
	state ActivityState,
	terms source.Trade, revision int64,
) (Execution, error) {
	if revision < 1 || id == "" || account == "" || instrument == "" || rule == "" || state.Lifecycle() == "" {
		return Execution{}, ErrInvalid
	}

	if _, err := source.NewTrade(terms.Fields()); err != nil {
		return Execution{}, err
	}

	return Execution{id, account, instrument, listing, rule, state, terms, revision}, nil
}
func (e Execution) ID() string           { return e.id }
func (e Execution) Account() string      { return e.account }
func (e Execution) Instrument() string   { return e.instrument }
func (e Execution) Listing() string      { return e.listing }
func (e Execution) Status() string       { return string(e.state.Lifecycle()) }
func (e Execution) State() ActivityState { return e.state }
func (e Execution) Rule() string         { return e.rule }
func (e Execution) Terms() source.Trade  { return e.terms }

type (
	ActivityContext struct{ AccountName, InstrumentName string }
	Observation     struct {
		Artifact, Version, Location, Rule, Retrieved string
		Outcome                                      ObservationOutcome
	}
	ExecutionDetail struct {
		Context       ActivityContext
		Execution     Execution
		Relationships []Relationship
		Corrections   []CorrectionReview
	}
	ReferenceAttempt struct{ Evidence, Status, Retrieved, Rule string }
	InstrumentDetail struct {
		Instrument Instrument
		Underlier  string
		Listings   []Listing
		References []ReferenceAttempt
	}
	ReferenceEvidence struct{ Retrieved, Request, Response string }
)

// Lifecycle retains a reported outcome independently of any stock booking.
type Lifecycle struct {
	id, account, instrument, rule string
	state                         ActivityState
	terms                         source.Lifecycle
	revision                      int64
}

func NewLifecycle(
	id, account, instrument, rule string,
	state ActivityState,
	terms source.Lifecycle,
	revision int64,
) (Lifecycle, error) {
	if revision < 1 || id == "" || account == "" || instrument == "" || rule == "" || state.Lifecycle() == "" {
		return Lifecycle{}, ErrInvalid
	}

	if _, err := source.NewLifecycle(terms.Fields()); err != nil {
		return Lifecycle{}, err
	}

	return Lifecycle{id, account, instrument, rule, state, terms, revision}, nil
}
func (l Lifecycle) ID() string              { return l.id }
func (l Lifecycle) Account() string         { return l.account }
func (l Lifecycle) Instrument() string      { return l.instrument }
func (l Lifecycle) Status() string          { return string(l.state.Lifecycle()) }
func (l Lifecycle) State() ActivityState    { return l.state }
func (l Lifecycle) Terms() source.Lifecycle { return l.terms }

type (
	Relationship    struct{ Kind, Target, TargetID string }
	LifecycleDetail struct {
		Context       ActivityContext
		Lifecycle     Lifecycle
		Corrections   []CorrectionReview
		Relationships []Relationship
	}
)

type Conversion struct {
	id, account, rule string
	state             ActivityState
	terms             source.Conversion
	revision          int64
}

func NewConversion(
	id, account, rule string,
	state ActivityState,
	terms source.Conversion,
	revision int64,
) (Conversion, error) {
	if revision < 1 || id == "" || account == "" || rule == "" || state.Lifecycle() == "" {
		return Conversion{}, ErrInvalid
	}

	if _, err := source.NewConversion(terms.Fields()); err != nil {
		return Conversion{}, err
	}

	return Conversion{id, account, rule, state, terms, revision}, nil
}
func (c Conversion) ID() string               { return c.id }
func (c Conversion) Account() string          { return c.account }
func (c Conversion) Status() string           { return string(c.state.Lifecycle()) }
func (c Conversion) State() ActivityState     { return c.state }
func (c Conversion) Terms() source.Conversion { return c.terms }

type ConversionDetail struct {
	Context     ActivityContext
	Conversion  Conversion
	Corrections []CorrectionReview
}

func (i Instrument) Revision() int64 { return i.revision }
func (e Execution) Revision() int64  { return e.revision }
func (l Lifecycle) Revision() int64  { return l.revision }
func (c Conversion) Revision() int64 { return c.revision }

type CorrectionReview struct {
	Artifact, Version, Location, Kind, Target, Replacement string
	Status                                                 CorrectionStatus
	TargetIDs, ReplacementIDs                              []string
}
