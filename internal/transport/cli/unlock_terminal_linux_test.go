// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.trippwill.dev/wake/internal/transport/admin"
	"golang.org/x/sys/unix"
)

type unlockPrompts struct {
	bytes.Buffer
	prompt chan struct{}
}

func (output *unlockPrompts) Write(value []byte) (int, error) {
	n, err := output.Buffer.Write(value)
	if strings.HasSuffix(string(value), ": ") {
		output.prompt <- struct{}{}
	}

	return n, err
}

func (output *unlockPrompts) WriteString(value string) (int, error) {
	return output.Write([]byte(value))
}

func TestSetUnlockThroughTerminal(t *testing.T) {
	dir := setupEnvironment(t)
	writeTestConfig(t, filepath.Join(dir, "config.toml"), nil)

	master, slave := openUnlockTerminal(t)

	original := os.Stdin
	os.Stdin = slave
	defer func() { os.Stdin = original }()

	set := func(first, second string, want int, diagnostic string) {
		t.Helper()

		output := &unlockPrompts{prompt: make(chan struct{}, 2)}
		done := make(chan int, 1)
		go func() { done <- run([]string{"admin", "set-unlock"}, output, output) }()

		inputs := []string{first}
		if second != "" {
			inputs = append(inputs, second)
		}

		for _, input := range inputs {
			select {
			case <-output.prompt:
			case code := <-done:
				t.Fatal("unlock command ended before prompting", code, output.String())
			case <-time.After(5 * time.Second):
				t.Fatal("unlock command did not prompt")
			}

			if _, err := fmt.Fprintln(master, input); err != nil {
				t.Fatal(err)
			}
		}

		select {
		case code := <-done:
			if code != want || !strings.Contains(output.String(), diagnostic) {
				t.Fatal(code, output.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("unlock command did not finish")
		}
	}

	set("synthetic first unlock", "synthetic first unlock", exitOK, "Operator unlock set")

	cookie := testLogin(t, dir, "synthetic first unlock", http.StatusSeeOther)

	set("short", "short", exitFailure, "at least 12 bytes")
	set("synthetic first unlock", "synthetic second unlock", exitFailure, "do not match")
	testLogin(t, dir, "synthetic first unlock", http.StatusSeeOther)

	set("synthetic second unlock", "synthetic second unlock", exitOK, "Operator unlock set")
	testLogin(t, dir, "synthetic second unlock", http.StatusSeeOther)

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/", nil)
	request.AddCookie(cookie)

	response := httptest.NewRecorder()
	testAdminHandler(t, dir).ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatal("accepted a session issued before setting the new unlock", response.Code)
	}

	for _, size := range []int{admin.MaxUnlockBytes + 1, admin.MaxUnlockBytes + 2} {
		set(strings.Repeat("&", size), "", exitFailure, "exceeds the input limit")
	}

	longest := strings.Repeat("&", admin.MaxUnlockBytes)
	set(longest, longest, exitOK, "Operator unlock set")
	testLogin(t, dir, longest, http.StatusSeeOther)
}

func TestSetUnlockRejectsNonterminalInput(t *testing.T) {
	dir := setupEnvironment(t)
	writeTestConfig(t, filepath.Join(dir, "config.toml"), nil)

	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = input.Close() }()

	original := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = original }()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"admin", "set-unlock"}, &stdout, &stderr); code != exitFailure ||
		!strings.Contains(stderr.String(), "not a terminal") {
		t.Fatal(code, stderr.String())
	}
}

func openUnlockTerminal(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = master.Close() })

	if err = unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}

	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // The kernel supplies the terminal number, never user input.
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = slave.Close() })

	return master, slave
}
