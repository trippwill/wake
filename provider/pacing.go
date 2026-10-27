// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// PacingPolicy declares one real upstream request constraint. The host owns its
// durable reservations; streams/connections sharing a scope share the budget.
type PacingPolicy struct {
	id      string
	spacing time.Duration
}

func NewPacingPolicy(id string, spacing time.Duration) (PacingPolicy, error) {
	if !validKey(id) || spacing < time.Second || spacing > 24*time.Hour {
		return PacingPolicy{}, ErrInvalid
	}

	return PacingPolicy{id: id, spacing: spacing}, nil
}

func (p PacingPolicy) ID() string             { return p.id }
func (p PacingPolicy) Spacing() time.Duration { return p.spacing }

// PacingScope stores only a one-way digest. Account providers can derive a token
// scope inside WithSecret without putting that token into continuation or SQL.
type PacingScope struct {
	digest [32]byte
	valid  bool
}

func NewPacingScope(value []byte) (PacingScope, error) {
	if len(value) == 0 || len(value) > 64<<10 {
		return PacingScope{}, ErrInvalid
	}

	return PacingScope{digest: sha256.Sum256(value), valid: true}, nil
}

func (s PacingScope) Digest() string {
	if !s.valid {
		return ""
	}

	return hex.EncodeToString(s.digest[:])
}

type Reservation struct {
	granted bool
	next    time.Time
}

func NewReservation(granted bool, next time.Time) (Reservation, error) {
	if next.IsZero() {
		return Reservation{}, ErrInvalid
	}

	return Reservation{granted: granted, next: next.Round(0).UTC()}, nil
}

func (r Reservation) Granted() bool             { return r.granted }
func (r Reservation) NextEligibleAt() time.Time { return r.next }
