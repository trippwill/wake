// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package provider publishes Wake's build-time source extension contracts. It
// depends only on reported financial vocabulary, never host implementation types.
package provider

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.trippwill.dev/wake/source"
)

type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrInvalid      Error = "invalid provider contract value"
	ErrUnauthorized Error = "source access not authorized"
	ErrTemporary    Error = "temporary source failure"
	ErrUnavailable  Error = "source capability unavailable"
	ErrMalformed    Error = "malformed source evidence"
	ErrConflict     Error = "source state revision conflict"
	ErrLimit        Error = "source resource limit exceeded"
)

// Acquirer performs one bounded source-protocol step. The host persists returned
// evidence and continuation together before acknowledging this step.
type Acquirer interface {
	Acquire(context.Context, Request, Access) (Step, error)
}

// SettingsPreparer validates and canonicalizes provider-owned configuration before
// the host admits a connection. It never opens credentials or starts acquisition.
type SettingsPreparer interface {
	PrepareSettings(Settings) (Settings, error)
}

// Normalizer interprets retained evidence without credentials or financial
// lookups. A sink failure aborts processing; it must not be counted as a bad row.
type Normalizer interface {
	Normalize(context.Context, Artifact, Claims) (Result, error)
}

// Access is scoped by the host to one authorized acquisition. Secret bytes are
// valid only for the callback; providers must not retain or report them.
type Access interface {
	WithSecret(context.Context, string, func([]byte) error) error
	Report(context.Context, Progress) error
	ReservePacing(context.Context, string, PacingScope) (Reservation, error)
	DelayPacing(context.Context, string, PacingScope, time.Time) error
}

// Claims accepts a processing attempt's source interpretations. The host defines
// its commit boundary and retains artifact/location/version provenance.
// Calls are sequential; a normalizer must not retain the sink after returning.
type Claims interface {
	PutTrade(context.Context, source.Trade) error
	PutInstrument(context.Context, source.InstrumentObservation) error
	Reject(context.Context, string, string) error
}

type FieldKind string

const (
	TextField   FieldKind = "text"
	SecretField FieldKind = "secret"
)

type Field struct {
	key      string
	label    string
	kind     FieldKind
	required bool
}

func NewField(key, label string, kind FieldKind, required bool) (Field, error) {
	if !validKey(key) || !validText(label, 256) || (kind != TextField && kind != SecretField) {
		return Field{}, ErrInvalid
	}

	return Field{key: key, label: label, kind: kind, required: required}, nil
}

func (f Field) Key() string     { return f.key }
func (f Field) Label() string   { return f.label }
func (f Field) Kind() FieldKind { return f.kind }
func (f Field) Required() bool  { return f.required }

type Stream struct {
	id     string
	format string
}

func NewStream(id, format string) (Stream, error) {
	if !validKey(id) || !validKey(format) {
		return Stream{}, ErrInvalid
	}

	return Stream{id: id, format: format}, nil
}

func (s Stream) ID() string     { return s.id }
func (s Stream) Format() string { return s.format }

type Descriptor struct {
	id      string
	name    string
	streams []Stream
	fields  []Field
	pacing  []PacingPolicy
}

func NewDescriptor(
	id, name string,
	streams []Stream,
	fields []Field,
	pacing []PacingPolicy,
) (Descriptor, error) {
	if !validKey(id) || !validText(name, 256) || len(streams) == 0 || len(streams) > 64 || len(fields) > 64 ||
		len(pacing) > 32 {
		return Descriptor{}, ErrInvalid
	}

	seen := make(map[string]bool)
	for _, stream := range streams {
		if stream.id == "" || seen[stream.id] {
			return Descriptor{}, ErrInvalid
		}

		seen[stream.id] = true
	}

	clear(seen)

	for _, field := range fields {
		if field.key == "" || seen[field.key] {
			return Descriptor{}, ErrInvalid
		}

		seen[field.key] = true
	}

	clear(seen)

	for _, policy := range pacing {
		if policy.ID() == "" || seen[policy.ID()] {
			return Descriptor{}, ErrInvalid
		}

		seen[policy.ID()] = true
	}

	return Descriptor{
		id:      id,
		name:    name,
		streams: slices.Clone(streams),
		fields:  slices.Clone(fields),
		pacing:  slices.Clone(pacing),
	}, nil
}

func (d Descriptor) Pacing() []PacingPolicy { return slices.Clone(d.pacing) }

func (d Descriptor) ID() string        { return d.id }
func (d Descriptor) Name() string      { return d.name }
func (d Descriptor) Streams() []Stream { return slices.Clone(d.streams) }
func (d Descriptor) Fields() []Field   { return slices.Clone(d.fields) }

// Registration binds explicit capabilities to metadata. Construction starts no
// work and opens neither credentials nor storage. There is no global registry.
type Registration struct {
	descriptor Descriptor
	version    string
	acquirer   Acquirer
	normalizer Normalizer
	discoverer Discoverer
	preparer   SettingsPreparer
}

func NewRegistration(
	d Descriptor,
	version string,
	acquirer Acquirer,
	normalizer Normalizer,
	discoverer Discoverer,
	preparer SettingsPreparer,
) (Registration, error) {
	if d.id == "" || !validKey(version) || (acquirer == nil && normalizer == nil && discoverer == nil) {
		return Registration{}, ErrInvalid
	}

	return Registration{
		descriptor: d,
		version:    version,
		acquirer:   acquirer,
		normalizer: normalizer,
		discoverer: discoverer,
		preparer:   preparer,
	}, nil
}

func (r Registration) Discoverer() Discoverer { return r.discoverer }

func (r Registration) Descriptor() Descriptor { return r.descriptor }
func (r Registration) Version() string        { return r.version }
func (r Registration) Acquirer() Acquirer     { return r.acquirer }
func (r Registration) Normalizer() Normalizer { return r.normalizer }

func (r Registration) PrepareSettings(settings Settings) (Settings, error) {
	if r.preparer != nil {
		return r.preparer.PrepareSettings(settings)
	}

	return settings, nil
}

func validKey(text string) bool {
	if text == "" || len(text) > 128 {
		return false
	}

	for _, c := range text {
		allowed := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '/'
		if !allowed {
			return false
		}
	}

	return true
}

func validText(text string, limit int) bool {
	return text != "" && len(text) <= limit && utf8.ValidString(text) && strings.TrimSpace(text) == text &&
		strings.IndexFunc(text, unicode.IsControl) < 0
}
