-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

CREATE TABLE access.store_metadata(singleton INTEGER PRIMARY KEY CHECK(singleton=1), kind TEXT NOT NULL, version TEXT NOT NULL, id TEXT NOT NULL) STRICT;

CREATE TABLE access.configurations(id TEXT PRIMARY KEY NOT NULL, provider TEXT NOT NULL, name TEXT NOT NULL DEFAULT '', settings BLOB NOT NULL) STRICT;
CREATE TABLE access.credentials(connection_id TEXT NOT NULL REFERENCES configurations(id), key TEXT NOT NULL, encrypted BLOB NOT NULL, PRIMARY KEY(connection_id,key)) STRICT;
CREATE TABLE access.reference_settings(singleton INTEGER PRIMARY KEY CHECK(singleton=1), encrypted BLOB NOT NULL, failure TEXT NOT NULL DEFAULT '') STRICT;
