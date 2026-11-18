// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package runner

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFollowerRejectsBadStartup(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		script string
		want   string
	}{
		{"missing executable", "", "no such file or directory"},
		{"malformed output", `printf '{\n'`, "invalid adapter output"},
		{"unexpected commit", `printf '%s\n' '{"protocol":1,"event":"committed","baseline_id":"baseline","version":"wrong"}'; exec sleep 30`, "follower committed an unexpected publication"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			consumer := filepath.Join(dir, "adapter")
			if test.script != "" {
				// #nosec G306 -- The fixed test script must be executable by its owner.
				if err := os.WriteFile(consumer, []byte("#!/bin/sh\n"+test.script+"\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}

			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()

			target := &url.URL{Scheme: "http", Host: "127.0.0.1:1"}
			view := publishedView{publication: publication{BaselineID: "baseline", Version: "expected"}}

			follow, err := startFollower(
				ctx, adapterClient{consumer: consumer, stateDir: dir, profile: "history"},
				target, nil, view,
			)
			if follow != nil {
				follow.close()
			}

			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("startFollower returned %v, want %q", err, test.want)
			}
		})
	}
}

func TestFollowerReconnectIgnoresPreviousCommit(t *testing.T) {
	t.Parallel()

	before := publication{BaselineID: "baseline", Version: "first"}
	next := publication{BaselineID: "baseline", Version: "next"}

	follow := &follower{before: before, commits: make(chan publication, 2)}
	follow.commits <- before

	follow.commits <- next

	if err := follow.awaitCommit(t.Context(), next); err != nil {
		t.Fatal(err)
	}

	if len(follow.commits) != 0 {
		t.Fatal("follower accepted publication before consuming the new commit")
	}
}

func TestFollowerWaitsReportExitAndCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	follow := &follower{finished: make(chan struct{})}
	if err := follow.awaitCommit(ctx, publication{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("commit wait returned %v, want cancellation", err)
	}

	if err := follow.awaitSubscription(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("subscription wait returned %v, want cancellation", err)
	}

	exit := errors.New("adapter exited")
	follow.err = exit
	close(follow.finished)

	if err := follow.awaitSubscription(t.Context(), nil); !errors.Is(err, exit) {
		t.Fatalf("subscription wait returned %v, want the adapter failure", err)
	}
}
