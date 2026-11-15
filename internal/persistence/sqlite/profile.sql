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
SELECT access_id FROM {{schema}}.profile WHERE singleton=1;
-- name: InitializeProfile
INSERT INTO {{schema}}.profile(singleton,access_id,paused,reference_review) VALUES(1,?,0,0);
-- name: RecoverProfile
UPDATE {{schema}}.profile SET access_id=?,paused=1,reference_review=max(reference_review,?),recovery_revision=recovery_revision+1 WHERE singleton=1;
-- name: Attach
ATTACH DATABASE ? AS {{schema}};
-- name: AttachAccess
ATTACH DATABASE ? AS access;
-- name: Journal
PRAGMA {{schema}}.journal_mode=DELETE;
-- name: Sync
PRAGMA {{schema}}.synchronous=EXTRA;
-- name: SyncCheck
PRAGMA {{schema}}.synchronous;
-- name: InterruptAttempts
UPDATE sync.attempts SET outcome='interrupted' WHERE outcome='running';
-- name: AssociationCheck
SELECT
 (SELECT count(*) FROM sync.configurations s LEFT JOIN main.connections f ON f.id=s.id
  LEFT JOIN access.identities a ON a.id=s.id WHERE f.id IS NULL OR f.provider<>s.provider OR (a.id IS NOT NULL AND a.provider<>s.provider))
 + (SELECT count(*) FROM access.identities a JOIN main.connections f ON f.id=a.id WHERE f.provider<>a.provider)
 + (SELECT count(*) FROM sync.work w LEFT JOIN main.connections f ON f.id=w.connection_id WHERE f.id IS NULL)
 + (SELECT count(*) FROM sync.reference_cache c LEFT JOIN main.reference_evidence e ON e.id=c.evidence_id WHERE e.id IS NULL)
 + (SELECT count(*) FROM sync.reference_deadlines d LEFT JOIN main.instruments i ON i.id=d.instrument_id WHERE i.id IS NULL)
 + (SELECT count(*) FROM sync.attempts a LEFT JOIN main.artifacts f ON f.id=a.artifact_id WHERE f.id IS NULL);
-- name: Integrity
PRAGMA {{schema}}.quick_check;
-- name: ForeignCheck
PRAGMA {{schema}}.foreign_key_check;

-- name: InitializeRetainedAccessProfile
INSERT INTO {{schema}}.profile(singleton,access_id,paused,reference_review,warehouse_reset,recovery_revision) VALUES(1,?,1,0,1,1)
ON CONFLICT(singleton) DO UPDATE SET paused=1,warehouse_reset=1,recovery_revision=recovery_revision+1;
-- name: RetainConnections
INSERT INTO main.connections(id,provider,name) SELECT id,provider,name FROM sync.configurations WHERE true ON CONFLICT(id) DO NOTHING;

-- name: HistoryVersion
SELECT applied,target FROM {{schema}}.history_update WHERE singleton=1;
-- name: AdvanceFormat
UPDATE {{schema}}.store_metadata SET version=? WHERE singleton=1 AND version=?;
-- name: RenewStoreID
UPDATE {{schema}}.store_metadata SET id=? WHERE singleton=1;
-- name: RestoreReview
UPDATE {{schema}}.profile SET paused=1,recovery_revision=?,resumed_by='',resumed_at='' WHERE singleton=1;
-- name: DiscardRestoredWork
DELETE FROM sync.scheduled;
DELETE FROM sync.work;
DELETE FROM sync.reference_cache;
DELETE FROM sync.reference_deadlines;
DELETE FROM sync.reference_diagnostics;

-- name: InitializeLineage
INSERT INTO main.financial_lineage(singleton,id) VALUES(1,?);
-- name: FinancialLineage
SELECT id FROM main.financial_lineage WHERE singleton=1;
-- name: Association
SELECT EXISTS(SELECT 1 FROM sync.sync_association WHERE singleton=1 AND epoch<>(SELECT id FROM main.store_metadata));
-- name: VerifyUpstreamWitness
SELECT s.revision, p.paused,
 CASE WHEN a.actor<>'' AND a.granted_at<>'' THEN a.granted_revision ELSE '' END,
 s.epoch=m.id, s.bind_restore
