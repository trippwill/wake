// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package clientaccess

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestTokenFileBoundary(t *testing.T) {
	value := base64.RawURLEncoding.EncodeToString(
		make([]byte, 16),
	) + "." + base64.RawURLEncoding.EncodeToString(
		make([]byte, 32),
	)
	for _, tc := range []struct {
		name, value string
		mode        os.FileMode
		valid       bool
	}{
		{"private", value, 0o600, true},
		{"read only", value + "\n", 0o400, true},
		{"CRLF", value + "\r\n", 0o600, true},
		{"shared", value, 0o640, false},
		{"world", value, 0o604, false},
		{"empty", "", 0o600, false},
		{"multiple", value + "\n" + value, 0o600, false},
		{"space", value + " ", 0o600, false},
		{"oversized", strings.Repeat("x", 1024), 0o600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte(tc.value), tc.mode); err != nil {
				t.Fatal(err)
			}

			secret, err := ReadTokenFile(path)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}

				defer secret.Clear()

				data, err := secret.Copy()
				defer clear(data)

				if err != nil || string(data) != value {
					t.Fatal("token changed", err)
				}
			} else if !errors.Is(err, ErrTokenFile) {
				t.Fatal("accepted unsafe token file", err)
			}
		})
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	link := filepath.Join(dir, "link")
	fifo := filepath.Join(dir, "fifo")

	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}

	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"relative", dir, link, fifo, filepath.Join(dir, "missing")} {
		if _, err := ReadTokenFile(path); !errors.Is(err, ErrTokenFile) {
			t.Fatal("accepted unsafe path", path, err)
		}
	}
}
