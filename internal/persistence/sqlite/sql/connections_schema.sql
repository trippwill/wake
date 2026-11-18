-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

CREATE TABLE access.store_metadata(singleton INTEGER PRIMARY KEY CHECK(singleton=1), kind TEXT NOT NULL, version TEXT NOT NULL, id TEXT NOT NULL) STRICT;

CREATE TABLE access.identities(id TEXT PRIMARY KEY NOT NULL, provider TEXT NOT NULL) STRICT;
CREATE TABLE access.credentials(connection_id TEXT NOT NULL REFERENCES identities(id), key TEXT NOT NULL, encrypted BLOB NOT NULL, PRIMARY KEY(connection_id,key)) STRICT;
CREATE TABLE access.reference_settings(singleton INTEGER PRIMARY KEY CHECK(singleton=1), encrypted BLOB NOT NULL) STRICT;

CREATE TABLE access.upstream_grant (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 lineage TEXT NOT NULL,
 epoch TEXT NOT NULL,
 sync_id TEXT NOT NULL,
 access_id TEXT NOT NULL,
 generation TEXT NOT NULL,
 revision TEXT NOT NULL,
 token TEXT NOT NULL,
 granted_revision TEXT NOT NULL,
 actor TEXT NOT NULL,
 granted_at TEXT NOT NULL
) STRICT;