FROM sync.sync_association s JOIN access.upstream_grant a USING(singleton)
JOIN sync.profile p USING(singleton) JOIN main.store_metadata m USING(singleton)
JOIN access.store_metadata k USING(singleton)
JOIN sync.store_metadata y USING(singleton)
JOIN main.financial_lineage l USING(singleton)
WHERE s.lineage=a.lineage AND s.lineage=l.id AND s.epoch=a.epoch AND s.sync_id=a.sync_id AND s.sync_id=y.id
AND s.access_id=a.access_id AND s.access_id=k.id AND s.access_id=p.access_id
AND s.generation=a.generation
AND (NOT ? OR (s.revision=a.revision AND s.token=a.token AND s.token<>'' AND s.revision<>''));
-- name: BackupAssociation
SELECT lineage,generation,token FROM sync.sync_association WHERE singleton=1;
-- name: BindSync
INSERT OR REPLACE INTO sync.sync_association(singleton,lineage,epoch,sync_id,access_id,generation,revision,token)
VALUES(1,?,?,?,?,?,?,?);
-- name: BindGrant
INSERT OR REPLACE INTO access.upstream_grant
SELECT singleton,lineage,epoch,sync_id,access_id,generation,revision,token,?,?,?
FROM sync.sync_association;
-- name: RotateSync
UPDATE sync.sync_association SET token=?,revision=coalesce(?,revision) WHERE singleton=1;
-- name: RotateWitness
UPDATE access.upstream_grant SET
 token=(SELECT token FROM sync.sync_association WHERE singleton=1),
 revision=(SELECT revision FROM sync.sync_association WHERE singleton=1),
 granted_revision=CASE
 WHEN (SELECT paused FROM sync.profile WHERE singleton=1)<>0 THEN ''
 WHEN granted_revision=revision AND actor<>'' AND granted_at<>'' THEN (SELECT revision FROM sync.sync_association WHERE singleton=1)
 ELSE '' END
WHERE singleton=1;
-- name: IssueGrant
UPDATE access.upstream_grant
SET actor=?,granted_at=?,granted_revision=revision WHERE singleton=1;
-- name: InvalidateRecoveryPreview
UPDATE sync.profile SET recovery_revision=recovery_revision+1 WHERE singleton=1 AND (paused=1 OR EXISTS(SELECT 1 FROM sync.recovery_state));
-- name: MarkRestoreAssociation
UPDATE sync.sync_association SET bind_restore=1 WHERE singleton=1;

-- name: SyncRecoveryConfigurations
SELECT id,provider,name,settings FROM {{schema}}.configurations ORDER BY id;
-- name: RecoveryDescriptor
SELECT provider FROM main.connections WHERE id=?;
-- name: ImportSyncConfiguration
INSERT INTO sync.configurations(id,provider,name,settings) VALUES(?,?,?,?);

-- name: ClearRecoveryScopes
DELETE FROM sync.restart_scopes;
DELETE FROM sync.recovery_sources;
INSERT INTO sync.recovery_sources(connection_id) SELECT id FROM main.connections;
-- name: SetRecoveryStart
INSERT OR REPLACE INTO sync.recovery_state(singleton,recovered_at) VALUES(1,?);
-- name: RecoveryStart
SELECT recovered_at FROM sync.recovery_state WHERE singleton=1;
-- name: RecoverySourceInputs
SELECT f.id,f.provider,f.name,c.settings,coalesce(a.id,''),coalesce(a.revision,0),
 coalesce(a.authority,''),coalesce(a.kind,''),coalesce(a.scope,''),coalesce(a.value,'')
