// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package currencycatalog

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
)

const CacheName = "currency-catalog.json"

// Open loads a private runtime cache if available. An empty path keeps the
// catalog in memory.
func Open(path string, logger *slog.Logger) *Catalog {
	catalog := &Catalog{path: path, logger: logger}
	if path == "" {
		return catalog
	}

	cached, err := readCache(path)
	switch {
	case err == nil:
		catalog.current.Store(&cached)
	case !errors.Is(err, os.ErrNotExist):
		logger.Warn(
			"currency catalog cache ignored",
			"event",
			"currency_catalog_cache_ignored",
			"reason",
			errCacheRead.Error(),
		)
	}

	return catalog
}

func readCache(path string) (snapshot, error) {
	//nolint:gosec // The daemon owns the private runtime directory. Reject links and never block on a pipe.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return snapshot{}, err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return snapshot{}, errCacheRead
	}

	data, err := readBounded(file)
	if err != nil {
		return snapshot{}, err
	}

	var cached snapshot
	if err := json.Unmarshal(data, &cached); err != nil || !validList(cached) ||
		cached.Published < bundledPublication || cached.Checked.IsZero() {
		return snapshot{}, errCacheRead
	}

	cached.ETag = validETag(cached.ETag)

	return cached, nil
}

func saveCache(path string, value snapshot) error {
	info, err := os.Lstat(path)
	if (err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0)) ||
		(err != nil && !errors.Is(err, os.ErrNotExist)) {
		return errCacheWrite
	}

	file, err := os.CreateTemp(filepath.Dir(path), ".wake-currency-")
	if err != nil {
		return err
	}

	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()

	err = json.NewEncoder(file).Encode(value)
	if err == nil {
		err = file.Sync()
	}

	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return err
	}

	return os.Rename(temporary, path)
}
