// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package archive

import (
	"archive/tar"
	"io"
	"strings"
)

const backupDigestEntry = "wake-backup/v1/sha256"

var (
	backupEntries = [storeCount]string{
		"wake-backup/v2/warehouse.db",
		"wake-backup/v2/access.db",
		"wake-backup/v2/sync.db",
	}
	legacyBackupEntries = [storeCount]string{
		"wake-upgrade/warehouse.db",
		"wake-upgrade/access.db",
		"wake-upgrade/sync.db",
	}
)

func WriteUpgrade(output io.Writer, stores [storeCount]SizedReader) error {
	archive := tar.NewWriter(output)

	var digests strings.Builder
	for i, input := range stores {
		digest, err := writeMember(archive, backupEntries[i], input)
		if err != nil {
			return err
		}

		digests.WriteString(digest)
	}

	if err := writeEntry(
		archive,
		backupDigestEntry,
		SizedReader{Reader: strings.NewReader(digests.String()), Size: int64(digests.Len())},
	); err != nil {
		return err
	}

	return archive.Close()
}

func VerifyUpgrade(input io.Reader) error {
	archive := tar.NewReader(input)

	entry, err := archive.Next()
	if err != nil {
		return ErrScope
	}

	return readUpgradeStores(archive, entry, func(_ int, member io.Reader) error {
		return discardMember(member)
	})
}

// Versioned member names require checksums. The original member names use
// the legacy format without embedded checksums.
func readUpgradeStores(archive *tar.Reader, entry *tar.Header, stage func(int, io.Reader) error) error {
	legacy := entry.Name == legacyBackupEntries[0]
	names := backupEntries
	if legacy {
		names = legacyBackupEntries
	}

	var digests strings.Builder

	for i, name := range names {
		if !isRegularEntry(entry, name) || entry.Size == 0 {
			return ErrScope
		}

		digest, err := readMember(archive, entry.Size, func(member io.Reader) error {
			return stage(i, member)
		})
		if err != nil {
			return err
		}

		digests.WriteString(digest)

		if i < len(names)-1 {
			entry, err = archive.Next()
			if err != nil {
				return ErrScope
			}
		}
	}

	if legacy {
		if _, err := archive.Next(); err != io.EOF {
			return ErrScope
		}

		return nil
	}

	return readChecksums(archive, backupDigestEntry, digests.String())
}
