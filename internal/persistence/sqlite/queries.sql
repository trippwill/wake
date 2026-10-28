-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- name: CountTables :one
SELECT count(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%';
-- name: PutConnection :exec
INSERT INTO access.configurations(id,provider,settings) VALUES(?,?,?);
-- name: Connection :one
SELECT * FROM access.configurations WHERE id=?;
-- name: PutSecret :exec
INSERT INTO access.credentials(connection_id,key,encrypted) VALUES(?,?,?);
-- name: Secret :one
SELECT encrypted FROM access.credentials WHERE connection_id=? AND key=?;
-- name: EnsureWork :exec
INSERT INTO work(connection_id,stream,operation) VALUES(?,?,?) ON CONFLICT DO NOTHING;
-- name: Checkpoint :one
SELECT * FROM work WHERE connection_id=? AND stream=? AND operation=?;
-- name: ObserveWork :exec
UPDATE work SET observed=?,observed_completed=?,observed_total=? WHERE connection_id=? AND stream=? AND operation=?;
-- name: FailWork :exec
UPDATE work SET failure=? WHERE connection_id=? AND stream=? AND operation=?;
-- name: AdvanceWork :execrows
UPDATE work SET revision=revision+1,state_version=?,state=?,next=?,acknowledged=?,acknowledged_completed=?,acknowledged_total=?,failure=? WHERE connection_id=? AND stream=? AND operation=? AND revision=?;
-- name: PutArtifact :exec
INSERT INTO artifacts(id,connection_id,stream,operation,format,media_type,retrieved,hash,content,requested_from,requested_to,reported_from,reported_to) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?);
-- name: Retained :one
SELECT artifacts.*,connections.provider FROM artifacts JOIN connections ON connections.id=artifacts.connection_id WHERE artifacts.id=?;
-- name: PutCandidate :exec
INSERT INTO candidates(artifact_id,authority,kind,scope,value,name,currency) VALUES(?,?,?,?,?,?,?);
-- name: Candidates :many
SELECT candidates.* FROM candidates JOIN artifacts ON artifacts.id=candidates.artifact_id WHERE artifacts.connection_id=? ORDER BY candidates.artifact_id,candidates.authority,candidates.kind,candidates.scope,candidates.value;
-- name: StartAttempt :exec
INSERT INTO operational.attempts(id,artifact_id,version,outcome,counts) VALUES(?,?,?,'running',X'');
-- name: FinishAttempt :exec
INSERT INTO operational.attempts(id,artifact_id,version,outcome,counts) SELECT a.id,a.artifact_id,a.version,?,? FROM operational.attempts a WHERE a.id=? ON CONFLICT(id) DO UPDATE SET outcome=excluded.outcome,counts=excluded.counts;
-- name: Succeeded :one
SELECT counts FROM normalization_receipts WHERE artifact_id=? AND version=?;
-- name: Attempts :many
SELECT * FROM operational.attempts WHERE artifact_id=? ORDER BY rowid;
-- name: PutRecord :exec
INSERT INTO records(artifact_id,version,location,kind,data) VALUES(?,?,?,?,?);
-- name: Records :many
SELECT * FROM records WHERE artifact_id=? AND version=? ORDER BY location;
-- name: Pacing :one
SELECT next FROM pacing WHERE provider=? AND policy=? AND scope=?;
-- name: SetPacing :exec
INSERT INTO pacing(provider,policy,scope,next) VALUES(?,?,?,?) ON CONFLICT(provider,policy,scope) DO UPDATE SET next=excluded.next;

