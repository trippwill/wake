// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build linux

package credential

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestLoadMasterKey(t *testing.T) {
	path := writeMasterKeyFile(t, []byte("12345678901234567890123456789012"), 0o600)

	key, err := LoadMasterKey(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(key.Clear)

	envelope, err := NewEnvelope(key)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(envelope.Clear)

	secret := NewSecret([]byte("flex-token"))
	t.Cleanup(secret.Clear)

	encrypted, err := envelope.Encrypt(secret, []byte("credential/ibkr-flex"))
	if err != nil {
		t.Fatal(err)
	}

	opened, err := envelope.Decrypt(encrypted, []byte("credential/ibkr-flex"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(opened.Clear)

	plaintext, err := opened.Copy()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plaintext)

	if !bytes.Equal(plaintext, []byte("flex-token")) {
		t.Fatalf("decrypted secret = %q", plaintext)
	}
}

func TestCreateMasterKeyCreatesPrivateVerifiedMaterial(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(directory, "master.key")
	if err := CreateMasterKey(path); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o400 {
		t.Fatalf("master-key mode = %#o, want 0400", info.Mode().Perm())
	}

	key, err := LoadMasterKey(path)
	if err != nil {
		t.Fatal(err)
	}

	key.Clear()

	if err = CreateMasterKey(path); !errors.Is(err, ErrMasterKeyExists) {
		t.Fatalf("duplicate CreateMasterKey() error = %v, want ErrMasterKeyExists", err)
	}
}

func TestCreateMasterKeyRejectsUnsafeTarget(t *testing.T) {
	if err := CreateMasterKey("master.key"); !errors.Is(err, ErrInvalidMasterKeyPath) {
		t.Fatalf("relative CreateMasterKey() error = %v", err)
	}

	unsafe := t.TempDir()
	//nolint:gosec // This test deliberately creates an unsafe parent directory.
	if err := os.Chmod(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := CreateMasterKey(filepath.Join(unsafe, "master.key")); !errors.Is(err, ErrInsecureMasterKeyFile) {
		t.Fatalf("unsafe CreateMasterKey() error = %v", err)
	}
}

func TestLoadMasterKeyRejectsInvalidPathAndMaterial(t *testing.T) {
	if _, err := LoadMasterKey("key"); !errors.Is(err, ErrInvalidMasterKeyPath) {
		t.Fatalf("relative path error = %v", err)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := LoadMasterKey(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error = %v", err)
	}

	for _, material := range [][]byte{make([]byte, dataKeySize-1), make([]byte, dataKeySize+1)} {
		path := writeMasterKeyFile(t, material, 0o400)
		if _, err := LoadMasterKey(path); !errors.Is(err, ErrInvalidMasterKey) {
			t.Fatalf("LoadMasterKey(%d bytes) error = %v", len(material), err)
		}
	}
}

func TestLoadMasterKeyRejectsInsecureFiles(t *testing.T) {
	key := []byte("12345678901234567890123456789012")
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				t.Helper()

				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symbolic link",
			setup: func(t *testing.T, path string) {
				t.Helper()

				target := writeMasterKeyFile(t, key, 0o400)
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "named pipe",
			setup: func(t *testing.T, path string) {
				t.Helper()

				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "group readable",
			setup: func(t *testing.T, path string) {
				t.Helper()
				writeMasterKeyFileAt(t, path, key, 0o440)
			},
		},
		{
			name: "other readable",
			setup: func(t *testing.T, path string) {
				t.Helper()
				writeMasterKeyFileAt(t, path, key, 0o404)
			},
		},
		{
			name: "not owner readable",
			setup: func(t *testing.T, path string) {
				t.Helper()
				writeMasterKeyFileAt(t, path, key, 0o200)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "master-key")
			test.setup(t, path)

			if _, err := LoadMasterKey(path); !errors.Is(err, ErrInsecureMasterKeyFile) {
				t.Fatalf("LoadMasterKey() error = %v", err)
			}
		})
	}
}

func TestLoadMasterKeyRejectsForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing file ownership requires root")
	}

	path := writeMasterKeyFile(t, []byte("12345678901234567890123456789012"), 0o400)
	if err := os.Chown(path, 1, -1); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadMasterKey(path); !errors.Is(err, ErrInsecureMasterKeyFile) {
		t.Fatalf("foreign owner error = %v", err)
	}
}

func writeMasterKeyFile(t *testing.T, material []byte, mode os.FileMode) string {
	t.Helper()
	return writeMasterKeyFileAt(t, filepath.Join(t.TempDir(), "master-key"), material, mode)
}

func writeMasterKeyFileAt(t *testing.T, path string, material []byte, mode os.FileMode) string {
	t.Helper()

	if err := os.WriteFile(path, material, mode); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}

	return path
}
