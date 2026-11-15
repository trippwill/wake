-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

CREATE TABLE sync.store_metadata(singleton INTEGER PRIMARY KEY CHECK(singleton=1), kind TEXT NOT NULL, version TEXT NOT NULL, id TEXT NOT NULL) STRICT;
CREATE TABLE sync.configurations(id TEXT PRIMARY KEY NOT NULL, provider TEXT NOT NULL, name TEXT NOT NULL DEFAULT '', settings BLOB NOT NULL) STRICT;
CREATE TABLE sync.profile(singleton INTEGER PRIMARY KEY CHECK(singleton=1), access_id TEXT NOT NULL, paused INTEGER NOT NULL CHECK(paused IN (0,1)), reference_review INTEGER NOT NULL CHECK(reference_review IN (0,1)), warehouse_reset INTEGER NOT NULL DEFAULT 0 CHECK(warehouse_reset IN (0,1)), recovery_revision INTEGER NOT NULL DEFAULT 0, resumed_by TEXT NOT NULL DEFAULT '', resumed_at TEXT NOT NULL DEFAULT '') STRICT;
CREATE TABLE sync.work(connection_id TEXT NOT NULL, stream TEXT NOT NULL, operation TEXT NOT NULL, revision INTEGER NOT NULL DEFAULT 0, state_version TEXT NOT NULL DEFAULT '', state BLOB NOT NULL DEFAULT X'', next TEXT NOT NULL DEFAULT '', observed TEXT NOT NULL DEFAULT '', observed_completed INTEGER NOT NULL DEFAULT 0, observed_total INTEGER NOT NULL DEFAULT 0, acknowledged TEXT NOT NULL DEFAULT '', acknowledged_completed INTEGER NOT NULL DEFAULT 0, acknowledged_total INTEGER NOT NULL DEFAULT 0, failure TEXT NOT NULL DEFAULT '', failure_code TEXT NOT NULL DEFAULT '', PRIMARY KEY(connection_id,stream,operation)) STRICT;
CREATE TABLE sync.pacing(provider TEXT NOT NULL, policy TEXT NOT NULL, scope TEXT NOT NULL, next TEXT NOT NULL, PRIMARY KEY(provider,policy,scope)) STRICT;
CREATE TABLE sync.scheduled(connection_id TEXT NOT NULL, stream TEXT NOT NULL, operation TEXT NOT NULL, enabled INTEGER NOT NULL, PRIMARY KEY(connection_id,stream,operation), FOREIGN KEY(connection_id,stream,operation) REFERENCES work(connection_id,stream,operation)) STRICT;
CREATE TABLE sync.reference_budget(singleton INTEGER PRIMARY KEY CHECK(singleton=1), window_start TEXT NOT NULL, used INTEGER NOT NULL, cooldown TEXT NOT NULL) STRICT;
CREATE TABLE sync.attempts(id TEXT PRIMARY KEY NOT NULL, artifact_id TEXT NOT NULL, version TEXT NOT NULL, outcome TEXT NOT NULL, counts BLOB NOT NULL) STRICT;
CREATE TABLE sync.reference_cache(key TEXT PRIMARY KEY NOT NULL, status TEXT NOT NULL, records BLOB NOT NULL, expires TEXT NOT NULL, evidence_id TEXT NOT NULL) STRICT;
CREATE TABLE sync.sync_association (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 lineage TEXT NOT NULL,
 epoch TEXT NOT NULL,
 sync_id TEXT NOT NULL,
 access_id TEXT NOT NULL,
 generation TEXT NOT NULL,
 revision TEXT NOT NULL,
 token TEXT NOT NULL,
 bind_restore INTEGER NOT NULL DEFAULT 0 CHECK(bind_restore IN (0,1))
) STRICT;
CREATE INDEX sync.attempts_artifact ON attempts(artifact_id,version,outcome);
CREATE TABLE sync.reference_deadlines(instrument_id TEXT PRIMARY KEY NOT NULL, next TEXT NOT NULL) STRICT;
CREATE TABLE sync.reference_diagnostics(singleton INTEGER PRIMARY KEY CHECK(singleton=1), failure TEXT NOT NULL) STRICT;
