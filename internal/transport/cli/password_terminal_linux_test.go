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

type passwordPrompts struct {
	bytes.Buffer
	prompt chan struct{}
}

func (output *passwordPrompts) Write(value []byte) (int, error) {
	n, err := output.Buffer.Write(value)
	if strings.HasSuffix(string(value), ": ") {
		output.prompt <- struct{}{}
	}

	return n, err
}

func (output *passwordPrompts) WriteString(value string) (int, error) {
	return output.Write([]byte(value))
}

func TestSetPasswordThroughTerminal(t *testing.T) {
	dir := setupEnvironment(t)
	writeTestConfig(t, filepath.Join(dir, "config.toml"), nil)

	master, slave := openPasswordTerminal(t)

	original := os.Stdin
	os.Stdin = slave
	defer func() { os.Stdin = original }()

	set := func(first, second string, want int, diagnostic string) {
		t.Helper()

		output := &passwordPrompts{prompt: make(chan struct{}, 2)}
		done := make(chan int, 1)
		go func() { done <- run([]string{"access", "set", "password"}, output, output) }()

		inputs := []string{first}
		if second != "" {
			inputs = append(inputs, second)
		}

		for _, input := range inputs {
			select {
			case <-output.prompt:
			case code := <-done:
				t.Fatal("password command ended before prompting", code, output.String())
			case <-time.After(5 * time.Second):
				t.Fatal("password command did not prompt")
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
			t.Fatal("password command did not finish")
		}
	}

	set("synthetic first unlock", "synthetic first unlock", exitOK, "Password set")

	handler := testAdminHandler(t, dir)
	cookie := testLogin(t, handler, "synthetic first unlock", http.StatusSeeOther)

	set("short", "short", exitFailure, "at least 12 bytes")
	set("synthetic first unlock", "synthetic second unlock", exitFailure, "do not match")
	testLogin(t, testAdminHandler(t, dir), "synthetic first unlock", http.StatusSeeOther)

	set("synthetic second unlock", "synthetic second unlock", exitOK, "Password set")
	testLogin(t, testAdminHandler(t, dir), "synthetic second unlock", http.StatusSeeOther)

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/", nil)
	request.AddCookie(cookie)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatal("accepted a session issued before setting the new unlock", response.Code)
	}

	for _, size := range []int{admin.MaxPasswordBytes + 1, admin.MaxPasswordBytes + 2} {
		set(strings.Repeat("&", size), "", exitFailure, "exceeds the input limit")
	}

	longest := strings.Repeat("&", admin.MaxPasswordBytes)
	set(longest, longest, exitOK, "Password set")
	testLogin(t, testAdminHandler(t, dir), longest, http.StatusSeeOther)
}

func TestSetPasswordRejectsNonterminalInput(t *testing.T) {
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
	if code := run([]string{"access", "set", "password"}, &stdout, &stderr); code != exitUsage ||
		!strings.Contains(stderr.String(), "setting a password requires a terminal") {
		t.Fatal(code, stderr.String())
	}
}

func openPasswordTerminal(t *testing.T) (*os.File, *os.File) {
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
