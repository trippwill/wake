-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

CREATE TABLE sync.recovery_state(
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 recovered_at TEXT NOT NULL,
 reference_granted INTEGER NOT NULL DEFAULT 0 CHECK(reference_granted IN (0,1))
) STRICT;

CREATE TABLE sync.recovery_sources(connection_id TEXT PRIMARY KEY NOT NULL) STRICT;

CREATE TABLE sync.restart_scopes(
 connection_id TEXT NOT NULL REFERENCES recovery_sources(connection_id),
 stream TEXT NOT NULL,
 settings BLOB NOT NULL,
 account_id TEXT NOT NULL,
 account_revision INTEGER NOT NULL,
 not_before TEXT NOT NULL,
 requested_from TEXT NOT NULL,
 requested_to TEXT NOT NULL,
 approved_by TEXT NOT NULL,
 approved_at TEXT NOT NULL,
 PRIMARY KEY(connection_id,stream)
) STRICT;
