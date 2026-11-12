// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package persistence

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/host"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/provider"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func TestPollDeadlineChangesLeaveOtherWorkScheduled(t *testing.T) {
	s := newStoreWithSyntheticAccount(t)
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

		if _, err := s.Accept(
			ctx,
			key,
			0,
			entry.step,
			nil,
			provider.Progress{},
			host.PollCycle{At: now.Add(-time.Minute), Interval: 61 * time.Minute},
		); err != nil {
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

	for _, stream := range []string{"routine", "continuing", "failed", "disabled"} {
		if err := reschedulePoll(ctx, s.queries, "synthetic", stream, 20*time.Minute); err != nil {
			t.Fatal(err)
		}

		want := later
		if stream == "routine" {
			want = now.Add(19 * time.Minute)
		}

		key := host.WorkKey{Connection: "synthetic", Stream: stream, Operation: "acquire"}
		if checkpoint, err := s.Checkpoint(ctx, key); err != nil || !checkpoint.Next.Equal(want) {
			t.Fatalf("%s: edited deadline %s, want %s (%v)", stream, checkpoint.Next, want, err)
		}
	}

	if work, err := s.PendingWork(ctx, later); err != nil || len(work) != 0 {
		t.Fatal("editing a deadline admitted paused work", work, err)
	}

	if n, err := s.RefreshPolls(ctx, now); err != nil || n != 0 {
		t.Fatal("refresh advanced paused work", n, err)
	}

	if _, err := s.db.ExecContext(ctx, "UPDATE profile SET paused=0"); err != nil {
		t.Fatal(err)
	}

	if work, err := s.PendingWork(ctx, now.Add(19*time.Minute)); err != nil ||
		len(work) != 1 || work[0].Stream != "routine" {
		t.Fatal("resumed work did not use the edited deadline", work, err)
	}

	if n, err := s.RefreshPolls(ctx, now); err != nil || n != 1 {
		t.Fatal("refresh should advance only the routine poll", n, err)
	}

	work, err := s.PendingWork(ctx, now)
	if err != nil || len(work) != 1 || work[0].Stream != "routine" {
		t.Fatal("unexpected runnable work", work, err)
	}
}

func TestPausedQueryRepairPreservesDeadline(t *testing.T) {
	for _, lost := range []bool{false, true} {
		name := "existing connection"
		if lost {
			name = "lost connections store"
		}

		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			s, _ := testStore(t)
			settings, _ := provider.NewSettings(map[string]string{"query_id": "123"})

			connection := host.Connection{ID: "connection", Provider: "ibkr-flex", Settings: settings}
			if err := s.Configure(ctx, connection, map[string][]byte{"token": []byte("synthetic")}); err != nil {
				t.Fatal(err)
			}

			key := host.WorkKey{Connection: "connection", Stream: "confirmations", Operation: "acquire"}
			if err := s.Schedule(ctx, key, true); err != nil {
				t.Fatal(err)
			}

			if _, err := s.Checkpoint(ctx, key); err != nil {
				t.Fatal(err)
			}

			next := time.Now().UTC().Add(time.Hour)
			state, _ := provider.NewState("confirmation-state-1", []byte(`{"query":"123"}`))

			step, _ := provider.NewStep(state, provider.Artifact{}, next)
			progress, _ := provider.NewProgress("retained", 1, 1)
			if _, err := s.Accept(ctx, key, 0, step, nil, progress, host.PollCycle{}); err != nil {
				t.Fatal(err)
			}

			if _, err := s.db.Exec(`UPDATE profile SET paused=1`); err != nil {
				t.Fatal(err)
			}

			before, err := s.Checkpoint(ctx, key)
			if err != nil {
				t.Fatal(err)
			}

			if err = s.Reconfigure(ctx, connection); err != nil {
				t.Fatal(err)
			}

			same, err := s.Checkpoint(ctx, key)
			if err != nil || same.Revision != before.Revision ||
				string(same.State.Payload()) != string(before.State.Payload()) {
				t.Fatal("unchanged settings reset continuation", same, err)
			}

			if lost {
				if _, e := s.db.Exec(`DELETE FROM access.credentials; DELETE FROM access.configurations`); e != nil {
					t.Fatal(e)
				}
			}

			connection.Settings, _ = provider.NewSettings(map[string]string{"query_id": "456"})
			connection.ResetStreams = []string{"confirmations"}
			if err = s.Reconfigure(ctx, connection); err != nil {
				t.Fatal(err)
			}

			after, err := s.Checkpoint(ctx, key)
			if err != nil || after.Revision != before.Revision+1 || len(after.State.Payload()) != 0 ||
				after.State.Version() != "" ||
				after.Next != before.Next {
				t.Fatal("repair did not reset continuation while preserving deadline", after, err)
			}

			if work, e := s.PendingWork(ctx, next.Add(time.Hour)); e != nil || len(work) != 0 {
				t.Fatal("paused work ran", work, e)
			}

			if lost {
				if e := s.ReplaceSecret(ctx, connection.ID, "token", []byte("synthetic")); e != nil {
					t.Fatal(e)
				}
			}

			view, err := s.Dashboard(ctx)
			if err != nil {
				t.Fatal(err)
			}

			if err = s.ResumeStorage(ctx, view.Storage.Revision, "admin/test", time.Now()); err != nil {
				t.Fatal(err)
			}

			if work, e := s.PendingWork(ctx, next.Add(-time.Minute)); e != nil || len(work) != 0 {
				t.Fatal("deadline bypassed", work, e)
			}

			if work, e := s.PendingWork(ctx, next.Add(time.Minute)); e != nil || len(work) != 1 {
				t.Fatal("repaired work not scheduled", work, e)
			}

			calls := 0

			registration, err := ibkrflex.Registration(
				ibkrflex.Options{
					HTTPClient: &http.Client{
						Transport: repairTransport(func(request *http.Request) (*http.Response, error) {
							calls++

							if !strings.HasSuffix(request.URL.Path, "/SendRequest") ||
								request.URL.Query().Get("q") != "456" ||
								request.URL.Query().Get("p") != "365" {
								t.Error(
									"wrong repaired acquisition",
									request.URL.Path,
									request.URL.Query().Get("q"),
									request.URL.Query().Get("p"),
								)
							}

							return &http.Response{
								StatusCode: http.StatusOK,
								Header:     make(http.Header),
								Body: io.NopCloser(
									strings.NewReader(
										`<FlexStatementResponse><Status>Success</Status><ReferenceCode>synthetic-generation</ReferenceCode></FlexStatementResponse>`,
									),
								),
							}, nil
						}),
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}

			request := provider.Request{
				Stream:   key.Stream,
				Settings: connection.Settings,
				State:    before.State,
				Now:      next.Add(time.Minute),
			}
			access := repairAccess{next: request.Now.Add(time.Minute)}
			if _, err = registration.Acquirer().
				Acquire(ctx, request, access); !errors.Is(err, provider.ErrInvalid) ||
				calls != 0 {
				t.Fatal("fixture did not reproduce incompatible continuation", err, calls)
			}

			request.State = after.State
			acquired, err := registration.Acquirer().Acquire(ctx, request, access)
			if err != nil || calls != 1 || acquired.State().IsZero() {
				t.Fatal("repaired acquisition failed", err, calls)
			}
		})
	}
}

type repairTransport func(*http.Request) (*http.Response, error)

func (f repairTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type repairAccess struct {
	provider.Access
	next time.Time
}

func (repairAccess) WithSecret(_ context.Context, _ string, fn func([]byte) error) error {
	return fn([]byte("synthetic-token"))
}
func (repairAccess) Report(context.Context, provider.Progress) error { return nil }

func (a repairAccess) ReservePacing(
	context.Context,
	string,
	provider.PacingScope,
) (provider.Reservation, error) {
	return provider.NewReservation(true, a.next)
}

func TestIdlePollingDoesNotLog(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)

	var output bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))

	h, err := host.New(ctx, s, nil, logger, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	refs, err := reference.New(s, nil, nil, "", nil, logger)
	if err != nil {
		t.Fatal(err)
	}

	workflow := application.New(s, h, refs, nil, logger)
	for range 100 {
		if err := workflow.RunOnce(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	if output.Len() != 0 {
		t.Fatal("idle polling produced logs", output.String())
	}
}
