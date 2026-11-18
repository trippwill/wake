// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package archive

import (
	"archive/tar"
	"io"
	"strings"
)

const (
	snapshotWarehouseEntry = "wake-snapshot/v1/warehouse.db"
	snapshotDigestEntry    = "wake-snapshot/v1/warehouse.sha256"
)

func WriteSnapshot(output io.Writer, warehouse SizedReader) error {
	archive := tar.NewWriter(output)

	digest, err := writeMember(archive, snapshotWarehouseEntry, warehouse)
	if err != nil {
		return err
	}

	if err := writeEntry(
		archive,
		snapshotDigestEntry,
		SizedReader{Reader: strings.NewReader(digest), Size: int64(len(digest))},
	); err != nil {
		return err
	}

	return archive.Close()
}

// ReadSnapshot streams the member to destination before verifying its digest.
// The caller may use the staged bytes only after successful verification.
func ReadSnapshot(input io.Reader, destination io.Writer) error {
	archive := tar.NewReader(input)

	entry, err := archive.Next()
	if err != nil {
		return ErrScope
	}

	switch entry.Name {
	case backupEntries[0], legacyBackupEntries[0], installationEntries[0]:
		return ErrPairedBackup
	}

	if !isRegularEntry(entry, snapshotWarehouseEntry) || entry.Size == 0 {
		return ErrScope
	}

	digest, err := readMember(archive, entry.Size, func(member io.Reader) error {
		_, err := io.Copy(destination, member)

		return err
	})
	if err != nil {
		return err
	}

	return readChecksums(archive, snapshotDigestEntry, digest)
}
