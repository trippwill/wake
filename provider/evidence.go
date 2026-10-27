// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"time"
)

// Artifact holds a bounded immutable response. Host admission can impose a lower
// resource limit. Context (provider, stream, authority) comes from the host request.
type Artifact struct {
	format    string
	mediaType string
	retrieved time.Time
	content   []byte
}

func NewArtifact(format, mediaType string, retrieved time.Time, content []byte) (Artifact, error) {
	if !validKey(format) || !validText(mediaType, 256) || retrieved.IsZero() || len(content) == 0 {
		return Artifact{}, ErrInvalid
	}

	if len(content) > 64<<20 {
		return Artifact{}, ErrLimit
	}

	return Artifact{
		format: format, mediaType: mediaType, retrieved: retrieved.Round(0).UTC(), content: slices.Clone(content),
	}, nil
}

func (a Artifact) Format() string         { return a.format }
func (a Artifact) MediaType() string      { return a.mediaType }
func (a Artifact) RetrievedAt() time.Time { return a.retrieved }
func (a Artifact) Content() []byte        { return slices.Clone(a.content) }
func (a Artifact) IsZero() bool           { return a.format == "" }

// Hash identifies bytes, not event identity or acquisition observation identity.
func (a Artifact) Hash() string {
	sum := sha256.Sum256(a.content)
	return hex.EncodeToString(sum[:])
}

// Counts names each outcome so adding a supported claim kind cannot shift
// positional arguments in a provider's report accounting.
type Counts struct {
	Rows, Trades, Instruments, Rejected, Ignored uint64
}

type Result struct{ counts Counts }

func NewResult(counts Counts) (Result, error) {
	remaining := counts.Rows
	for _, n := range []uint64{counts.Trades, counts.Instruments, counts.Rejected, counts.Ignored} {
		if n > remaining {
			return Result{}, ErrInvalid
		}

		remaining -= n
	}

	if remaining != 0 {
		return Result{}, ErrInvalid
	}

	return Result{counts}, nil
}
func (r Result) Rows() uint64        { return r.counts.Rows }
func (r Result) Trades() uint64      { return r.counts.Trades }
func (r Result) Instruments() uint64 { return r.counts.Instruments }
func (r Result) Rejected() uint64    { return r.counts.Rejected }
func (r Result) Ignored() uint64     { return r.counts.Ignored }

func (r Result) Counts() Counts { return r.counts }
