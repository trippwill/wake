// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package runner

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAdapterTerminalProtocol(t *testing.T) {
	const (
		complete  = `{"protocol":1,"event":"complete"}`
		failed    = `{"protocol":1,"event":"failed"}`
		committed = `{"protocol":1,"event":"committed","baseline_id":"b","version":"v"}`
	)

	for _, tc := range []struct {
		name, operation, output, wantError string
		fault                              bool
	}{
		{name: "sync commits then completes", operation: "sync", output: committed + "\n" + complete},
		{name: "fault reports failure", operation: "sync", output: failed, fault: true},
		{name: "fault cannot succeed", operation: "sync", output: complete, fault: true, wantError: "terminal"},
		{name: "fault cannot commit", operation: "sync", output: committed + "\n" + failed, fault: true, wantError: "commit"},
		{name: "export cannot commit", operation: "export", output: committed + "\n" + complete, wantError: "commit"},
		{name: "commit needs version", operation: "sync", output: `{"protocol":1,"event":"committed","baseline_id":"b"}`, wantError: "commit"},
		{name: "output after terminal", operation: "sync", output: complete + "\n" + committed, wantError: "invalid adapter output"},
		{name: "unknown progress", operation: "sync", output: `{"protocol":1,"event":"progress","phase":"complete"}`, wantError: "progress"},
		{name: "unknown event", operation: "sync", output: `{"protocol":1,"event":"installed"}`, wantError: "unknown adapter event"},
		{name: "incomplete stream", operation: "follow", output: committed, wantError: "missing adapter completion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := readAdapter(
				strings.NewReader(tc.output),
				tc.operation,
				tc.fault,
				func(adapterEvent) error { return nil },
			)
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected error containing %q, got %v", tc.wantError, err)
			}
		})
	}
}

func TestAcceptanceRejectsWrongStagePublication(t *testing.T) {
	const current = `{"mode":"full","baseline_id":"baseline","version":"2","resources":[]}`
	for _, tc := range []struct {
		stage, baseline, version, want string
	}{
		{"restart", "baseline", "1", "restart or access recovery changed publication"},
		{"access-recovery", "other", "2", "restart or access recovery changed publication"},
		{"replacement", "baseline", "1", "replacement retained obsolete baseline"},
		{"acquisition", "baseline", "2", "fixture update did not advance publication"},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			suite := acceptanceState{
				history: publishedView{publication: publication{BaselineID: tc.baseline, Version: tc.version}},
			}
			err := acceptance(
				t.Context(),
				"unused",
				"history",
				t.TempDir(),
				nil,
				strings.NewReader(current),
				http.DefaultClient,
				nil,
				tc.stage,
				&suite,
			)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

func TestAdapterReportsCommittedPublicationAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name, operation, output string
		fault                   bool
		wantError               string
	}{
		{"wrong publication", "sync", `{"protocol":1,"event":"committed","baseline_id":"b","version":"older"}`, false, "incorrect committed publication"},
		{"failed event needs nonzero exit", "sync", `{"protocol":1,"event":"failed"}`, true, "did not exit with a failure"},
		{"inaccurate positions export", "export", `{"protocol":1,"event":"complete","view":{"positions":null}}`, false, "inaccurate positions export"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "adapter")
			script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '" + tc.output + "'\n"
			// #nosec G306 -- This temporary adapter is executable only by its owner.
			if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}

			adapter := adapterClient{consumer: path, stateDir: dir, profile: "positions"}
			want := publishedView{
				publication: publication{BaselineID: "b", Version: "new"},
				raw:         []byte(`{"positions":[]}`),
			}

			err := adapter.run(t.Context(), tc.operation, "", want, tc.fault)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected %q, got %v", tc.wantError, err)
			}
		})
	}
}

func TestRunRejectsInvalidArgumentsBeforeCreatingWorkspace(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "new")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "expected serve, accept or reset"},
		{[]string{"unsupported"}, "unknown operation"},
		{[]string{"serve", "--unknown"}, "flag provided but not defined"},
		{[]string{"serve", "--workspace", "relative"}, "expected absolute workspace"},
		{[]string{"accept", "--workspace", workspace, "--consumer", "relative"}, "invalid consumer or timeout"},
		{[]string{"accept", "--workspace", workspace, "--consumer", "/unused", "--timeout", "0s"}, "invalid consumer or timeout"},
	} {
		if err := Run(t.Context(), tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("Run(%v): expected %q, got %v", tc.args, tc.want, err)
		}
	}

	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("invalid arguments affected workspace: %v", err)
	}
}

func TestRunRejectsWorkspaceAccessibleToOtherUsers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	// #nosec G302 -- Make the empty test directory deliberately nonprivate, independent of umask.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	err := Run(t.Context(), []string{"serve", "--workspace", dir})
	if err == nil || !strings.Contains(err.Error(), "must not be accessible by group or other users") {
		t.Fatalf("expected workspace permission failure, got %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".wake-dev")); !os.IsNotExist(err) {
		t.Fatalf("unsafe workspace was initialized: %v", err)
	}
}

func TestReadAdapterRejectsOutputBeyondLimit(t *testing.T) {
	stream := `{"protocol":1,"event":"complete"}` + strings.Repeat(" ", 16<<20)

	err := readAdapter(strings.NewReader(stream), "sync", false, func(adapterEvent) error { return nil })
	if err == nil || err.Error() != "invalid adapter output size" {
		t.Fatalf("expected output size failure, got %v", err)
	}
}
