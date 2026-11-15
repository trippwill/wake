// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"go.trippwill.dev/wake/financial"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/source"
)

type RestartInput struct {
	ID, Provider, Name, Binding, AccountID string
	AccountRevision                        int64
	Account                                source.Reference
	Settings                               provider.Settings
	Retained                               map[string]financial.Date
	Approved                               map[string]bool
	PacingFloor                            time.Time
	ReplanAt                               time.Time
}

type RestartInputs struct {
	At, ReferenceFloor time.Time
	Revision           int64
	Sources            []RestartInput
}

type RestartScope struct {
	Connection, Provider, Name, Stream, AccountID, Account, Binding string
	AccountRevision                                                 int64
	Approved                                                        bool
	Blocked                                                         string
	Plan                                                            provider.RestartPlan
	NotBefore                                                       time.Time
}

type RestartReview struct {
	Required    bool
	Revision    int64
	Digest      string
	Reference   bool
	ReferenceAt time.Time
	Scopes      []RestartScope
}

type restartStore interface {
	RestartInputs(context.Context) (RestartInputs, error)
	ApproveRestart(context.Context, RestartReview, string, time.Time) error
}

func (w *Workflow) PreviewRestart(ctx context.Context) (RestartReview, error) {
	store, ok := w.store.(restartStore)
	if w.host == nil || !ok {
		return RestartReview{}, nil
	}

	inputs, err := store.RestartInputs(ctx)
	if err != nil || inputs.At.IsZero() {
		return RestartReview{}, err
	}

	review := RestartReview{Required: true, Revision: inputs.Revision, ReferenceAt: inputs.At.Add(time.Minute)}
	if inputs.ReferenceFloor.After(review.ReferenceAt) {
		review.ReferenceAt = inputs.ReferenceFloor
	}

	review.Reference = w.reference != nil && w.reference.CheckAccess(ctx) == nil

	for _, input := range inputs.Sources {
		review.Scopes = append(review.Scopes, w.planSourceRestart(ctx, inputs.At, input)...)
	}

	type scopeBinding struct {
		Scope   RestartScope
		Version string
		State   []byte
		From    string
		To      string
	}

	bindings := make([]scopeBinding, 0, len(review.Scopes))
	for _, scope := range review.Scopes {
		bindings = append(bindings, scopeBinding{
			scope, scope.Plan.State().Version(), scope.Plan.State().Payload(),
			scope.Plan.From().String(), scope.Plan.To().String(),
		})
	}

	data, err := json.Marshal(struct {
		Revision    int64
		At          time.Time
		Reference   bool
		ReferenceAt time.Time
		Bindings    []scopeBinding
	}{review.Revision, inputs.At, review.Reference, review.ReferenceAt, bindings})
	if err != nil {
		return RestartReview{}, err
	}

	digest := sha256.Sum256(data)
	review.Digest = hex.EncodeToString(digest[:])

	return review, nil
}

func (w *Workflow) planSourceRestart(ctx context.Context, at time.Time, input RestartInput) []RestartScope {
	if input.ReplanAt.After(at) {
		at = input.ReplanAt
	}

	scope := RestartScope{
		Connection: input.ID, Provider: input.Provider, Name: input.Name,
		AccountID: input.AccountID, Account: input.Account.Value(),
		Binding: input.Binding, AccountRevision: input.AccountRevision,
	}
	registration, _ := w.Provider(input.Provider)

	planner, supported := registration.Acquirer().(provider.RestartPlanner)
	if !supported {
		scope.Blocked = "This provider has no supported restart plan."
		return []RestartScope{scope}
	}

	if input.Account.IsZero() {
		scope.Blocked = "A confirmed source account is required for recovery."
		return []RestartScope{scope}
	}

	if input.Binding == "" || w.host.CheckAccess(ctx, input.ID) != nil {
		scope.Blocked = "Review the source settings and credentials."
		return []RestartScope{scope}
	}

	streams, err := registration.ConfigureStreams(input.Settings)
	if err != nil || len(streams) == 0 {
		scope.Blocked = "No valid acquisition streams are enabled."
		return []RestartScope{scope}
	}

	slices.SortFunc(streams, func(a, b provider.StreamConfiguration) int {
		return strings.Compare(a.Stream().ID(), b.Stream().ID())
	})

	out := make([]RestartScope, 0, len(streams))
	for _, configuration := range streams {
		current := scope
		current.Stream = configuration.Stream().ID()
		current.Approved = input.Approved[current.Stream]
		if !current.Approved {
			plan, err := planner.PlanRestart(provider.RestartRequest{
				Stream: current.Stream, Settings: input.Settings, SourceAccount: input.Account,
				RetainedThrough: input.Retained[current.Stream], RecoveredAt: at,
			})
			if err != nil {
				current.Blocked = "This account or stream has no supported restart plan."
			} else {
				current.Plan = plan
				current.NotBefore = plan.NotBefore()
				if input.PacingFloor.After(current.NotBefore) {
					current.NotBefore = input.PacingFloor
				}
			}
		}

		out = append(out, current)
	}

	return out
}

func (w *Workflow) ApproveRestart(ctx context.Context, revision int64, digest, actor string) error {
	review, err := w.PreviewRestart(ctx)
	if err != nil {
		return err
	}

	if !review.Required || review.Revision != revision || digest == "" || review.Digest != digest {
		return ErrStale
	}

	store := w.store.(restartStore)
	if err := store.ApproveRestart(ctx, review, actor, w.now()); err != nil {
		return err
	}

	w.notify()

	return nil
}
