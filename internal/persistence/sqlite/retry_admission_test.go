// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

type blockedDiscovery struct{ started, release chan struct{} }

func TestRetryAdvancesCheckpointRevision(t *testing.T) {
	for _, operation := range []string{"acquire", "discover"} {
		t.Run(operation, func(t *testing.T) {
			ctx := t.Context()
			s, _ := testStore(t)
			settings, _ := provider.NewSettings(map[string]string{"query_id": "123"})
			if err := s.Configure(
				ctx,
				host.Connection{ID: "synthetic", Provider: "ibkr-flex", Settings: settings},
				nil,
			); err != nil {
				t.Fatal(err)
			}

			key := host.WorkKey{Connection: "synthetic", Stream: ibkrflex.Confirmations, Operation: operation}

			before, err := s.Checkpoint(ctx, key)
			if err != nil {
				t.Fatal(err)
			}

			if err = s.Retry(ctx, key); err != nil {
				t.Fatal(err)
			}

			after, err := s.Checkpoint(ctx, key)
			if err != nil || after.Revision != before.Revision+1 {
				t.Fatal(after, err)
			}

			step, _ := provider.NewStep(provider.State{}, provider.Artifact{}, time.Now().Add(time.Hour))
			if _, err = s.Accept(
				ctx,
				key,
				before.Revision,
				step,
				nil,
				provider.Progress{},
			); !errors.Is(
				err,
				provider.ErrConflict,
			) {
				t.Fatal("stale acceptance overwrote retry", err)
			}
		})
	}
}

func (d blockedDiscovery) Discover(
	ctx context.Context,
	request provider.Request,
	_ provider.Access,
) (provider.Discovery, error) {
	close(d.started)

	select {
	case <-ctx.Done():
		return provider.Discovery{}, ctx.Err()
	case <-d.release:
	}

	state, _ := provider.NewState("synthetic-continuation", []byte("pending"))
	step, _ := provider.NewStep(state, provider.Artifact{}, request.Now.Add(time.Hour))

	return provider.NewDiscovery(step, nil)
}

func TestWorkflowRetryRefusesActiveDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s, _ := testStore(t)

	base, err := ibkrflex.Registration(ibkrflex.Options{})
	if err != nil {
		t.Fatal(err)
	}

	d := blockedDiscovery{make(chan struct{}), make(chan struct{})}

	r, err := provider.NewRegistration(
		base.Descriptor(),
		ibkrflex.Version,
		base.Acquirer(),
		base.Normalizer(),
		d,
		base,
	)
	if err != nil {
		t.Fatal(err)
	}

	h, err := host.New(ctx, s, testEnvelope(t), nil, nil, r)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { cancel(); h.Close() }()

	w := application.New(s, h, nil, "ibkr-flex", ibkrflex.Confirmations, ibkrflex.Version, nil, nil)

	token := credential.NewSecret([]byte("synthetic-token"))
	defer token.Clear()

	id, err := w.CreateConnection(ctx, "Synthetic", "123", token)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { _, e := h.Discover(ctx, id, ibkrflex.Confirmations); done <- e }()

	select {
	case <-d.started:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not start")
	}

	if err = w.Retry(ctx, id); !errors.Is(err, host.ErrBusy) {
		t.Fatal("retry raced active discovery", err)
	}

	close(d.release)

	if err = <-done; err != nil {
		t.Fatal(err)
	}

	key := host.WorkKey{Connection: id, Stream: ibkrflex.Confirmations, Operation: "discover"}

	before, err := s.Checkpoint(ctx, key)
	if err != nil || before.State.IsZero() {
		t.Fatal(before, err)
	}

	if err = w.Retry(ctx, id); err != nil {
		t.Fatal(err)
	}

	after, err := s.Checkpoint(ctx, key)
	if err != nil || !after.State.IsZero() || after.Revision != before.Revision+1 {
		t.Fatal("retry did not invalidate old checkpoint", after, err)
	}

	step, _ := provider.NewStep(before.State, provider.Artifact{}, before.Next)
	if _, err = s.Accept(
		ctx,
		key,
		before.Revision,
		step,
		nil,
		provider.Progress{},
	); !errors.Is(
		err,
		provider.ErrConflict,
	) {
		t.Fatal("old acquisition overwrote retry", err)
	}
}
