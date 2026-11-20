// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package application owns financial authority, acceptance and operator workflows.
package application

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/internal/application/calculation"
	"go.trippwill.dev/wake/source"
)

const (
	ErrAccountLimit      calculation.Error = "one account is already activated on this connection"
	ErrAccountBound      calculation.Error = "the source account is already bound to a connection"
	ErrAccessReview      calculation.Error = "repair missing connection access and review the reference setting before resuming upstream work"
	ErrCandidate         calculation.Error = "account was not discovered through this connection"
	ErrConnectionChanged calculation.Error = "connection settings changed; retry confirmation"
)

type AccountDetails struct {
	name     string
	currency financial.Currency
}

func NewAccountDetails(name, currency string) (AccountDetails, error) {
	if name == "" || strings.TrimSpace(name) != name || len(name) > 256 || !utf8.ValidString(name) ||
		strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return AccountDetails{}, calculation.ErrInvalid
	}

	c := financial.Currency{}

	var err error
	if currency != "" {
		c, err = financial.ParseCurrency(currency)
		if err != nil {
			return AccountDetails{}, calculation.ErrInvalid
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
	id, connection, provider string
	rule                     string
	reference                source.Reference
	details                  AccountDetails
}

func NewAccount(
	id, connection, provider string,
	reference source.Reference,
	details AccountDetails,
	rule string,
) (Account, error) {
	if id == "" || connection == "" || provider == "" || reference.IsZero() ||
		details.name == "" || rule == "" {
		return Account{}, calculation.ErrInvalid
	}

	return Account{
		id:         id,
		connection: connection,
		provider:   provider,
		reference:  reference,
		details:    details,
		rule:       rule,
	}, nil
}
func (a Account) ID() string                  { return a.id }
func (a Account) Connection() string          { return a.connection }
func (a Account) Provider() string            { return a.provider }
func (a Account) Reference() source.Reference { return a.reference }
func (a Account) Details() AccountDetails     { return a.details }
func (a Account) Rule() string                { return a.rule }

// AuthorizesSource checks the confirmed financial source account. Acquisition
// inputs and report formats retain provenance without defining this authority.
func (a Account) AuthorizesSource(connection, provider string, account source.Reference) bool {
	return a.connection == connection && a.provider == provider && a.reference == account
}

type ReferenceMetadata struct{ Allocation, Reason string }

type Instrument struct {
	metadata                               ReferenceMetadata
	id, name, ticker, kind, figi, evidence string
	status                                 calculation.ReferenceStatus
	authority                              calculation.SelectionAuthority
	rule                                   string
	reported                               source.Instrument
	revision                               int64
}

func NewInstrument(
	id, name, ticker, kind, status, figi, evidence string,
	reported source.Instrument,
	rule string, revision int64, authority calculation.SelectionAuthority, metadata ReferenceMetadata,
) (Instrument, error) {
	if revision < 1 || rule == "" || id == "" || reported.Kind() == "" ||
		(authority != calculation.AutomaticSelection && authority != calculation.OperatorSelection) ||
		(kind != string(source.Equity) && kind != string(source.Fund) && kind != string(source.Option) && kind != string(source.Debt) && kind != string(source.UnknownInstrument)) {
		return Instrument{}, calculation.ErrInvalid
	}

	return Instrument{
		metadata:  metadata,
		revision:  revision,
		id:        id,
		name:      name,
		ticker:    ticker,
		kind:      kind,
		status:    calculation.ReferenceStatus(status),
		authority: authority,
		figi:      figi,
		evidence:  evidence,
		reported:  reported,
		rule:      rule,
	}, nil
}
func (i Instrument) ReferenceMetadata() ReferenceMetadata      { return i.metadata }
func (i Instrument) ID() string                                { return i.id }
func (i Instrument) Name() string                              { return i.name }
func (i Instrument) Ticker() string                            { return i.ticker }
func (i Instrument) Kind() string                              { return i.kind }
func (i Instrument) Status() calculation.ReferenceStatus       { return i.status }
func (i Instrument) Authority() calculation.SelectionAuthority { return i.authority }
func (i Instrument) FIGI() string                              { return i.figi }
func (i Instrument) Evidence() string                          { return i.evidence }
func (i Instrument) Reported() source.Instrument               { return i.reported }
func (i Instrument) Rule() string                              { return i.rule }

type Listing struct{ id, instrument, venue, ticker, currency, figi string }

func NewListing(id, instrument, venue, ticker, currency, figi string) (Listing, error) {
	if id == "" || instrument == "" {
		return Listing{}, calculation.ErrInvalid
	}

	if _, err := financial.ParseCurrency(currency); err != nil {
		return Listing{}, calculation.ErrInvalid
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
	state                                  calculation.ActivityState
	terms                                  source.Trade
	revision                               int64
}

func NewExecution(
	id, account, instrument, listing, rule string,
	state calculation.ActivityState,
	terms source.Trade, revision int64,
) (Execution, error) {
	if revision < 1 || id == "" || account == "" || instrument == "" || rule == "" || state.Lifecycle() == "" {
		return Execution{}, calculation.ErrInvalid
	}

	if _, err := source.NewTrade(terms.Fields()); err != nil {
		return Execution{}, err
	}

	return Execution{id, account, instrument, listing, rule, state, terms, revision}, nil
}
func (e Execution) ID() string                       { return e.id }
func (e Execution) Account() string                  { return e.account }
func (e Execution) Instrument() string               { return e.instrument }
func (e Execution) Listing() string                  { return e.listing }
func (e Execution) Status() string                   { return string(e.state.Lifecycle()) }
func (e Execution) State() calculation.ActivityState { return e.state }
func (e Execution) Rule() string                     { return e.rule }
func (e Execution) Terms() source.Trade              { return e.terms }

type (
	ActivityContext struct {
		AccountName, InstrumentName, InstrumentTicker string
		Realized                                      *PublishedRealizedPnL
		FieldDetails                                  []PublishedFieldDetail
		PriceSources                                  []SourceLocation
		PriceCorrected                                bool
	}
	SourceLocation struct{ Artifact, Version, Location string }
	Observation    struct {
		Artifact, Version, Location, Rule, Retrieved, ActivityID string
		SourceName, Stream                                       string
		EarlierAppearance                                        bool
		Outcome                                                  calculation.ObservationOutcome
		Reported                                                 source.ActivityObservation
	}
	SourceObservation struct {
		Observation
		Previous, Next string
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
	state                         calculation.ActivityState
	terms                         source.Lifecycle
	revision                      int64
}

func NewLifecycle(
	id, account, instrument, rule string,
	state calculation.ActivityState,
	terms source.Lifecycle,
	revision int64,
) (Lifecycle, error) {
	if revision < 1 || id == "" || account == "" || instrument == "" || rule == "" || state.Lifecycle() == "" {
		return Lifecycle{}, calculation.ErrInvalid
	}

	if _, err := source.NewLifecycle(terms.Fields()); err != nil {
		return Lifecycle{}, err
	}

	return Lifecycle{id, account, instrument, rule, state, terms, revision}, nil
}
func (l Lifecycle) ID() string                       { return l.id }
func (l Lifecycle) Account() string                  { return l.account }
func (l Lifecycle) Instrument() string               { return l.instrument }
func (l Lifecycle) Status() string                   { return string(l.state.Lifecycle()) }
func (l Lifecycle) State() calculation.ActivityState { return l.state }
func (l Lifecycle) Terms() source.Lifecycle          { return l.terms }

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
	state             calculation.ActivityState
	terms             source.Conversion
	revision          int64
}

func NewConversion(
	id, account, rule string,
	state calculation.ActivityState,
	terms source.Conversion,
	revision int64,
) (Conversion, error) {
	if revision < 1 || id == "" || account == "" || rule == "" || state.Lifecycle() == "" {
		return Conversion{}, calculation.ErrInvalid
	}

	if _, err := source.NewConversion(terms.Fields()); err != nil {
		return Conversion{}, err
	}

	return Conversion{id, account, rule, state, terms, revision}, nil
}
func (c Conversion) ID() string                       { return c.id }
func (c Conversion) Account() string                  { return c.account }
func (c Conversion) Status() string                   { return string(c.state.Lifecycle()) }
func (c Conversion) State() calculation.ActivityState { return c.state }
func (c Conversion) Terms() source.Conversion         { return c.terms }

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
	Status                                                 calculation.CorrectionStatus
	TargetIDs, ReplacementIDs                              []string
}
