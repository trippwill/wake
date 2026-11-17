// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package recoveryarchive

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"time"
)

const (
	installationManifestEntry = "wake-installation/v1/manifest.json"
	maxManifestSize           = 16 << 10
)

var installationEntries = [...]string{
	"wake-installation/v1/warehouse.db",
	"wake-installation/v1/access.db",
	"wake-installation/v1/sync.db",
	"wake-installation/v1/config.toml",
	"wake-installation/v1/auth.json",
}

type Manifest struct {
	BackupID   string           `json:"backup_id"`
	CapturedAt time.Time        `json:"captured_at"`
	Scope      string           `json:"scope"`
	Storage    Metadata         `json:"storage"`
	Members    []ManifestMember `json:"members"`
}

type Metadata struct {
	Stores      [storeCount]Store `json:"stores"`
	Lineage     string            `json:"financial_lineage"`
	Generation  string            `json:"association_generation"`
	SafetyToken string            `json:"safety_token"`
	AppliedRule int64             `json:"applied_rule"`
	TargetRule  int64             `json:"target_rule"`
}

type Store struct {
	Kind   string `json:"kind"`
	ID     string `json:"store_id"`
	Format string `json:"format"`
}

type ManifestMember struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"sha256"`
}

// WriteInstallation uses the caller's backup identity, timestamp, and storage
// metadata. A nil configuration or auth value omits that evidence member.
func WriteInstallation(
	output io.Writer, stores [storeCount]SizedReader, manifest Manifest, configuration, auth []byte,
) error {
	archive := tar.NewWriter(output)
	manifest.Scope = "installation"
	manifest.Members = nil

	for i, input := range stores {
		if err := writeInstallationMember(archive, &manifest, installationEntries[i], input); err != nil {
			return err
		}
	}

	for i, data := range [][]byte{configuration, auth} {
		if data == nil {
			continue
		}

		input := SizedReader{Reader: bytes.NewReader(data), Size: int64(len(data))}
		if err := writeInstallationMember(
			archive,
			&manifest,
			installationEntries[storeCount+i],
			input,
		); err != nil {
			return err
		}
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}

	if err := writeEntry(
		archive,
		installationManifestEntry,
		SizedReader{Reader: bytes.NewReader(data), Size: int64(len(data))},
	); err != nil {
		return err
	}

	return archive.Close()
}

func writeInstallationMember(archive *tar.Writer, manifest *Manifest, name string, input SizedReader) error {
	digest, err := writeMember(archive, name, input)
	if err != nil {
		return err
	}

	manifest.Members = append(manifest.Members, ManifestMember{name, input.Size, digest})

	return nil
}

// ReadInstallation stages stores in warehouse, access, sync order. The caller
// can use staged bytes once validation succeeds. Older upgrade backups return a
// nil manifest. Configuration and auth evidence are verified within the stream.
func ReadInstallation(input io.Reader, stage func(int, io.Reader) error) (*Manifest, error) {
	archive := tar.NewReader(input)

	entry, err := archive.Next()
	if err != nil {
		return nil, ErrScope
	}

	if entry.Name != installationEntries[0] {
		return nil, readUpgradeStores(archive, entry, stage)
	}

	var members []ManifestMember
	for i, name := range installationEntries {
		if i >= storeCount && entry.Name != name {
			continue
		}

		if !isRegularEntry(entry, name) || (entry.Size == 0 && i != storeCount) {
			return nil, ErrCorrupt
		}

		consume := discardMember
		if i < storeCount {
			consume = func(member io.Reader) error {
				return stage(i, member)
			}
		}

		digest, err := readMember(archive, entry.Size, consume)
		if err != nil {
			return nil, err
		}

		members = append(members, ManifestMember{name, entry.Size, digest})

		entry, err = archive.Next()
		if err != nil {
			return nil, ErrCorrupt
		}
	}

	return readManifest(archive, entry, members)
}

func readManifest(archive *tar.Reader, entry *tar.Header, members []ManifestMember) (*Manifest, error) {
	if !isRegularEntry(entry, installationManifestEntry) || entry.Size == 0 || entry.Size > maxManifestSize {
		return nil, ErrCorrupt
	}

	data, err := io.ReadAll(io.LimitReader(archive, entry.Size))
	if err != nil {
		return nil, err
	}

	var manifest Manifest
	if json.Unmarshal(data, &manifest) != nil || manifest.BackupID == "" || manifest.CapturedAt.IsZero() ||
		manifest.Scope != "installation" || len(manifest.Members) != len(members) {
		return nil, ErrCorrupt
	}

	for i, member := range members {
		if manifest.Members[i] != member {
			return nil, ChecksumError{Member: member.Name}
		}
	}

	if _, err := archive.Next(); err != io.EOF {
		return nil, ErrCorrupt
	}

	return &manifest, nil
}