-- name: ReplaceSecret :exec
INSERT INTO access.credentials(connection_id,key,encrypted) VALUES(?,?,?) ON CONFLICT(connection_id,key) DO UPDATE SET encrypted=excluded.encrypted;
-- name: Connections :many
SELECT id FROM connections ORDER BY rowid;
-- name: SetScheduled :exec
INSERT INTO scheduled(connection_id,stream,operation,enabled) VALUES(?,?,?,?) ON CONFLICT(connection_id,stream,operation) DO UPDATE SET enabled=excluded.enabled;
-- name: RetryWork :exec
UPDATE work SET revision=revision+1,failure='',next=CASE WHEN (SELECT paused FROM profile WHERE singleton=1)=1 THEN next ELSE '' END WHERE connection_id=? AND stream=? AND operation=?;
-- name: RestartDiscovery :exec
UPDATE work SET revision=revision+1,state_version=CASE WHEN (SELECT paused FROM profile WHERE singleton=1)=1 THEN state_version ELSE '' END,state=CASE WHEN (SELECT paused FROM profile WHERE singleton=1)=1 THEN state ELSE X'' END,failure='',next=CASE WHEN (SELECT paused FROM profile WHERE singleton=1)=1 THEN next ELSE '' END WHERE connection_id=? AND stream=? AND operation='discover';
-- name: PendingWork :many
SELECT work.connection_id,work.stream,work.operation FROM work JOIN scheduled USING(connection_id,stream,operation) JOIN operational.execution USING(connection_id,stream,operation) WHERE (SELECT paused FROM profile WHERE singleton=1)=0 AND enabled=1 AND (next='' OR julianday(next)<=julianday(?)) AND failure NOT IN ('source access not authorized','invalid provider contract value','source capability unavailable','malformed source evidence','source resource limit exceeded') ORDER BY work.rowid LIMIT 20;
-- name: WorkStatuses :many
SELECT * FROM work WHERE connection_id=? ORDER BY operation;
-- name: DisableDiscovery :exec
UPDATE scheduled SET enabled=0 WHERE connection_id=? AND operation='discover';
-- name: Account :one
SELECT * FROM accounts LIMIT 1;
-- name: CandidateExists :one
SELECT count(*) FROM candidates JOIN artifacts ON artifacts.id=candidates.artifact_id WHERE artifacts.connection_id=? AND candidates.artifact_id=? AND authority=? AND kind=? AND scope=? AND value=? AND operation='discover';
-- name: PutAccount :exec
INSERT INTO accounts(id,singleton,name,currency,connection_id,provider,stream,authority,kind,scope,value,artifact_id,rule) VALUES(?,1,?,?,?,?,?,?,?,?,?,?,?);
-- name: CandidateProfiles :many
SELECT candidates.* FROM candidates JOIN artifacts ON artifacts.id=candidates.artifact_id WHERE artifacts.connection_id=? ORDER BY artifacts.rowid DESC,candidates.value;
-- name: PendingArtifacts :many
SELECT artifacts.id FROM artifacts JOIN accounts ON accounts.connection_id=artifacts.connection_id AND accounts.stream=artifacts.stream WHERE artifacts.operation='acquire' AND artifacts.processed=0 AND (processing_next='' OR julianday(processing_next)<=julianday(?)) ORDER BY artifacts.rowid LIMIT 20;
-- name: ArtifactProcessed :exec
UPDATE artifacts SET processed=1,processing_failure='',processing_next='' WHERE id=?;
-- name: ArtifactFailure :exec
UPDATE artifacts SET processing_failure=?,processing_next=? WHERE id=?;
-- name: ProcessingStatuses :many
SELECT id,processed,processing_failure,requested_from,requested_to,reported_from,reported_to, (SELECT count(*) FROM records WHERE records.artifact_id=artifacts.id AND kind='trade') AS trade_claims, (SELECT count(*) FROM records WHERE records.artifact_id=artifacts.id AND kind='lifecycle') AS lifecycle_claims, (SELECT count(*) FROM records WHERE records.artifact_id=artifacts.id AND kind='conversion') AS conversion_claims, (SELECT count(*) FROM records WHERE records.artifact_id=artifacts.id AND kind='rejected') AS excluded_rows, (SELECT count(*) FROM execution_observations WHERE execution_observations.artifact_id=artifacts.id AND outcome IN ('identity needs review','native identity needs review')) AS needs_review FROM artifacts WHERE connection_id=? AND operation='acquire' ORDER BY rowid DESC LIMIT 10;
-- name: InstrumentBySource :one
SELECT * FROM instruments WHERE source_key=?;
-- name: CompetingFIGI :many
SELECT instruments.* FROM instruments
JOIN (
 SELECT projected.id FROM instruments projected WHERE projected.reference_figi=sqlc.arg(figi)
 UNION
 SELECT retained.instrument_id FROM instrument_selections retained WHERE retained.figi=sqlc.arg(figi)
) claims ON claims.id=instruments.id
WHERE instruments.id<>sqlc.arg(current_id) ORDER BY instruments.id;
-- name: PutInstrumentRoot :exec
INSERT INTO instruments(id,source_key,name,ticker,kind,reported,rule,reference_status) VALUES(?,?,?,?,?,?,?,'provisional');
-- name: Instruments :many
SELECT * FROM instruments ORDER BY rowid;
-- name: Instrument :one
SELECT * FROM instruments WHERE id=?;
-- name: PutListing :exec
INSERT INTO listings(id,instrument_id,venue,ticker,currency) VALUES(?,?,?,?,?) ON CONFLICT(instrument_id,venue,ticker,currency) DO NOTHING;
-- name: ListingByTerms :one
SELECT * FROM listings WHERE instrument_id=? AND venue=? AND ticker=? AND currency=?;
-- name: Listings :many
SELECT * FROM listings WHERE instrument_id=? ORDER BY rowid;
-- name: PutExecution :exec
INSERT INTO executions(id,account_id,instrument_id,listing_id,accepted,status,rule) VALUES(?,?,?,?,?,'accepted',?);
-- name: Execution :one
SELECT * FROM executions WHERE id=?;
-- name: Executions :many
SELECT * FROM executions ORDER BY rowid DESC;
-- name: EventLink :one
SELECT execution_id FROM event_links WHERE account_id=? AND reference=?;
-- name: PutEventLink :exec
INSERT INTO event_links(account_id,reference,execution_id) VALUES(?,?,?) ON CONFLICT(account_id,reference,execution_id) DO NOTHING;
-- name: PutObservation :exec
INSERT INTO execution_observations(artifact_id,version,location,execution_id,outcome,rule) VALUES(?,?,?,?,?,?);
-- name: ObservationExists :one
SELECT count(*) FROM execution_observations WHERE artifact_id=? AND version=? AND location=?;
-- name: ExecutionObservations :many
SELECT * FROM execution_observations WHERE execution_id=? ORDER BY rowid;
-- name: ConflictExecution :exec
UPDATE executions SET status='needs review' WHERE id=?;
-- name: ReferenceSetting :one
SELECT * FROM access.reference_settings WHERE singleton=1;
-- name: SetReferenceKey :exec
INSERT INTO access.reference_settings(singleton,encrypted,failure) VALUES(1,?,'') ON CONFLICT(singleton) DO UPDATE SET encrypted=excluded.encrypted,failure='';
-- name: ReferenceFailure :exec
INSERT INTO access.reference_settings(singleton,encrypted,failure) VALUES(1,X'',?) ON CONFLICT(singleton) DO UPDATE SET failure=excluded.failure;
-- name: ReferenceBudget :one
SELECT * FROM reference_budget WHERE singleton=1;
-- name: SetReferenceBudget :exec
INSERT INTO reference_budget(singleton,window_start,used,cooldown) VALUES(1,?,?,?) ON CONFLICT(singleton) DO UPDATE SET window_start=excluded.window_start,used=excluded.used,cooldown=excluded.cooldown;
-- name: PutReferenceEvidence :exec
INSERT INTO reference_evidence(id,retrieved,request,response,http_status,headers) VALUES(?,?,?,?,?,?);
-- name: ReferenceEvidence :one
SELECT * FROM reference_evidence WHERE id=?;
-- name: CachedReference :one
SELECT * FROM operational.reference_cache WHERE key=?;
-- name: CacheReference :exec
INSERT INTO operational.reference_cache(key,status,records,expires,evidence_id) VALUES(?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET status=excluded.status,records=excluded.records,expires=excluded.expires,evidence_id=excluded.evidence_id;
-- name: PendingReference :many
SELECT * FROM instruments WHERE (SELECT paused FROM profile WHERE singleton=1)=0 AND reference_status<>'conflict' AND (reference_next='' OR julianday(reference_next)<=julianday(?)) ORDER BY rowid LIMIT 100;
-- name: ResolveInstrument :exec
UPDATE instruments SET name=sqlc.arg(name),ticker=sqlc.arg(ticker),reference_figi=sqlc.arg(reference_figi),evidence_id=sqlc.arg(evidence_id),reference_status=sqlc.arg(reference_status),reference_next=sqlc.arg(reference_next),revision=revision+CASE WHEN name<>sqlc.arg(name) OR ticker<>sqlc.arg(ticker) OR reference_figi<>sqlc.arg(reference_figi) OR reference_status<>sqlc.arg(reference_status) THEN 1 ELSE 0 END WHERE id=sqlc.arg(id);
-- name: SetListingFIGI :exec
UPDATE listings SET figi=? WHERE id=?;
-- name: PutInstrumentReferenceAttempt :exec
INSERT INTO instrument_reference_attempts(instrument_id,evidence_id,status,rule) VALUES(?,?,?,?) ON CONFLICT DO NOTHING;
-- name: WakeReference :exec
UPDATE instruments SET reference_next='' WHERE reference_status<>'conflict' AND (SELECT paused FROM profile WHERE singleton=1)=0;
-- name: InstrumentReferenceAttempts :many
SELECT instrument_reference_attempts.*,reference_evidence.retrieved FROM instrument_reference_attempts JOIN reference_evidence ON reference_evidence.id=evidence_id WHERE instrument_id=? ORDER BY reference_evidence.rowid DESC;
-- name: EventLinks :many
SELECT execution_id FROM event_links WHERE account_id=? AND reference=? ORDER BY execution_id;
-- name: ExecutionLinks :many
SELECT reference FROM event_links WHERE execution_id=?;
-- name: UpdateConnection :exec
INSERT INTO access.configurations(id,provider,settings) VALUES(sqlc.arg(id),sqlc.arg(provider),sqlc.arg(settings)) ON CONFLICT(id) DO UPDATE SET settings=excluded.settings WHERE configurations.provider=excluded.provider;
-- name: ResetConnectionWork :exec
UPDATE work SET revision=revision+1,state_version='',state=X'',next=CASE WHEN (SELECT paused FROM profile WHERE singleton=1)=1 THEN next ELSE '' END,failure='',observed='',acknowledged='' WHERE connection_id=?;

-- name: InstrumentObservations :many
SELECT execution_observations.* FROM execution_observations JOIN executions ON executions.id=execution_id WHERE executions.instrument_id=? ORDER BY execution_observations.rowid DESC LIMIT 100;

-- name: SetUnderlier :exec
UPDATE instruments SET underlier_id=sqlc.narg(underlier_id),revision=revision+1 WHERE id=sqlc.arg(id) AND underlier_id IS NOT sqlc.narg(underlier_id);

-- name: EnrichInstrument :exec
UPDATE instruments SET revision=revision+1,kind=sqlc.arg(kind),reported=sqlc.arg(reported),name=CASE WHEN name='' THEN sqlc.arg(name) ELSE name END,ticker=CASE WHEN ticker='' THEN sqlc.arg(ticker) ELSE ticker END, reference_next='' WHERE id=sqlc.arg(id);

-- name: PutLifecycleActivity :exec
INSERT INTO executions(kind,id,account_id,instrument_id,accepted,status,rule) VALUES('lifecycle',?,?,?,?,'accepted',?);
-- name: PutRelationship :exec
INSERT INTO activity_relationships(activity_id,kind,target) VALUES(?,?,?) ON CONFLICT DO NOTHING;
-- name: Relationships :many
SELECT r.kind,r.target,coalesce(e.execution_id,'') AS target_id FROM activity_relationships r LEFT JOIN executions a ON a.id=r.activity_id LEFT JOIN event_links e ON e.account_id=a.account_id AND e.reference=r.target WHERE r.activity_id=? ORDER BY r.kind,r.target,e.execution_id;

-- name: PutConversionActivity :exec
INSERT INTO executions(kind,id,account_id,accepted,status,rule) VALUES('conversion',?,?,?,'accepted',?);

-- name: SeedActivityBases :exec
INSERT INTO activity_bases(activity_id,data,instrument_id,listing_id) SELECT id,accepted,instrument_id,listing_id FROM executions WHERE id NOT IN (SELECT activity_id FROM activity_bases);
-- name: ActivityBase :one
SELECT * FROM activity_bases WHERE activity_id=?;
-- name: ActivityInputs :many
SELECT records.* FROM execution_observations o JOIN records USING(artifact_id,version,location) WHERE o.execution_id=? ORDER BY o.rowid;
-- name: ProjectActivity :exec
UPDATE executions SET accepted=sqlc.arg(accepted),status=sqlc.arg(status),instrument_id=sqlc.narg(instrument_id),listing_id=sqlc.narg(listing_id),revision=revision+1 WHERE id=sqlc.arg(id) AND (accepted<>sqlc.arg(accepted) OR status<>sqlc.arg(status) OR instrument_id IS NOT sqlc.narg(instrument_id) OR listing_id IS NOT sqlc.narg(listing_id));
-- name: PutSourceCorrection :exec
INSERT INTO source_corrections(artifact_id,version,location,account_id,kind,target,replacement) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING;
-- name: SourceCorrections :many
SELECT * FROM source_corrections ORDER BY artifact_id,version,location;
-- name: CorrectionStatus :exec
UPDATE source_corrections SET status=? WHERE artifact_id=? AND version=? AND location=?;
-- name: SetObservationOutcome :exec
UPDATE execution_observations SET outcome=? WHERE artifact_id=? AND version=? AND location=?;

-- name: Decisions :many
SELECT * FROM operator_decisions WHERE target_kind=? AND target_id=? ORDER BY rowid;
-- name: DecisionSubmission :one
SELECT * FROM operator_decisions WHERE submission=?;
-- name: PutDecision :exec
INSERT INTO operator_decisions(id,target_kind,target_id,scope,text,currency,action,actor,at,rationale,supersedes,submission,request) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?);
-- name: AdvanceActivityRevision :exec
UPDATE executions SET revision=revision+1 WHERE id=?;
-- name: AdvanceInstrumentRevision :exec
UPDATE instruments SET revision=revision+1 WHERE id=?;
-- name: SeedInstrumentSelections :exec
INSERT INTO instrument_selections(instrument_id,name,ticker,figi,evidence,status,underlier_id) SELECT id,name,ticker,reference_figi,evidence_id,reference_status,underlier_id FROM instruments WHERE id NOT IN (SELECT instrument_id FROM instrument_selections);
-- name: InstrumentSelection :one
SELECT * FROM instrument_selections WHERE instrument_id=?;
-- name: SaveInstrumentSelection :exec
INSERT INTO instrument_selections(instrument_id,name,ticker,figi,evidence,status,underlier_id) VALUES(?,?,?,?,?,?,?) ON CONFLICT(instrument_id) DO UPDATE SET name=excluded.name,ticker=excluded.ticker,figi=excluded.figi,evidence=excluded.evidence,status=excluded.status,underlier_id=excluded.underlier_id;
-- name: ProjectInstrument :exec
UPDATE instruments SET name=sqlc.arg(name),ticker=sqlc.arg(ticker),reference_figi=sqlc.arg(figi),evidence_id=sqlc.arg(evidence),reference_status=sqlc.arg(status),underlier_id=sqlc.narg(underlier_id),revision=revision+1 WHERE id=sqlc.arg(id) AND (name<>sqlc.arg(name) OR ticker<>sqlc.arg(ticker) OR reference_figi<>sqlc.arg(figi) OR reference_status<>sqlc.arg(status) OR underlier_id IS NOT sqlc.narg(underlier_id));
-- name: Listing :one
SELECT * FROM listings WHERE id=?;
-- name: RetainedReferenceCandidates :many
SELECT e.id,e.request,e.response FROM instrument_reference_attempts a JOIN reference_evidence e ON a.evidence_id=e.id WHERE a.instrument_id=? ORDER BY e.rowid;

