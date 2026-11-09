// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
)

func TestOverviewSyncTiming(t *testing.T) {
	ctx := t.Context()
	s, _ := testStore(t)
	settings, _ := provider.NewSettings(nil)
	for _, id := range []string{"first", "second"} {
		if err := s.Configure(
			ctx,
			host.Connection{ID: id, Name: id, Provider: "synthetic", Settings: settings},
			nil,
		); err != nil {
			t.Fatal(err)
		}
	}

	key := host.WorkKey{Connection: "first", Stream: "activity", Operation: "acquire"}
	if err := s.Schedule(ctx, key, true); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	deadline := start.Add(time.Hour)
	retain := func(key host.WorkKey, at time.Time) string {
		t.Helper()

		cp, err := s.Checkpoint(ctx, key)
		if err != nil {
			t.Fatal(err)
		}

		artifact, err := provider.NewArtifact("synthetic-v1", "application/json", at, []byte("{}"))
		if err != nil {
			t.Fatal(err)
		}

		step, err := provider.NewStep(provider.State{}, artifact, deadline)
		if err != nil {
			t.Fatal(err)
		}

		id, err := s.Accept(ctx, key, cp.Revision, step, nil, provider.Progress{})
		if err != nil {
			t.Fatal(err)
		}

		return id
	}

	read := func() application.WorkStatus {
		t.Helper()

		view, err := s.Overview(ctx)
		if err != nil {
			t.Fatal(err)
		}

		for _, connection := range view.Status.Connections {
			for _, work := range connection.Work {
				if connection.ID == key.Connection && work.Stream == key.Stream && work.Operation == key.Operation {
					return work
				}
			}
		}

		t.Fatal("missing work status", key)

		return application.WorkStatus{}
	}
	if work := read(); !work.LastRetrieved.IsZero() || !work.Next.IsZero() {
		t.Fatal("unsuccessful stream shows timestamps", work)
	}

	first := retain(key, start)
	latest := start.Add(time.Minute)
	if second := retain(key, latest); first != second {
		t.Fatal("identical report did not reuse retained body")
	}

	retain(host.WorkKey{Connection: "first", Stream: "activity", Operation: "supply"}, start.Add(2*time.Minute))
	retain(
		host.WorkKey{Connection: "first", Stream: "activity", Operation: "discover"},
		start.Add(3*time.Minute),
	)
	retain(
		host.WorkKey{Connection: "first", Stream: "confirmations", Operation: "acquire"},
		start.Add(4*time.Minute),
	)
	retain(
		host.WorkKey{Connection: "second", Stream: "activity", Operation: "acquire"},
		start.Add(5*time.Minute),
	)

	for _, tc := range []struct {
		code    host.FailureCode
		blocked bool
	}{
		{host.FailureTemporary, false},
		{host.FailureUnauthorized, true},
		{host.FailureInvalid, true},
		{host.FailureUnavailable, true},
		{host.FailureMalformed, true},
		{host.FailureLimit, true},
	} {
		if err := s.Fail(ctx, key, tc.code, "synthetic failure"); err != nil {
			t.Fatal(err)
		}

		work := read()
		if !work.LastRetrieved.Equal(latest) || !work.Next.Equal(deadline) || work.RetryBlocked != tc.blocked {
			t.Fatalf("%s: unexpected Overview timing %+v", tc.code, work)
		}

		due, err := s.PendingWork(ctx, deadline)
		if err != nil || (len(due) == 0) != work.RetryBlocked {
			t.Fatal("Overview disagrees with scheduler", due, err, work)
		}
	}
}
