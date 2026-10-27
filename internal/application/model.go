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

type Instrument struct {
	id, name, ticker, kind, status, figi, evidence string
	rule                                           string
	reported                                       source.Instrument
}

func NewInstrument(
	id, name, ticker, kind, status, figi, evidence string,
	reported source.Instrument,
	rule string,
) (Instrument, error) {
	if rule == "" || id == "" || reported.Kind() == "" ||
		(kind != string(source.Equity) && kind != string(source.Fund)) {
		return Instrument{}, ErrInvalid
	}

	return Instrument{
		id:       id,
		name:     name,
		ticker:   ticker,
		kind:     kind,
		status:   status,
		figi:     figi,
		evidence: evidence,
		reported: reported,
		rule:     rule,
	}, nil
}
func (i Instrument) ID() string                  { return i.id }
func (i Instrument) Name() string                { return i.name }
func (i Instrument) Ticker() string              { return i.ticker }
func (i Instrument) Kind() string                { return i.kind }
func (i Instrument) Status() string              { return i.status }
func (i Instrument) FIGI() string                { return i.figi }
func (i Instrument) Evidence() string            { return i.evidence }
func (i Instrument) Reported() source.Instrument { return i.reported }
func (i Instrument) Rule() string                { return i.rule }

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
	id, account, instrument, listing, status, rule string
	terms                                          source.Trade
}

func NewExecution(
	id, account, instrument, listing, status, rule string,
	terms source.Trade,
) (Execution, error) {
	if id == "" || account == "" || instrument == "" || listing == "" || rule == "" {
		return Execution{}, ErrInvalid
	}

	if _, err := source.NewTrade(terms.Fields()); err != nil {
		return Execution{}, err
	}

	return Execution{id, account, instrument, listing, status, rule, terms}, nil
}
func (e Execution) ID() string          { return e.id }
func (e Execution) Account() string     { return e.account }
func (e Execution) Instrument() string  { return e.instrument }
func (e Execution) Listing() string     { return e.listing }
func (e Execution) Status() string      { return e.status }
func (e Execution) Rule() string        { return e.rule }
func (e Execution) Terms() source.Trade { return e.terms }

type (
	Observation     struct{ Artifact, Version, Location, Outcome, Rule string }
	ExecutionDetail struct {
		Execution    Execution
		Observations []Observation
	}
	ReferenceAttempt struct{ Evidence, Status, Retrieved, Rule string }
	InstrumentDetail struct {
		Instrument   Instrument
		Listings     []Listing
		References   []ReferenceAttempt
		Observations []Observation
	}
	ReferenceEvidence struct{ Retrieved, Request, Response string }
)
