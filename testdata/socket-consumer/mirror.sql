-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- name: Settings
PRAGMA journal_mode=DELETE;
PRAGMA synchronous=FULL;
PRAGMA busy_timeout=5000;
-- name: Initialize
BEGIN EXCLUSIVE;
CREATE TABLE mirror(singleton INTEGER PRIMARY KEY CHECK(singleton=1),payload BLOB NOT NULL) STRICT;
PRAGMA application_id=0x57414b43; -- WAKC: this consumer
PRAGMA user_version=1;
COMMIT;
-- name: Identity
SELECT application_id,user_version FROM pragma_application_id(),pragma_user_version();
-- name: Read
SELECT payload FROM mirror WHERE singleton=1;
-- name: Replace
INSERT INTO mirror(singleton,payload) VALUES(1,?) ON CONFLICT(singleton) DO UPDATE SET payload=excluded.payload;
