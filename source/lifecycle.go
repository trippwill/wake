// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package source

import (
	"slices"

	"go.trippwill.dev/wake/financial"
)

type LifecycleKind string

const (
	Exercise   LifecycleKind = "exercise"
	Assignment LifecycleKind = "assignment"
	Expiration LifecycleKind = "expiration"
)

type RelationshipKind string

const (
	LifecycleLink    RelationshipKind = "lifecycle"
	StockBookingLink RelationshipKind = "stock-booking"
)

type Relationship struct {
	kind   RelationshipKind
	target Reference
}

func NewRelationship(kind RelationshipKind, target Reference) (Relationship, error) {
	if (kind != LifecycleLink && kind != StockBookingLink) || target.IsZero() {
		return Relationship{}, ErrInvalidTrade
	}

	return Relationship{kind, target}, nil
}
func (r Relationship) Kind() RelationshipKind { return r.kind }
func (r Relationship) Target() Reference      { return r.target }
func validRelationships(links []Relationship) bool {
	if len(links) > 32 {
		return false
	}

	for _, r := range links {
		if r.target.IsZero() {
			return false
		}
	}

	return true
}

type LifecycleInput struct {
	Location              string
	Account, Event, Order Reference
	EventAliases          []Reference
	// EventDistinct states that Event identifies one event within its qualified scope.
	// Its native kind and value remain opaque to the core.
	EventDistinct bool
	Instrument    Instrument
	Kind          LifecycleKind
	Quantity      financial.Quantity
	Date          financial.Date
	At            financial.Timestamp
	Relationships []Relationship
}
type Lifecycle struct{ input LifecycleInput }

func NewLifecycle(f LifecycleInput) (Lifecycle, error) {
	if !validText(f.Location, 1024) || f.Account.IsZero() || (f.EventDistinct && f.Event.IsZero()) ||
		f.Instrument.Kind() != Option ||
		!f.Quantity.Positive() ||
		f.Date.IsZero() ||
		len(f.EventAliases) > 16 ||
		!validRelationships(f.Relationships) {
		return Lifecycle{}, ErrInvalidTrade
	}

	if f.Kind != Exercise && f.Kind != Assignment && f.Kind != Expiration {
		return Lifecycle{}, ErrInvalidTrade
	}

	for _, r := range f.EventAliases {
		if r.IsZero() {
			return Lifecycle{}, ErrInvalidTrade
		}
	}

	f.EventAliases = slices.Clone(f.EventAliases)
	f.Relationships = slices.Clone(f.Relationships)

	return Lifecycle{f}, nil
}

func (l Lifecycle) Fields() LifecycleInput {
	f := l.input
	f.EventAliases = slices.Clone(f.EventAliases)
	f.Relationships = slices.Clone(f.Relationships)

	return f
}
