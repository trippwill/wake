// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"reflect"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
)

func TestReconfigureReconcilesEnabledDiscoveryStreams(t *testing.T) {
	s, _ := testStore(t)
	ctx := t.Context()
	settings, _ := provider.NewSettings(map[string]string{})
	old, _ := provider.NewStream("old", "synthetic-v1")
	old, _ = old.WithDiscovery()
	added, _ := provider.NewStream("added", "synthetic-v1")
	added, _ = added.WithDiscovery()
	acquireOnly, _ := provider.NewStream("acquire-only", "synthetic-v1")

	connection := host.Connection{
		ID:          "synthetic",
		Provider:    "synthetic",
		Name:        "Synthetic",
		Settings:    settings,
		CanDiscover: true,
	}
	if err := s.Configure(ctx, connection, nil); err != nil {
		t.Fatal(err)
	}

	oldKey := host.WorkKey{Connection: connection.ID, Stream: old.ID(), Operation: "discover"}
	if err := s.Schedule(ctx, oldKey, true); err != nil {
		t.Fatal(err)
	}

	cp, err := s.Checkpoint(ctx, oldKey)
	if err != nil {
		t.Fatal(err)
	}

	state, _ := provider.NewState("synthetic-1", []byte(`{"page":"two"}`))
	next := time.Now().UTC().Add(time.Hour)

	step, _ := provider.NewStep(state, provider.Artifact{}, next)
	if _, err = s.Accept(
		ctx,
		oldKey,
		cp.Revision,
		step,
		nil,
		provider.Progress{},
		host.PollCycle{},
	); err != nil {
		t.Fatal(err)
	}

	before, err := s.Checkpoint(ctx, oldKey)
	if err != nil {
		t.Fatal(err)
	}

	assertWork := func(want []host.WorkKey) {
		t.Helper()

		got, e := s.PendingWork(ctx, next.Add(time.Hour))
		if e != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("unexpected discovery schedule", got, want, e)
		}

		after, e := s.Checkpoint(ctx, oldKey)
		if e != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("unchanged continuation or deadline lost", before, after, e)
		}
	}

	connection.Streams = []provider.Stream{added, acquireOnly}
	if err = s.Reconfigure(ctx, connection); err != nil {
		t.Fatal(err)
	}

	assertWork([]host.WorkKey{{Connection: connection.ID, Stream: added.ID(), Operation: "discover"}})

	connection.Streams = []provider.Stream{}
	if err = s.Reconfigure(ctx, connection); err != nil {
		t.Fatal(err)
	}

	assertWork([]host.WorkKey{})

	connection.Streams = []provider.Stream{old}
	if err = s.Reconfigure(ctx, connection); err != nil {
		t.Fatal(err)
	}

	assertWork([]host.WorkKey{oldKey})

	connection.CanDiscover = false
	if err = s.Reconfigure(ctx, connection); err != nil {
		t.Fatal(err)
	}

	assertWork([]host.WorkKey{})
}
