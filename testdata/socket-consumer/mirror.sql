-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- name: Initialize
PRAGMA journal_mode=DELETE;
PRAGMA synchronous=FULL;
PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS mirror(singleton INTEGER PRIMARY KEY CHECK(singleton=1),payload BLOB NOT NULL) STRICT;
-- name: Read
SELECT payload FROM mirror WHERE singleton=1;
-- name: Replace
INSERT INTO mirror(singleton,payload) VALUES(1,?) ON CONFLICT(singleton) DO UPDATE SET payload=excluded.payload;
