// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"go.trippwill.dev/wake/internal/clientaccess"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/devenv"
	"go.trippwill.dev/wake/internal/devfixture"
)

type marker struct {
	Magic    string `json:"magic"`
	Revision string `json:"revision"`
	Dirty    bool   `json:"dirty"`
	Digest   string `json:"fixture_digest"`
}

const childWorkspaceLockFD = 3

func identity() (marker, error) {
	m := marker{Magic: "wake-dev/1"}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return m, errors.New("missing build information")
	}

	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			m.Revision = s.Value
		case "vcs.modified":
			m.Dirty = s.Value == "true"
		}
	}

	if m.Revision == "" {
		return m, errors.New("build wake-dev with VCS stamping from a pinned checkout")
	}

	h := sha256.New()
	for _, name := range []string{"activity/realized.xml", "confirmations/initial.xml", "confirmations/overlap.xml"} {
		data, e := devfixture.ReadReport(name)
		if e != nil {
			return m, e
		}

		h.Write(data)
	}

	m.Digest = fmt.Sprintf("%x", h.Sum(nil))

	return m, nil
}

func Run(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New("expected serve, accept or reset")
	}

	if args[0] == "child" && len(args) == 3 {
		return runChild(ctx, args[1], args[2])
	}

	op := args[0]
	if op != "serve" && op != "reset" && op != "accept" {
		return errors.New("unknown operation")
	}

	flags := flag.NewFlagSet(op, flag.ContinueOnError)
	workspace := flags.String("workspace", "", "absolute private directory")
	confirm := false
	if op == "reset" {
		flags.BoolVar(&confirm, "confirm", false, "remove managed data")
	}

	var (
		consumer, profiles string
		timeout            time.Duration
	)
	if op == "accept" {
		flags.StringVar(&consumer, "consumer", "", "adapter executable")
		flags.StringVar(&profiles, "profile", "history,positions", "profiles")
		flags.DurationVar(&timeout, "timeout", 3*time.Minute, "total deadline")
	}

	if e := flags.Parse(args[1:]); e != nil {
		return e
	}

	if flags.NArg() != 0 || !filepath.IsAbs(*workspace) || filepath.Clean(*workspace) != *workspace ||
		*workspace == "/" {
		return errors.New("expected absolute workspace")
	}

	if op == "accept" {
		if !filepath.IsAbs(consumer) || timeout <= 0 {
			return errors.New("invalid consumer or timeout")
		}

		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	if op != "reset" {
		parent := filepath.Dir(*workspace)
		resolved, err := filepath.EvalSymlinks(parent)
		if err != nil || resolved != parent {
			return errors.New("unsafe workspace parent")
		}
		// #nosec G703 -- The caller explicitly selected this absolute workspace.
		if e := os.Mkdir(*workspace, 0o700); e != nil && !errors.Is(e, os.ErrExist) {
			return e
		}
	}

	real, e := filepath.EvalSymlinks(*workspace)
	if e != nil || real != *workspace {
		return errors.New("unsafe workspace")
	}

	if e = config.CheckPrivateDir(real); e != nil {
		return e
	}

	// #nosec G703 -- Open the verified private directory to lock its inode.
	root, e := os.Open(real)
	if e != nil {
		return e
	}

	defer func() { _ = root.Close() }()

	if e = syscall.Flock(int(root.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("workspace busy")
	}

	dir := filepath.Join(real, ".wake-dev")

	m, e := identity()
	if e != nil {
		return e
	}

	anchored, e := os.OpenRoot(real)
	if e != nil {
		return e
	}
	defer func() { _ = anchored.Close() }()

	mode := "resume"
	if _, e = anchored.Lstat(".wake-dev"); errors.Is(e, os.ErrNotExist) && op != "reset" {
		if e = anchored.Mkdir(".wake-dev", 0o700); e != nil {
			return e
		}

		mode = "new"

		data, _ := json.Marshal(m)
		if e = anchored.WriteFile(".wake-dev/owner.json", data, 0o600); e != nil {
			return e
		}
	} else {
		if e = config.CheckPrivateDir(dir); e != nil {
			return e
		}

		info, e := anchored.Lstat(".wake-dev/owner.json")
		if e != nil || !info.Mode().IsRegular() {
			return errors.New("invalid workspace ownership marker")
		}

		// #nosec G304 -- Fixed marker name under the checked private workspace.
		data, e := anchored.ReadFile(".wake-dev/owner.json")

		var saved marker
		if e != nil || json.Unmarshal(data, &saved) != nil || saved.Magic != m.Magic {
			return errors.New("invalid workspace ownership marker")
		}

		if e = filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			info, e := entry.Info()
			if e != nil {
				return e
			}

			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok || int(st.Uid) != os.Geteuid() ||
				(!info.IsDir() && (!info.Mode().IsRegular() || st.Nlink != 1)) {
				return errors.New("unsafe linked or foreign workspace entry")
			}

			return nil
		}); e != nil {
			return e
		}

		if op == "reset" {
			if confirm {
				if e = anchored.RemoveAll(".wake-dev"); e != nil {
					return e
				}
			}

			return json.NewEncoder(os.Stdout).Encode(map[string]any{"path": dir, "removed": confirm})
		}

		if saved != m || op == "accept" {
			return errors.New("workspace requires reset")
		}
	}

	stages := []string{"initial"}
	if op == "accept" {
		stages = append(
			stages,
			"restart",
			"acquisition",
			"source-conflict",
			"source-resolution",
			"identity-merge",
			"correction",
			"access-recovery",
			"financial-restore",
			"replacement",
		)
	}

	if op == "accept" && strings.Contains(profiles, "history") {
		stages = append(stages, "empty-warehouse")
	}

	suite := &acceptanceState{followers: make(map[string]*follower)}
	defer suite.close()

	for _, stage := range stages {
		if stage != "initial" {
			mode = "resume"

			if stage == "empty-warehouse" {
				profiles = "history"
			}

			if stage == "access-recovery" || stage == "financial-restore" || stage == "empty-warehouse" {
				mode = "local"
			}

			if e = devenv.Advance(ctx, dir, stage); e != nil {
				return e
			}
		}

		if e = runStage(ctx, op, consumer, profiles, dir, real, mode, stage, m, suite, root); e != nil {
			return e
		}

		if op == "accept" {
			if e = json.NewEncoder(os.Stdout).
				Encode(map[string]string{"event": "scenario", "scenario": stage, "status": "passed"}); e != nil {
				return e
			}
		}
	}

	return json.NewEncoder(os.Stdout).Encode(map[string]string{"event": "complete"})
}

