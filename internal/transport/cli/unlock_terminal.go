// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/urfave/cli/v3"
	"go.trippwill.dev/wake/internal/transport/admin"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type setupError string

func (e setupError) Error() string { return string(e) }

const (
	errTerminalRequired setupError = "standard input is not a terminal"
	errUnlockMismatch   setupError = "operator unlocks do not match"
)

func readTerminalInput(ctx context.Context, c *cli.Command, prompt string) ([]byte, error) {
	fd := int(os.Stdin.Fd())

	state, err := term.GetState(fd)
	if err != nil {
		return nil, err
	}

	attrs, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}

	attrs.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, attrs); err != nil {
		return nil, err
	}

	defer func() { _ = term.Restore(fd, state) }()

	if fd < 0 || fd > math.MaxInt32 {
		return nil, errTerminalRequired
	}

	if c.Bool("json") {
		_ = json.NewEncoder(c.ErrWriter).
			Encode(map[string]any{"event": "input_required", "prompt": prompt, "secret": true})
	} else {
		writeString(c.ErrWriter, prompt)
	}

	polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}

	content := make([]byte, 0, 128)
	defer func() { clear(content) }()

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		n, err := unix.Poll(polls, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}

		if err != nil {
			return nil, err
		}

		if n == 0 {
			continue
		}

		var buffer [admin.MaxUnlockBytes]byte

		n, err = unix.Read(fd, buffer[:])
		if errors.Is(err, unix.EINTR) {
			continue
		}

		if err != nil {
			return nil, err
		}

		if n == 0 {
			return nil, io.EOF
		}

		content = append(content, buffer[:n]...)
		clear(buffer[:])

		if len(content) > admin.MaxUnlockBytes {
			return nil, fmt.Errorf("input too long")
		}

		if bytes.IndexByte(content, '\n') >= 0 {
			if !c.Bool("json") {
				writeString(c.ErrWriter, "\n")
			}

			return bytes.Clone(bytes.TrimSuffix(bytes.TrimSuffix(content, []byte("\n")), []byte("\r"))), nil
		}
	}
}

func readOperatorUnlock(ctx context.Context, c *cli.Command) ([]byte, error) {
	first, err := readTerminalInput(ctx, c, "Operator unlock: ")
	if err != nil {
		return nil, err
	}

	second, err := readTerminalInput(ctx, c, "Confirm operator unlock: ")
	if err != nil {
		clear(first)
		clear(second)

		return nil, err
	}

	if subtle.ConstantTimeCompare(first, second) != 1 {
		clear(first)
		clear(second)

		return nil, errUnlockMismatch
	}

	clear(second)

	if len(first) < admin.MinimumUnlockBytes {
		clear(first)
		return nil, admin.ErrWeakUnlock
	}

	return first, nil
}
