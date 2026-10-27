-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- name: Metadata
SELECT kind,version,id,warehouse_id FROM {{schema}}.store_metadata WHERE singleton=1;
-- name: PutMetadata
INSERT INTO {{schema}}.store_metadata(singleton,kind,version,id,warehouse_id) VALUES(1,?,?,?,?);
-- name: ObjectCount
SELECT count(*) FROM {{schema}}.sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%';
-- name: Profile
SELECT access_id FROM profile WHERE singleton=1;
-- name: InitializeProfile
INSERT INTO profile(singleton,access_id,paused,reference_review) VALUES(1,?,0,0);
-- name: RecoverProfile
UPDATE profile SET access_id=?,paused=1,reference_review=max(reference_review,?),recovery_revision=recovery_revision+1 WHERE singleton=1;
-- name: AttachAccess
ATTACH DATABASE ? AS access;
-- name: AttachOperational
ATTACH DATABASE ? AS operational;
-- name: Journal
PRAGMA {{schema}}.journal_mode=DELETE;
-- name: Sync
PRAGMA {{schema}}.synchronous=EXTRA;
-- name: SyncCheck
PRAGMA {{schema}}.synchronous;
-- name: InterruptAttempts
UPDATE operational.attempts SET outcome='interrupted' WHERE outcome='running';
-- name: AssociationCheck
SELECT
 (SELECT count(*) FROM access.configurations a LEFT JOIN main.connections c ON c.id=a.id WHERE c.id IS NULL OR c.provider<>a.provider) +
 (SELECT count(*) FROM operational.execution e LEFT JOIN main.work w USING(connection_id,stream,operation) WHERE w.connection_id IS NULL) +
 (SELECT count(*) FROM operational.attempts a LEFT JOIN main.artifacts f ON f.id=a.artifact_id WHERE f.id IS NULL) +
 (SELECT count(*) FROM operational.reference_cache c LEFT JOIN main.reference_evidence e ON e.id=c.evidence_id WHERE e.id IS NULL);
-- name: Integrity
PRAGMA {{schema}}.quick_check;
-- name: ForeignCheck
PRAGMA {{schema}}.foreign_key_check;

-- name: InitializeRetainedAccessProfile
INSERT INTO profile(singleton,access_id,paused,reference_review,warehouse_reset,recovery_revision) VALUES(1,?,1,0,1,1);
-- name: RebindMetadata
UPDATE {{schema}}.store_metadata SET warehouse_id=? WHERE singleton=1 AND kind=? AND version=? AND id=? AND warehouse_id=?;
-- name: RetainConnections
INSERT INTO main.connections(id,provider) SELECT id,provider FROM access.configurations WHERE true ON CONFLICT(id) DO NOTHING;
-- name: DiscardOldExecution
DELETE FROM operational.execution;
DELETE FROM operational.attempts;
DELETE FROM operational.reference_cache;
