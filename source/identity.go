// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package source defines immutable reported financial claims, before Wake
// resolves financial identities or selects accepted values.
package source

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrInvalidReference  Error = "invalid source reference"
	ErrInvalidIdentifier Error = "invalid reported identifier"
	ErrInvalidInstrument Error = "invalid reported instrument"
	ErrInvalidTrade      Error = "invalid reported trade"
)

// Reference preserves an assigning authority, object kind, optional scope and
// opaque source value. Connection and query identities do not define authority.
type Reference struct {
	authority string
	kind      string
	scope     string
	value     string
}

func NewReference(authority, kind, scope, value string) (Reference, error) {
	if !validText(authority, 256) || !validText(kind, 128) ||
		(scope != "" && !validText(scope, 1024)) || !validText(value, 1024) {
		return Reference{}, ErrInvalidReference
	}

	return Reference{authority: authority, kind: kind, scope: scope, value: value}, nil
}

func (r Reference) Authority() string { return r.authority }
func (r Reference) Kind() string      { return r.kind }
func (r Reference) Scope() string     { return r.scope }
func (r Reference) Value() string     { return r.value }
func (r Reference) IsZero() bool      { return r == Reference{} }

// IdentifierKind states the reported scheme/allocation level. The source field
// remains attached even when core cannot corroborate its asserted meaning.
type IdentifierKind string

const (
	ISIN             IdentifierKind = "isin"
	CUSIP            IdentifierKind = "cusip"
	SEDOL            IdentifierKind = "sedol"
	VenueFIGI        IdentifierKind = "venue_figi"
	CompositeFIGI    IdentifierKind = "composite_figi"
	ShareClassFIGI   IdentifierKind = "share_class_figi"
	InstrumentFIGI   IdentifierKind = "instrument_figi"
	UnclassifiedFIGI IdentifierKind = "unclassified_figi"
	OCC              IdentifierKind = "occ"
)

type Identifier struct {
	kind  IdentifierKind
	value string
	field string
}

func NewIdentifier(kind IdentifierKind, value, field string) (Identifier, error) {
	switch kind {
	case ISIN, CUSIP, SEDOL, VenueFIGI, CompositeFIGI, ShareClassFIGI, InstrumentFIGI, UnclassifiedFIGI, OCC:
	default:
		return Identifier{}, ErrInvalidIdentifier
	}

	if !validText(value, 256) || !validText(field, 256) {
		return Identifier{}, ErrInvalidIdentifier
	}

	return Identifier{kind: kind, value: value, field: field}, nil
}

func (i Identifier) Kind() IdentifierKind { return i.kind }
func (i Identifier) Value() string        { return i.value }
func (i Identifier) Field() string        { return i.field }

func validText(text string, limit int) bool {
	return text != "" && len(text) <= limit && utf8.ValidString(text) && strings.TrimSpace(text) == text &&
		strings.IndexFunc(text, unicode.IsControl) < 0
}
