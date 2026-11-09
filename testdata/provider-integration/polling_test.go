// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package providerproof_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
)

func TestRecurringPollingPreservesContinuationAndDeadlines(t *testing.T) {
	stream, _ := provider.NewStream("statements", "fixture-v1")
	stream, _ = stream.WithPolling(15*time.Minute, time.Minute)
	base := registration(t).Descriptor()
	descriptor, _ := provider.NewDescriptor(base.ID(), base.Name(), []provider.Stream{stream}, base.Fields(), nil)
	state, _ := provider.NewState("v1", []byte("in-progress"))
	var interval time.Duration
	var resumed provider.State
	var calls int
	acquirer := acquireFunc(func(_ context.Context, request provider.Request, _ provider.Access) (provider.Step, error) {
		interval, resumed = request.PollInterval, request.State
		calls++
		if calls == 1 {
			return provider.NewStep(state, provider.Artifact{}, request.Now.Add(2*time.Second))
		}
		floor := request.Now
		if calls == 2 {
			floor = floor.Add(40 * time.Minute)
		}
		step, err := provider.NewStep(provider.State{}, provider.Artifact{}, floor)
		if err != nil {
			return provider.Step{}, err
		}
		return step.WithPollCompleted()
	})
	r, err := provider.NewRegistration(descriptor, "v1", acquirer, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, r)
	ctx := t.Context()
	acquire := func(wantInterval, wantDelay time.Duration) {
		t.Helper()
		if _, err := f.host.Acquire(ctx, f.id, stream.ID()); err != nil {
			t.Fatal(err)
		}
		if cp := f.checkpoint(t, "acquire"); interval != wantInterval || !cp.Next.Equal(f.now.Add(wantDelay)) {
			t.Fatalf("interval %v and deadline %v, want %v and %v", interval, cp.Next, wantInterval, f.now.Add(wantDelay))
		}
	}
	acquire(15*time.Minute, 2*time.Second)
	if err := f.host.UpdatePolling(ctx, f.id, stream.ID(), "30s"); !errors.Is(err, host.ErrPollInterval) {
		t.Fatalf("invalid interval: %v", err)
	}
	if err := f.host.UpdatePolling(ctx, f.id, stream.ID(), "20m"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.host.Acquire(ctx, f.id, stream.ID()); !errors.Is(err, provider.ErrTemporary) {
		t.Fatalf("interval edit bypassed the continuation deadline: %v", err)
	}

	f.host.Close()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.pollDefaults = map[string]time.Duration{"fixture:statements": 30 * time.Minute}
	f.open(t, r)
	f.now = f.now.Add(2 * time.Second)
	acquire(20*time.Minute, 40*time.Minute)
	if string(resumed.Payload()) != string(state.Payload()) {
		t.Fatal("polling edit lost the continuation")
	}

	if err := f.host.UpdatePolling(ctx, f.id, stream.ID(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.host.Acquire(ctx, f.id, stream.ID()); !errors.Is(err, provider.ErrTemporary) {
		t.Fatalf("reset bypassed the upstream deadline: %v", err)
	}
	f.now = f.now.Add(40 * time.Minute)
	acquire(30*time.Minute, 30*time.Minute)
}
