// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package runner

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAdapterShutdownKillsExitedLaunchersDescendants(t *testing.T) {
	for _, mode := range []string{"deadline", "stop"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			if mode == "deadline" {
				var cancel context.CancelFunc

				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}

			cmd := exec.Command("/bin/sh", "-c", `sleep 30 & printf '%s\n' "$!"`)

			proc, err := startProcess(ctx, cmd, 0)
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() { _ = proc.stop() })

			reader := bufio.NewReader(proc.stdout)

			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}

			pid, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil {
				t.Fatal(err)
			}

			launcher := processFD(t, cmd.Process.Pid)
			descendant := processFD(t, pid)

			waitForExit(t, launcher)

			if mode == "deadline" {
				if _, err = io.ReadAll(reader); err != nil && !errors.Is(err, os.ErrClosed) {
					t.Fatal(err)
				}

				if err = proc.wait(); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("wait returned %v, want deadline exceeded", err)
				}
			} else if err = proc.stop(); err != nil {
				t.Fatal(err)
			}

			waitForExit(t, descendant)
		})
	}
}

func processFD(t *testing.T, pid int) int32 {
	t.Helper()

	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = unix.Close(fd) })

	// #nosec G115 -- A Linux kernel file descriptor is a signed 32-bit integer.
	return int32(fd)
}

func waitForExit(t *testing.T, fd int32) {
	t.Helper()

	ready, err := unix.Poll([]unix.PollFd{{Fd: fd, Events: unix.POLLIN}}, 1500)
	if err != nil || ready != 1 {
		t.Fatalf("process did not exit, poll returned %d, %v", ready, err)
	}
}

func TestDaemonShutdownJoinsAndReportsExit(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		grace  time.Duration
		check  func(error) bool
	}{
		{
			name:   "clean",
			script: `trap 'exit 0' TERM; printf 'ready\n'; while :; do sleep .01; done`,
			grace:  2 * time.Second,
			check:  func(err error) bool { return err == nil },
		},
		{
			name:   "nonzero",
			script: `trap 'exit 7' TERM; printf 'ready\n'; while :; do sleep .01; done`,
			grace:  2 * time.Second,
			check: func(err error) bool {
				var exit *exec.ExitError

				return errors.As(err, &exit) && exit.ExitCode() == 7
			},
		},
		{
			name:   "forced",
			script: `trap '' TERM; printf 'ready\n'; exec sleep 30`,
			grace:  30 * time.Millisecond,
			check: func(err error) bool {
				var exit *exec.ExitError

				return errors.Is(err, errForcedShutdown) && errors.As(err, &exit) &&
					exit.Sys().(syscall.WaitStatus).Signal() == syscall.SIGKILL
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			// #nosec G204 -- Each script is a fixed case in this test.
			cmd := exec.Command("/bin/sh", "-c", test.script)

			proc, err := startProcess(context.Background(), cmd, test.grace)
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() { _ = proc.stop() })

			if _, err = bufio.NewReader(proc.stdout).ReadString('\n'); err != nil {
				t.Fatal(err)
			}

			if err = proc.stop(); !test.check(err) || cmd.ProcessState == nil {
				t.Fatalf("shutdown returned %v with process state %v", err, cmd.ProcessState)
			}
		})
	}
}