func runChild(ctx context.Context, dir, mode string) error {
	if os.Getenv("WAKE_DEV_CHILD") != "1" {
		return errors.New("child requires supervisor")
	}

	lock := os.NewFile(childWorkspaceLockFD, "workspace lock")
	if lock == nil {
		return errors.New("child requires inherited workspace lock")
	}
	defer func() { _ = lock.Close() }()

	info, err := lock.Stat()
	if err != nil || !info.IsDir() {
		return errors.New("invalid inherited workspace lock")
	}

	parent, err := os.Lstat(filepath.Dir(dir))
	if err != nil || !os.SameFile(info, parent) {
		return errors.New("inherited lock does not match workspace")
	}

	if err = syscall.Flock(childWorkspaceLockFD, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("workspace busy")
	}

	syscall.CloseOnExec(childWorkspaceLockFD)

	return devenv.Session(ctx, dir, mode == "new", mode == "local", json.NewEncoder(os.Stdout))
}

func daemonCommand(exe, dir, mode string, lock *os.File) *exec.Cmd {
	// #nosec G204 G702 -- Re-execute the current binary with an explicit argument vector.
	cmd := exec.Command(exe, "child", dir, mode)
	cmd.Env = []string{"WAKE_DEV_CHILD=1"}
	if coverDir := os.Getenv("GOCOVERDIR"); coverDir != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+coverDir)
	}

	// Share the locked open file description with the daemon until it finishes shutdown.
	cmd.ExtraFiles = []*os.File{lock}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	cmd.WaitDelay = 10 * time.Second

	return cmd
}

func runStage(
	ctx context.Context,
	op, consumer, profiles, dir, real, mode, stage string,
	m marker,
	suite *acceptanceState,
	lock *os.File,
) (result error) {
	exe, e := os.Executable()
	if e != nil {
		return e
	}

	cmd := daemonCommand(exe, dir, mode, lock)

	// #nosec G304 G703 -- Use the fixed log name in the locked private workspace and refuse symlinks.
	logFile, e := os.OpenFile(
		filepath.Join(dir, "daemon.log"),
		os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW,
		0o600,
	)
	if e != nil {
		return e
	}

	defer func() { _ = logFile.Close() }()

	cmd.Stderr = io.MultiWriter(logFile, os.Stderr)

	proc, e := startProcess(ctx, cmd, 10*time.Second)
	if e != nil {
		return e
	}

	waited := false
	defer func() {
		if !waited {
			if err := proc.stop(); err != nil {
				result = errors.Join(result, fmt.Errorf("daemon shutdown: %w", err))
			}
		}

		if err := ctx.Err(); op == "accept" && err != nil && !errors.Is(result, err) {
			result = errors.Join(result, err)
		}
	}()

	var ready map[string]string
	if e = json.NewDecoder(proc.stdout).Decode(&ready); e != nil {
		return e
	}

	token, e := clientaccess.ReadTokenFile(ready["token_file"])
	if e != nil {
		return e
	}
	defer token.Clear()

	v, e := token.Copy()
	if e != nil {
		return e
	}
	defer clear(v)

	req, e := http.NewRequestWithContext(ctx, http.MethodGet, ready["endpoint"]+"/v1/snapshot", nil)
	if e != nil {
		return e
	}

	req.Header.Set("Authorization", "Bearer "+string(v))

	client := http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()

	res, e := client.Do(req)
	if e != nil {
		return e
	}

	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return errors.New("readiness probe failed")
	}

	if e = json.NewEncoder(os.Stdout).
		Encode(map[string]any{"event": "ready", "protocol": 1, "workspace": real, "endpoint": ready["endpoint"], "token_file": ready["token_file"], "revision": m.Revision, "dirty": m.Dirty, "fixture_digest": m.Digest}); e != nil {
		return e
	}

	if op == "accept" {
		return acceptance(ctx, consumer, profiles, dir, ready, res.Body, &client, req.Header, stage, suite)
	}

	waited = true

	return proc.wait()
}
