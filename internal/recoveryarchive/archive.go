// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

// Package recoveryarchive encodes and verifies Wake's recovery archive streams.
package recoveryarchive

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

type archiveError string

func (e archiveError) Error() string { return string(e) }

const (
	ErrScope        archiveError = "wrong recovery archive scope"
	ErrPairedBackup archiveError = "recovery archive contains multiple stores"
	ErrCorrupt      archiveError = "invalid recovery archive"
)

type ChecksumError struct {
	Member string
}

func (e ChecksumError) Error() string {
	if e.Member == "" {
		return "archive checksum mismatch"
	}

	return fmt.Sprintf("archive checksum mismatch for %s", e.Member)
}

const storeCount = 3

// SizedReader supplies exactly Size bytes from Reader.
type SizedReader struct {
	Reader io.Reader
	Size   int64
}

func writeMember(archive *tar.Writer, name string, input SizedReader) (string, error) {
	hash := sha256.New()
	if err := writeEntry(
		archive,
		name,
		SizedReader{Reader: io.TeeReader(input.Reader, hash), Size: input.Size},
	); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeEntry(archive *tar.Writer, name string, input SizedReader) error {
	if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: input.Size}); err != nil {
		return err
	}

	_, err := io.Copy(archive, input.Reader)

	return err
}

func isRegularEntry(entry *tar.Header, name string) bool {
	if entry.Name != name || entry.Typeflag != tar.TypeReg || entry.Size < 0 {
		return false
	}

	// Go's tar reader can expand GNU sparse PAX members with type TypeReg.
	for key := range entry.PAXRecords {
		if strings.HasPrefix(key, "GNU.sparse.") {
			return false
		}
	}

	return true
}

// readMember checks that the consumer read the entire non-sparse member.
func readMember(archive *tar.Reader, size int64, consume func(io.Reader) error) (string, error) {
	hash := sha256.New()

	member := io.LimitedReader{R: io.TeeReader(archive, hash), N: size}
	if err := consume(&member); err != nil {
		return "", err
	}

	if member.N != 0 {
		return "", io.ErrUnexpectedEOF
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func discardMember(member io.Reader) error {
	//nolint:gosec // The validated non-sparse member cannot expand beyond its archive bytes.
	_, err := io.Copy(io.Discard, member)

	return err
}

func readChecksums(archive *tar.Reader, name, expected string) error {
	entry, err := archive.Next()
	if err != nil || !isRegularEntry(entry, name) || entry.Size != int64(len(expected)) {
		return ErrCorrupt
	}

	actual := make([]byte, len(expected))
	if _, err := io.ReadFull(archive, actual); err != nil {
		return err
	}

	if string(actual) != expected {
		return ChecksumError{}
	}

	if _, err := archive.Next(); err != io.EOF {
		return ErrCorrupt
	}

	return nil
}
