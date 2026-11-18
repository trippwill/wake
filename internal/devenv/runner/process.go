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
)

type processError string

func (e processError) Error() string { return string(e) }

const errForcedShutdown processError = "daemon exceeded its shutdown grace period"

type supervisedProcess struct {
	cmd    *exec.Cmd
	stdout *os.File

	cancel   context.CancelFunc
	stopping chan struct{}
	exited   chan struct{}
	joined   chan struct{}
	waitOnce sync.Once
	waitErr  error
	stopErr  error
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
		cmd: cmd, stdout: stdout, cancel: cancel,
		stopping: make(chan struct{}), exited: make(chan struct{}), joined: make(chan struct{}),
	}
	go p.monitor(ctx, stopCtx, shutdownGrace)

	return p, nil
}

func (p *supervisedProcess) signal(sig syscall.Signal) error {
	select {
	case <-p.exited:
		return os.ErrProcessDone
	default:
		return syscall.Kill(-p.cmd.Process.Pid, sig)
	}
}

func (p *supervisedProcess) monitor(ctx, stopCtx context.Context, grace time.Duration) {
	defer close(p.joined)

	select {
	case <-p.exited:
		return
	case <-stopCtx.Done():
	}

	if grace == 0 {
		// Kill the group while the launcher can still be holding its process group ID.
		err := p.signal(syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			err = nil
		}

		p.stopErr = errors.Join(ctx.Err(), err)
		_ = p.stdout.Close()
		close(p.stopping)

		return
	}

	err := p.cmd.Process.Signal(syscall.SIGTERM)
	_ = p.stdout.Close()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		p.stopErr = err
	}

	close(p.stopping)

	timer := time.NewTimer(grace)
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

func (p *supervisedProcess) wait() error {
	p.waitOnce.Do(func() {
		err := p.cmd.Wait()
		close(p.exited)
		<-p.joined
		p.cancel()
		_ = p.stdout.Close()
		p.waitErr = errors.Join(p.stopErr, err)
	})

	return p.waitErr
}

func (p *supervisedProcess) stop() error {
	p.cancel()
	// Signal the group before Wait can reap a launcher that has already exited.
	select {
	case <-p.stopping:
	case <-p.exited:
	}

	return p.wait()
}
