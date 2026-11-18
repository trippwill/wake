// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type processError string

func (e processError) Error() string { return string(e) }

const errForcedShutdown processError = "daemon exceeded its shutdown grace period"

type supervisedProcess struct {
	cmd    *exec.Cmd
	stdout *os.File

	cancel        context.CancelFunc
	shutdownGrace time.Duration
	exited        chan struct{}
	joined        chan struct{}
	waitOnce      sync.Once
	waitErr       error
	stopErr       error
	groupMu       sync.Mutex
	groupClosed   bool
}

func startProcess(
	ctx context.Context,
	cmd *exec.Cmd,
	shutdownGrace time.Duration,
) (*supervisedProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Own the read end so cancellation can unblock callers before they call Wait.
	stdout, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = writer.Close() }()

	cmd.Stdout = writer
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}

	cmd.SysProcAttr.Setpgid = true

	if err = cmd.Start(); err != nil {
		_ = stdout.Close()
		return nil, err
	}

	stopCtx, cancel := context.WithCancel(ctx)
	p := &supervisedProcess{
		cmd: cmd, stdout: stdout, cancel: cancel, shutdownGrace: shutdownGrace,
		exited: make(chan struct{}), joined: make(chan struct{}),
	}
	go p.monitor(ctx, stopCtx)

	return p, nil
}

func (p *supervisedProcess) signal(sig syscall.Signal) error {
	p.groupMu.Lock()
	defer p.groupMu.Unlock()

	if p.shutdownGrace != 0 || p.groupClosed {
		return os.ErrProcessDone
	}

	return syscall.Kill(-p.cmd.Process.Pid, sig)
}

func (p *supervisedProcess) closeGroup(owned bool) error {
	p.groupMu.Lock()
	defer p.groupMu.Unlock()

	if p.groupClosed {
		return nil
	}

	p.groupClosed = true

	if owned {
		if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}

	return nil
}

func (p *supervisedProcess) monitor(ctx, stopCtx context.Context) {
	defer close(p.joined)

	select {
	case <-p.exited:
		return
	case <-stopCtx.Done():
	}

	if p.shutdownGrace == 0 {
		p.stopErr = errors.Join(ctx.Err(), p.closeGroup(true))
		_ = p.stdout.Close()

		return
	}

	err := p.cmd.Process.Signal(syscall.SIGTERM)
	_ = p.stdout.Close()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		p.stopErr = err
	}

	timer := time.NewTimer(p.shutdownGrace)
	defer timer.Stop()

	select {
	case <-p.exited:
	case <-timer.C:
		err = p.cmd.Process.Kill()
		if !errors.Is(err, os.ErrProcessDone) {
			p.stopErr = errors.Join(p.stopErr, errForcedShutdown, err)
		}
	}
}

func (p *supervisedProcess) waitForAdapterExit() error {
	var info unix.Siginfo
	for {
		// Observe exit without releasing the launcher PID, which also identifies the group.
		err := unix.Waitid(unix.P_PID, p.cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}

		return errors.Join(err, p.closeGroup(!errors.Is(err, unix.ECHILD)))
	}
}

func (p *supervisedProcess) wait() error {
	p.waitOnce.Do(func() {
		var groupErr error
		if p.shutdownGrace == 0 {
			groupErr = p.waitForAdapterExit()
		}

		err := p.cmd.Wait()
		close(p.exited)
		<-p.joined
		p.cancel()
		_ = p.stdout.Close()
		p.waitErr = errors.Join(p.stopErr, groupErr, err)
	})

	return p.waitErr
}

func (p *supervisedProcess) stop() error {
	p.cancel()

	return p.wait()
}