-- name: ReferenceNext :exec
UPDATE instruments SET reference_next=? WHERE id=?;

-- name: EnsureInstrumentSelection :exec
INSERT INTO instrument_selections(instrument_id,name,ticker,figi,evidence,status,underlier_id) SELECT id,name,ticker,reference_figi,evidence_id,reference_status,underlier_id FROM instruments WHERE id=? ON CONFLICT DO NOTHING;

-- name: RetryProcessing :execrows
UPDATE artifacts SET processed=0,processing_failure='',processing_next='' WHERE artifacts.id=? AND operation='acquire' AND EXISTS(SELECT 1 FROM accounts WHERE accounts.connection_id=artifacts.connection_id AND accounts.stream=artifacts.stream);
-- name: RecoveryJobs :many
SELECT * FROM recovery_jobs ORDER BY rowid DESC LIMIT 10;
-- name: RecoveryJob :one
SELECT * FROM recovery_jobs WHERE id=?;
-- name: ActiveRecovery :one
SELECT * FROM recovery_jobs WHERE status IN ('queued','running') LIMIT 1;
-- name: PutRecovery :exec
INSERT INTO recovery_jobs(id,status,total,actor,created) VALUES(?,'queued',?,?,?);
-- name: PutRecoveryItem :exec
INSERT INTO recovery_items(job_id,group_id,kind,entity_id) VALUES(?,?,?,?);
-- name: NextRecoveryGroup :one
SELECT group_id FROM recovery_items WHERE job_id=? AND complete=0 ORDER BY group_id LIMIT 1;
-- name: RecoveryGroup :many
SELECT * FROM recovery_items WHERE job_id=? AND group_id=? ORDER BY kind,entity_id LIMIT 129;
-- name: CompleteRecoveryGroup :exec
UPDATE recovery_items SET complete=1 WHERE job_id=? AND group_id=?;
-- name: AdvanceRecovery :exec
UPDATE recovery_jobs SET status='running',completed=(SELECT count(*) FROM recovery_items WHERE job_id=recovery_jobs.id AND complete=1),failure='' WHERE id=?;
-- name: FinishRecovery :exec
UPDATE recovery_jobs SET status='complete',completed=total,failure='' WHERE id=?;
-- name: FailRecovery :exec
UPDATE recovery_jobs SET status='failed',failure='Projection rebuild failed; retained evidence is safe. Check daemon status and retry.' WHERE id=?;
-- name: RetryRecovery :execrows
UPDATE recovery_jobs SET status='queued',failure='' WHERE recovery_jobs.id=? AND recovery_jobs.status='failed' AND recovery_jobs.rowid=(SELECT max(rowid) FROM recovery_jobs) AND NOT EXISTS(SELECT 1 FROM recovery_jobs WHERE status IN ('queued','running'));
-- name: AllRelationships :many
SELECT r.activity_id,coalesce(e.execution_id,'') AS target_id FROM activity_relationships r JOIN executions a ON a.id=r.activity_id LEFT JOIN event_links e ON e.account_id=a.account_id AND e.reference=r.target;

