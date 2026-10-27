// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/urfave/cli/v3"
	"golang.org/x/sys/unix"
)

func TestSetupInputCancellationRestoresTerminal(t *testing.T) {
	master, slave := openUnlockTerminal(t)
	defer func() { _ = master.Close() }()

	original := os.Stdin
	os.Stdin = slave
	defer func() { os.Stdin = original }()

	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &cli.Command{Writer: io.Discard, ErrWriter: io.Discard}
	done := make(chan error, 1)
	go func() { value, err := readTerminalInput(ctx, c, "unlock", true); clear(value); done <- err }()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}

		after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
		if err != nil || before.Lflag != after.Lflag {
			t.Fatalf("terminal state was not restored: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal input ignored cancellation")
	}
}