FROM sync.recovery_sources r JOIN main.connections f ON f.id=r.connection_id
LEFT JOIN sync.configurations c ON c.id=f.id AND c.provider=f.provider
LEFT JOIN main.accounts a ON a.connection_id=f.id AND a.provider=f.provider
ORDER BY f.id;
-- name: RecoveryCoverage
SELECT b.stream,max(r.reported_to) FROM main.acquisition_receipts r JOIN main.artifacts b ON b.id=r.artifact_id
WHERE b.connection_id=? AND r.operation IN ('acquire','supply') AND r.reported_to<>''
GROUP BY b.stream;
-- name: RecoveryPacingFloor
SELECT next FROM sync.pacing WHERE provider=?;
-- name: CurrentRestartScope
SELECT c.settings,a.id,a.revision
FROM sync.recovery_sources r
JOIN sync.configurations c ON c.id=r.connection_id
JOIN main.accounts a ON a.connection_id=c.id AND a.provider=c.provider
WHERE r.connection_id=?;
-- name: ApprovedRestartStreams
SELECT r.stream FROM sync.restart_scopes r
JOIN sync.configurations c ON c.id=r.connection_id AND c.settings=r.settings
JOIN main.accounts a ON a.connection_id=r.connection_id AND a.id=r.account_id AND a.revision=r.account_revision
WHERE r.connection_id=?;
-- name: CheckRestartScope
SELECT EXISTS(SELECT 1 FROM sync.recovery_sources WHERE connection_id=?1),
 coalesce((SELECT r.not_before FROM sync.restart_scopes r
 JOIN sync.configurations c ON c.id=r.connection_id AND c.settings=r.settings
 JOIN main.accounts a ON a.connection_id=r.connection_id AND a.id=r.account_id AND a.revision=r.account_revision
 WHERE r.connection_id=?1 AND r.stream=?2 AND ?3='acquire'),'');
-- name: ApproveRestartScope
INSERT OR REPLACE INTO sync.restart_scopes
(connection_id,stream,settings,account_id,account_revision,not_before,requested_from,requested_to,approved_by,approved_at)
VALUES(?,?,?,?,?,?,?,?,?,?);
-- name: SeedRestartWork
INSERT INTO sync.work(connection_id,stream,operation,state_version,state,next)
VALUES(?,?,'acquire',?,?,?)
ON CONFLICT(connection_id,stream,operation) DO UPDATE SET
 revision=sync.work.revision+1,state_version=excluded.state_version,state=excluded.state,next=excluded.next,
 observed='',observed_completed=0,observed_total=0,acknowledged='',acknowledged_completed=0,acknowledged_total=0,
 failure='',failure_code='';
-- name: ResumeReviewedRecovery
UPDATE sync.profile SET paused=0,warehouse_reset=0,resumed_by=?,resumed_at=? WHERE singleton=1;
-- name: SetRecoveredReference
UPDATE sync.recovery_state SET reference_granted=? WHERE singleton=1;
-- name: RevokeRestartScopes
DELETE FROM sync.restart_scopes WHERE connection_id=?;
-- name: RevokeChangedRestartScopes
DELETE FROM sync.restart_scopes WHERE connection_id=? AND NOT EXISTS(
 SELECT 1 FROM sync.configurations c WHERE c.id=restart_scopes.connection_id AND c.settings=restart_scopes.settings);
-- name: ReferenceRecoveryApproved
SELECT coalesce((SELECT reference_granted FROM sync.recovery_state WHERE singleton=1),1);
-- name: ReadPacingForRecovery
SELECT provider,policy,scope,next FROM {{schema}}.pacing;
-- name: ReadReferencePacingForRecovery
SELECT window_start,cooldown FROM {{schema}}.reference_budget WHERE singleton=1;
-- name: ExistingRecoveryPacing
SELECT next FROM sync.pacing WHERE provider=? AND policy=? AND scope=?;
-- name: PreserveRecoveryPacing
INSERT OR REPLACE INTO sync.pacing(provider,policy,scope,next) VALUES(?,?,?,?);
