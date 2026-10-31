// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"maps"
	"slices"
	"time"

	"go.trippwill.dev/wake/financial"
)

// Settings contains non-secret configuration. Secret fields are supplied only
// through Access. The host validates fields against the selected descriptor.
type Settings struct{ values map[string]string }

func NewSettings(values map[string]string) (Settings, error) {
	if len(values) > 64 {
		return Settings{}, ErrLimit
	}

	for key, value := range values {
		if !validKey(key) || !validText(value, 4096) {
			return Settings{}, ErrInvalid
		}
	}

	return Settings{values: maps.Clone(values)}, nil
}

func (s Settings) Values() map[string]string { return maps.Clone(s.values) }

func (s Settings) Get(key string) (string, bool) {
	value, found := s.values[key]
	return value, found
}

// State is a bounded opaque continuation document, not a queryable financial
// store. A zero State means no continuation. Its version belongs to the provider.
type State struct {
	version string
	payload []byte
}

func NewState(version string, payload []byte) (State, error) {
	if !validKey(version) || len(payload) == 0 {
		return State{}, ErrInvalid
	}

	if len(payload) > 64<<10 {
		return State{}, ErrLimit
	}

	return State{version: version, payload: slices.Clone(payload)}, nil
}

func (s State) Version() string { return s.version }
func (s State) Payload() []byte { return slices.Clone(s.payload) }
func (s State) IsZero() bool    { return s.version == "" }

// Request is one authorized stream attempt. State revision/concurrency checks
// are host responsibilities; the provider receives only the saved continuation.
type Request struct {
	Stream   string
	Settings Settings
	State    State
	Now      time.Time
}

type Step struct {
	state    State
	artifact Artifact
	next     time.Time
	failure  string
	reason   RetryReason
	coverage Coverage
}

// NewStep describes the next durable state after one bounded protocol step.
// A zero artifact permits deferral; a zero state clears a completed continuation.
func NewStep(state State, artifact Artifact, next time.Time) (Step, error) {
	if next.IsZero() {
		return Step{}, ErrInvalid
	}

	return Step{state: state, artifact: artifact, next: next.Round(0).UTC()}, nil
}

func (s Step) State() State              { return s.state }
func (s Step) Artifact() Artifact        { return s.artifact }
func (s Step) NextEligibleAt() time.Time { return s.next }

// NewRetryStep retains updated continuation and a safe failure code together.
// Generation-pending and unavailable-date waits use NewStep instead.
func NewRetryStep(state State, next time.Time, reason RetryReason, code string) (Step, error) {
	if !validKey(code) || (reason != RetryUpstream && reason != RetryNetwork && reason != RetryThrottled) {
		return Step{}, ErrInvalid
	}

	step, err := NewStep(state, Artifact{}, next)
	if err != nil {
		return Step{}, err
	}

	step.failure = code
	step.reason = reason

	return step, nil
}

func (s Step) FailureCode() string      { return s.failure }
func (s Step) RetryReason() RetryReason { return s.reason }

// RetryReason is a source-neutral outcome. FailureCode retains a provider-owned
// diagnostic identifier; hosts must never interpret its protocol meaning.
type RetryReason string

const (
	RetryUpstream  RetryReason = "upstream_retry"
	RetryNetwork   RetryReason = "network_failure"
	RetryThrottled RetryReason = "throttled"
)

// Coverage distinguishes requested bounds from the bounds actually reported.
// Absent returned bounds establish no coverage.
type Coverage struct{ requestedFrom, requestedTo, reportedFrom, reportedTo financial.Date }

func NewCoverage(from, to, reportedFrom, reportedTo financial.Date) (Coverage, error) {
	if from.IsZero() || to.IsZero() || from.String() > to.String() ||
		reportedFrom.IsZero() != reportedTo.IsZero() ||
		(!reportedFrom.IsZero() && reportedFrom.String() > reportedTo.String()) {
		return Coverage{}, ErrInvalid
	}

	return Coverage{from, to, reportedFrom, reportedTo}, nil
}

func (c Coverage) RequestedFrom() string {
	if c.requestedFrom.IsZero() {
		return ""
	}

	return c.requestedFrom.String()
}

func (c Coverage) RequestedTo() string {
	if c.requestedTo.IsZero() {
		return ""
	}

	return c.requestedTo.String()
}

func (c Coverage) ReportedFrom() string {
	if c.reportedFrom.IsZero() {
		return ""
	}

	return c.reportedFrom.String()
}

func (c Coverage) ReportedTo() string {
	if c.reportedTo.IsZero() {
		return ""
	}

	return c.reportedTo.String()
}

func (s Step) WithCoverage(c Coverage) (Step, error) {
	if s.artifact.IsZero() || c.requestedFrom.IsZero() {
		return Step{}, ErrInvalid
	}

	s.coverage = c

	return s, nil
}
func (s Step) Coverage() Coverage { return s.coverage }

// ProgressPhase describes observable work without exposing protocol codes.
type ProgressPhase string

const (
	ProgressRequesting  ProgressPhase = "requesting"
	ProgressWaiting     ProgressPhase = "waiting"
	ProgressPaced       ProgressPhase = "paced"
	ProgressRetained    ProgressPhase = "retained"
	ProgressUnavailable ProgressPhase = "unavailable"
)

// Progress describes work observed, not a claim that the host has committed it.
type Progress struct {
	code      ProgressPhase
	completed uint64
	total     uint64
}

func NewProgress(code ProgressPhase, completed, total uint64) (Progress, error) {
	switch code {
	case ProgressRequesting, ProgressWaiting, ProgressPaced, ProgressRetained, ProgressUnavailable:
	default:
		return Progress{}, ErrInvalid
	}

	if total != 0 && completed > total {
		return Progress{}, ErrInvalid
	}

	return Progress{code: code, completed: completed, total: total}, nil
}

func (p Progress) Phase() ProgressPhase { return p.code }
func (p Progress) Completed() uint64    { return p.completed }
func (p Progress) Total() uint64        { return p.total }
