-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 1000;
PRAGMA synchronous = EXTRA;

-- Keep ordinary ingestion batches under the RESERVED lock until commit,
-- while allowing larger transactions to spill pages to disk.
PRAGMA main.cache_spill = 8192;
