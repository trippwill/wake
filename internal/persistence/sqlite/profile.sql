-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- name: Metadata
SELECT kind,version,id FROM {{schema}}.store_metadata WHERE singleton=1;
-- name: PutMetadata
INSERT INTO {{schema}}.store_metadata(singleton,kind,version,id) VALUES(1,?,?,?);
-- name: ObjectCount
SELECT count(*) FROM {{schema}}.sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%';
-- name: RecoverySchema
SELECT type,name,sql FROM {{schema}}.sqlite_schema WHERE substr(name,1,7)<>'sqlite_' AND sql IS NOT NULL;
-- name: Profile
SELECT access_id FROM profile WHERE singleton=1;
-- name: InitializeProfile
INSERT INTO profile(singleton,access_id,paused,reference_review) VALUES(1,?,0,0);
-- name: RecoverProfile
UPDATE profile SET access_id=?,paused=1,reference_review=max(reference_review,?),recovery_revision=recovery_revision+1 WHERE singleton=1;
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
SELECT count(*) FROM access.configurations a LEFT JOIN main.connections c ON c.id=a.id WHERE c.id IS NULL OR c.provider<>a.provider;
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
-- name: AdvanceFormat
UPDATE {{schema}}.store_metadata SET version=? WHERE singleton=1 AND version=?;
-- name: RestoreWarehouse
UPDATE store_metadata SET id=? WHERE singleton=1;
-- name: RestoreReview
UPDATE profile SET paused=1,recovery_revision=?,resumed_by='',resumed_at='' WHERE singleton=1;

-- name: Association
SELECT lineage,bind_restore FROM main.sync_association WHERE singleton=1;
-- name: VerifyUpstreamWitness
SELECT s.revision, p.paused,
 CASE WHEN a.actor<>'' AND a.granted_at<>'' THEN a.granted_revision ELSE '' END,
 s.epoch=m.id, s.bind_restore
FROM main.sync_association s JOIN access.upstream_grant a USING(singleton)
JOIN main.profile p USING(singleton) JOIN main.store_metadata m USING(singleton)
JOIN access.store_metadata k USING(singleton)
WHERE s.lineage=a.lineage AND s.epoch=a.epoch AND s.sync_id=a.sync_id
AND s.access_id=a.access_id AND s.access_id=k.id AND s.access_id=p.access_id
AND s.generation=a.generation AND s.revision=a.revision AND s.token=a.token
AND s.token<>'' AND s.revision<>'';
-- name: BindSync
INSERT OR REPLACE INTO main.sync_association(singleton,lineage,epoch,sync_id,access_id,generation,revision,token)
VALUES(1,?,?,?,?,?,?,?);
-- name: BindGrant
INSERT OR REPLACE INTO access.upstream_grant
SELECT singleton,lineage,epoch,sync_id,access_id,generation,revision,token,?,?,?
FROM main.sync_association;
-- name: RotateSync
UPDATE main.sync_association SET token=?,revision=coalesce(?,revision) WHERE singleton=1;
-- name: RotateWitness
UPDATE access.upstream_grant SET
 token=(SELECT token FROM main.sync_association WHERE singleton=1),
 revision=(SELECT revision FROM main.sync_association WHERE singleton=1),
 granted_revision=CASE
 WHEN (SELECT paused FROM main.profile WHERE singleton=1)<>0 THEN ''
 WHEN granted_revision=revision AND actor<>'' AND granted_at<>'' THEN (SELECT revision FROM main.sync_association WHERE singleton=1)
 ELSE '' END
WHERE singleton=1;
-- name: IssueGrant
UPDATE access.upstream_grant
SET actor=?,granted_at=?,granted_revision=revision WHERE singleton=1;
-- name: InvalidateRecoveryPreview
UPDATE main.profile SET recovery_revision=recovery_revision+1 WHERE singleton=1 AND paused=1;
-- name: MarkRestoreAssociation
UPDATE main.sync_association SET bind_restore=1 WHERE singleton=1;
