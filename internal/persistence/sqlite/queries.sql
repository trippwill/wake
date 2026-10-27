-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- name: CountTables :one
SELECT count(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%';
-- name: ReadFormat :one
SELECT version FROM wake_format;
-- name: SetFormat :exec
INSERT INTO wake_format(version) VALUES (?);

-- name: PutConnection :exec
INSERT INTO connections(id,provider,settings) VALUES(?,?,?);
-- name: Connection :one
SELECT * FROM connections WHERE id=?;
-- name: PutSecret :exec
INSERT INTO credentials(connection_id,key,encrypted) VALUES(?,?,?);
-- name: Secret :one
SELECT encrypted FROM credentials WHERE connection_id=? AND key=?;
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
INSERT INTO attempts(id,artifact_id,version,outcome,counts) VALUES(?,?,?,'running',X'');
-- name: FinishAttempt :exec
UPDATE attempts SET outcome=?,counts=? WHERE id=?;
-- name: Succeeded :one
SELECT counts FROM attempts WHERE artifact_id=? AND version=? AND outcome='succeeded' LIMIT 1;
-- name: Attempts :many
SELECT * FROM attempts WHERE artifact_id=? ORDER BY rowid;
-- name: InterruptAttempts :exec
UPDATE attempts SET outcome='interrupted' WHERE outcome='running';
-- name: PutRecord :exec
INSERT INTO records(artifact_id,version,location,kind,data) VALUES(?,?,?,?,?);
-- name: Records :many
SELECT * FROM records WHERE artifact_id=? AND version=? ORDER BY location;
-- name: Pacing :one
SELECT next FROM pacing WHERE provider=? AND policy=? AND scope=?;
-- name: SetPacing :exec
INSERT INTO pacing(provider,policy,scope,next) VALUES(?,?,?,?) ON CONFLICT(provider,policy,scope) DO UPDATE SET next=excluded.next;

-- name: ReplaceSecret :exec
INSERT INTO credentials(connection_id,key,encrypted) VALUES(?,?,?) ON CONFLICT(connection_id,key) DO UPDATE SET encrypted=excluded.encrypted;
-- name: Connections :many
SELECT id FROM connections ORDER BY rowid;
-- name: SetScheduled :exec
INSERT INTO scheduled(connection_id,stream,operation,enabled) VALUES(?,?,?,?) ON CONFLICT(connection_id,stream,operation) DO UPDATE SET enabled=excluded.enabled;
-- name: RetryWork :exec
UPDATE work SET revision=revision+1,failure='',next='' WHERE connection_id=? AND stream=? AND operation=?;
-- name: RestartDiscovery :exec
UPDATE work SET revision=revision+1,state_version='',state=X'',failure='',next='' WHERE connection_id=? AND stream=? AND operation='discover';
-- name: PendingWork :many
SELECT work.connection_id,work.stream,work.operation FROM work JOIN scheduled USING(connection_id,stream,operation) WHERE enabled=1 AND (next='' OR julianday(next)<=julianday(?)) AND failure NOT IN ('source access not authorized','invalid provider contract value','source capability unavailable','malformed source evidence','source resource limit exceeded') ORDER BY work.rowid LIMIT 20;
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
SELECT id,processed,processing_failure,requested_from,requested_to,reported_from,reported_to, (SELECT count(*) FROM records WHERE records.artifact_id=artifacts.id AND kind='trade') AS trade_claims, (SELECT count(*) FROM records WHERE records.artifact_id=artifacts.id AND kind='rejected') AS excluded_rows, (SELECT count(*) FROM execution_observations WHERE execution_observations.artifact_id=artifacts.id AND outcome IN ('identity needs review','native identity needs review')) AS needs_review FROM artifacts WHERE connection_id=? AND operation='acquire' ORDER BY rowid DESC LIMIT 10;
-- name: InstrumentBySource :one
SELECT * FROM instruments WHERE source_key=?;
-- name: InstrumentByFIGI :one
SELECT * FROM instruments WHERE share_class_figi=?;
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
SELECT * FROM reference_settings WHERE singleton=1;
-- name: SetReferenceKey :exec
INSERT INTO reference_settings(singleton,encrypted,failure) VALUES(1,?,'') ON CONFLICT(singleton) DO UPDATE SET encrypted=excluded.encrypted,failure='';
-- name: ReferenceFailure :exec
INSERT INTO reference_settings(singleton,encrypted,failure) VALUES(1,X'',?) ON CONFLICT(singleton) DO UPDATE SET failure=excluded.failure;
-- name: ReferenceBudget :one
SELECT * FROM reference_budget WHERE singleton=1;
-- name: SetReferenceBudget :exec
INSERT INTO reference_budget(singleton,window_start,used,cooldown) VALUES(1,?,?,?) ON CONFLICT(singleton) DO UPDATE SET window_start=excluded.window_start,used=excluded.used,cooldown=excluded.cooldown;
-- name: PutReferenceEvidence :exec
INSERT INTO reference_evidence(id,retrieved,request,response,http_status,headers) VALUES(?,?,?,?,?,?);
-- name: ReferenceEvidence :one
SELECT * FROM reference_evidence WHERE id=?;
-- name: CachedReference :one
SELECT * FROM reference_cache WHERE key=?;
-- name: CacheReference :exec
INSERT INTO reference_cache(key,status,records,expires,evidence_id) VALUES(?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET status=excluded.status,records=excluded.records,expires=excluded.expires,evidence_id=excluded.evidence_id;
-- name: PendingReference :many
SELECT * FROM instruments WHERE reference_status<>'conflict' AND (reference_next='' OR julianday(reference_next)<=julianday(?)) ORDER BY rowid LIMIT 100;
-- name: ResolveInstrument :exec
UPDATE instruments SET name=?,ticker=?,share_class_figi=?,evidence_id=?,reference_status=?,reference_next=? WHERE id=?;
-- name: SetListingFIGI :exec
UPDATE listings SET figi=? WHERE id=?;
-- name: PutInstrumentReferenceAttempt :exec
INSERT INTO instrument_reference_attempts(instrument_id,evidence_id,status,rule) VALUES(?,?,?,?) ON CONFLICT DO NOTHING;
-- name: WakeReference :exec
UPDATE instruments SET reference_next='' WHERE reference_status<>'conflict';
-- name: InstrumentReferenceAttempts :many
SELECT instrument_reference_attempts.*,reference_evidence.retrieved FROM instrument_reference_attempts JOIN reference_evidence ON reference_evidence.id=evidence_id WHERE instrument_id=? ORDER BY reference_evidence.rowid DESC;
-- name: EventLinks :many
SELECT execution_id FROM event_links WHERE account_id=? AND reference=? ORDER BY execution_id;
-- name: ExecutionLinks :many
SELECT reference FROM event_links WHERE execution_id=?;
-- name: UpdateConnection :exec
UPDATE connections SET settings=? WHERE id=? AND provider=?;
-- name: ResetConnectionWork :exec
UPDATE work SET revision=revision+1,state_version='',state=X'',next='',failure='',observed='',acknowledged='' WHERE connection_id=?;

-- name: InstrumentObservations :many
SELECT execution_observations.* FROM execution_observations JOIN executions ON executions.id=execution_id WHERE executions.instrument_id=? ORDER BY execution_observations.rowid DESC LIMIT 100;

-- name: ExecutionKnownTrades :many
SELECT r.data FROM records r JOIN execution_observations o
ON o.artifact_id=r.artifact_id AND o.version=r.version AND o.location=r.location
WHERE o.execution_id=? AND r.kind='trade';