-- name: PutDescriptor :exec
INSERT INTO connections(id,provider) VALUES(?,?);
-- name: SaveNormalizationReceipt :exec
INSERT INTO normalization_receipts(artifact_id,version,counts) VALUES(?,?,?) ON CONFLICT DO NOTHING;
-- name: RefreshExecution :exec
INSERT INTO operational.execution(connection_id,stream,operation) SELECT connection_id,stream,operation FROM scheduled WHERE enabled=1 ON CONFLICT DO NOTHING;
-- name: DeleteExecution :exec
DELETE FROM operational.execution WHERE NOT EXISTS(SELECT 1 FROM scheduled s WHERE s.connection_id=execution.connection_id AND s.stream=execution.stream AND s.operation=execution.operation AND s.enabled=1);
-- name: Profile :one
SELECT * FROM profile WHERE singleton=1;
-- name: MissingAccess :many
SELECT c.id,c.provider FROM connections c LEFT JOIN access.configurations a ON a.id=c.id WHERE a.id IS NULL ORDER BY c.rowid;
-- name: ReviewReference :exec
UPDATE profile SET reference_review=0 WHERE singleton=1;
-- name: ResumeStorage :execrows
UPDATE profile SET paused=0,warehouse_reset=0,resumed_by=?,resumed_at=? WHERE singleton=1 AND paused=1 AND recovery_revision=? AND reference_review=0 AND NOT EXISTS(SELECT 1 FROM connections c LEFT JOIN access.configurations a ON a.id=c.id WHERE a.id IS NULL OR NOT EXISTS(SELECT 1 FROM access.credentials k WHERE k.connection_id=c.id));
-- name: Descriptor :one
SELECT * FROM connections WHERE id=?;
-- name: ConnectionAccess :one
SELECT count(*) FROM access.configurations c WHERE c.id=? AND EXISTS(SELECT 1 FROM access.credentials k WHERE k.connection_id=c.id);

-- name: AccessReadiness :one
SELECT EXISTS(SELECT 1 FROM access.configurations WHERE id=sqlc.arg(connection_id)) AS settings_stored, EXISTS(SELECT 1 FROM access.credentials WHERE connection_id=sqlc.arg(connection_id)) AS credential_stored;

-- name: RelatedSourceCorrections :many
SELECT DISTINCT c.* FROM source_corrections c JOIN event_links e ON e.account_id=c.account_id AND (e.reference=c.target OR e.reference=c.replacement) WHERE e.execution_id=? ORDER BY c.artifact_id,c.version,c.location;
-- name: ArtifactObservations :many
SELECT * FROM execution_observations WHERE artifact_id=? ORDER BY rowid;
