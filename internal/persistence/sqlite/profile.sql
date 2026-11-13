-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- name: Metadata
SELECT kind,version,id FROM {{schema}}.store_metadata WHERE singleton=1;
-- name: PutMetadata
INSERT INTO {{schema}}.store_metadata(singleton,kind,version,id) VALUES(1,?,?,?);
-- name: ObjectCount
SELECT count(*) FROM {{schema}}.sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%';
-- name: Profile
SELECT access_id FROM profile WHERE singleton=1;
-- name: InitializeProfile
INSERT INTO profile(singleton,access_id,paused,reference_review) VALUES(1,?,0,0);
-- name: RecoverProfile
UPDATE profile SET access_id=?,paused=1,reference_review=max(reference_review,?),recovery_revision=recovery_revision+1 WHERE singleton=1;
-- name: RestoreWarehouse
UPDATE store_metadata SET id=? WHERE singleton=1;
UPDATE profile SET paused=1,recovery_revision=recovery_revision+1,resumed_by='',resumed_at='' WHERE singleton=1;
-- name: AttachAccess
ATTACH DATABASE ? AS access;
-- name: Journal
PRAGMA {{schema}}.journal_mode=DELETE;
-- name: Sync
PRAGMA {{schema}}.synchronous=EXTRA;
-- name: SyncCheck
PRAGMA {{schema}}.synchronous;
-- name: InterruptAttempts
UPDATE main.attempts SET outcome='interrupted' WHERE outcome='running';
-- name: AssociationCheck
SELECT count(*) FROM access.configurations a JOIN main.connections c ON c.id=a.id WHERE c.provider<>a.provider;
-- name: Integrity
PRAGMA {{schema}}.quick_check;
-- name: ForeignCheck
PRAGMA {{schema}}.foreign_key_check;

-- name: InitializeRetainedAccessProfile
INSERT INTO profile(singleton,access_id,paused,reference_review,warehouse_reset,recovery_revision) VALUES(1,?,1,0,1,1);
-- name: RetainConnections
INSERT INTO main.connections(id,provider,name) SELECT id,provider,name FROM access.configurations WHERE true ON CONFLICT(id) DO NOTHING;

-- name: HistoryVersion
SELECT applied,target FROM {{schema}}.history_update WHERE singleton=1;
-- name: BackupSnapshot
VACUUM main INTO ?;
-- name: AdvanceFormat
UPDATE {{schema}}.store_metadata SET version=? WHERE singleton=1 AND version=?;
