// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/provider"
)

func TestRefreshPollsLeavesOtherWorkScheduled(t *testing.T) {
	s := coverageStore(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)
	continuation, _ := provider.NewState("test-v1", []byte{0xff})
	waiting, _ := provider.NewStep(continuation, provider.Artifact{}, later)
	routine, _ := provider.NewStep(continuation, provider.Artifact{}, now.Add(-time.Second))
	routine, _ = routine.WithPollCompleted()
	routine = routine.NotBefore(later)

	for _, entry := range []struct {
		stream  string
		step    provider.Step
		enabled bool
	}{
		{"routine", routine, true},
		{"continuing", waiting, true},
		{"failed", routine, true},
		{"disabled", routine, false},
	} {
		key := host.WorkKey{Connection: "synthetic", Stream: entry.stream, Operation: "acquire"}
		if err := s.Schedule(ctx, key, entry.enabled); err != nil {
			t.Fatal(err)
		}

		if _, err := s.Accept(ctx, key, 0, entry.step, nil, provider.Progress{}); err != nil {
			t.Fatal(err)
		}

		if entry.stream == "failed" {
			if err := s.Fail(ctx, key, host.FailureTemporary, "source unavailable"); err != nil {
				t.Fatal(err)
			}
		}
	}

	if _, err := s.db.ExecContext(ctx, "UPDATE profile SET paused=1"); err != nil {
		t.Fatal(err)
	}

	if n, err := s.RefreshPolls(ctx, now); err != nil || n != 0 {
		t.Fatal("refresh advanced paused work", n, err)
	}

	if _, err := s.db.ExecContext(ctx, "UPDATE profile SET paused=0"); err != nil {
		t.Fatal(err)
	}

	if n, err := s.RefreshPolls(ctx, now); err != nil || n != 1 {
		t.Fatal("refresh should advance only the routine poll", n, err)
	}

	work, err := s.PendingWork(ctx, now)
	if err != nil || len(work) != 1 || work[0].Stream != "routine" {
		t.Fatal("unexpected runnable work", work, err)
	}
}
