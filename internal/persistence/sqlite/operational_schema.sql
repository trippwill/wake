-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

CREATE TABLE operational.store_metadata(singleton INTEGER PRIMARY KEY CHECK(singleton=1), kind TEXT NOT NULL, version TEXT NOT NULL, id TEXT NOT NULL, warehouse_id TEXT NOT NULL) STRICT;

CREATE TABLE operational.attempts(id TEXT PRIMARY KEY NOT NULL, artifact_id TEXT NOT NULL, version TEXT NOT NULL, outcome TEXT NOT NULL, counts BLOB NOT NULL) STRICT;
CREATE INDEX operational.attempts_artifact ON attempts(artifact_id,version,outcome);
CREATE TABLE operational.reference_cache(key TEXT PRIMARY KEY NOT NULL, status TEXT NOT NULL, records BLOB NOT NULL, expires TEXT NOT NULL, evidence_id TEXT NOT NULL) STRICT;
CREATE TABLE operational.execution(connection_id TEXT NOT NULL, stream TEXT NOT NULL, operation TEXT NOT NULL, PRIMARY KEY(connection_id,stream,operation)) STRICT;
