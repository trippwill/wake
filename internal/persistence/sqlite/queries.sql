-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- name: CountTables :one
SELECT count(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%';
-- name: PutConnection :exec
INSERT INTO access.configurations(id,provider,name,settings) VALUES(?,?,?,?);
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
UPDATE work SET failure=?,failure_code=? WHERE connection_id=? AND stream=? AND operation=?;
-- name: AdvanceWork :execrows
UPDATE work SET revision=revision+1,state_version=?,state=?,next=?,acknowledged=?,acknowledged_completed=?,acknowledged_total=?,failure=?,failure_code=? WHERE connection_id=? AND stream=? AND operation=? AND revision=?;
-- name: PutArtifact :exec
INSERT INTO artifacts(id,connection_id,stream,operation,format,media_type,retrieved,hash,content,requested_from,requested_to,reported_from,reported_to) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?);
-- name: Retained :one
SELECT artifacts.*,connections.provider FROM artifacts JOIN connections ON connections.id=artifacts.connection_id WHERE artifacts.id=?;
-- name: PutCandidate :exec
INSERT INTO candidates(receipt_id,artifact_id,authority,kind,scope,value,name,currency) VALUES(?,?,?,?,?,?,?,?);
-- name: Candidates :many
SELECT DISTINCT candidates.artifact_id,candidates.authority,candidates.kind,candidates.scope,candidates.value,candidates.name,candidates.currency FROM candidates JOIN artifacts ON artifacts.id=candidates.artifact_id WHERE artifacts.connection_id=? ORDER BY candidates.artifact_id,candidates.authority,candidates.kind,candidates.scope,candidates.value;
-- name: StartAttempt :exec
INSERT INTO main.attempts(id,artifact_id,version,outcome,counts) VALUES(?,?,?,'running',X'');
-- name: FinishAttempt :exec
INSERT INTO main.attempts(id,artifact_id,version,outcome,counts) SELECT a.id,a.artifact_id,a.version,?,? FROM main.attempts a WHERE a.id=? ON CONFLICT(id) DO UPDATE SET outcome=excluded.outcome,counts=excluded.counts;
-- name: Succeeded :one
SELECT counts FROM normalization_receipts WHERE artifact_id=? AND version=?;
-- name: Attempts :many
SELECT * FROM main.attempts WHERE artifact_id=? ORDER BY rowid;
-- name: PutRecord :exec
INSERT INTO records(artifact_id,version,location,claim_id,ordinal) VALUES(?,?,?,?,?);
-- name: Records :many
SELECT * FROM record_values WHERE artifact_id=? AND version=? ORDER BY location;
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
UPDATE work SET revision=revision+1,failure='',failure_code='',next=CASE WHEN (SELECT paused FROM profile WHERE singleton=1)=1 THEN next ELSE '' END WHERE connection_id=? AND stream=? AND operation=?;
-- name: RestartDiscovery :exec
UPDATE work SET revision=revision+1,state_version=CASE WHEN (SELECT paused FROM profile WHERE singleton=1)=1 THEN state_version ELSE '' END,state=CASE WHEN (SELECT paused FROM profile WHERE singleton=1)=1 THEN state ELSE X'' END,failure='',failure_code='',next=CASE WHEN (SELECT paused FROM profile WHERE singleton=1)=1 THEN next ELSE '' END WHERE connection_id=? AND stream=? AND operation='discover';
-- name: RefreshPolls :execrows
UPDATE work
SET next=CASE WHEN json_extract(CAST(state AS TEXT), '$.not_before')>CAST(sqlc.arg(now) AS TEXT)
              THEN json_extract(CAST(state AS TEXT), '$.not_before') ELSE CAST(sqlc.arg(now) AS TEXT) END
WHERE operation='acquire'
  AND (SELECT paused FROM profile WHERE singleton=1)=0
  AND failure='' AND failure_code=''
  AND julianday(next)>julianday(sqlc.arg(now))
  AND julianday(next)>CASE WHEN state_version=sqlc.arg(poll_version)
                           THEN julianday(json_extract(CAST(state AS TEXT), '$.not_before')) END
  AND EXISTS(SELECT 1 FROM scheduled WHERE scheduled.connection_id=work.connection_id
             AND scheduled.stream=work.stream AND scheduled.operation=work.operation AND enabled=1)
  AND EXISTS(SELECT 1 FROM accounts WHERE accounts.connection_id=work.connection_id);
-- name: QueuedPoll :one
SELECT work.state FROM work JOIN scheduled USING(connection_id,stream,operation)
WHERE work.connection_id=? AND work.stream=? AND work.operation='acquire' AND work.state_version=?
  AND enabled=1 AND work.next<>'' AND work.failure='' AND work.failure_code=''
  AND (SELECT paused FROM profile WHERE singleton=1)=0
  AND EXISTS(SELECT 1 FROM accounts WHERE accounts.connection_id=work.connection_id);
-- name: SetPollDeadline :exec
UPDATE work SET next=? WHERE connection_id=? AND stream=? AND operation='acquire';
-- name: PendingWork :many
SELECT work.connection_id,work.stream,work.operation FROM work JOIN scheduled USING(connection_id,stream,operation) WHERE (SELECT paused FROM profile WHERE singleton=1)=0 AND enabled=1 AND (next='' OR julianday(next)<=julianday(?)) AND failure_code NOT IN ('unauthorized','invalid','unavailable','malformed','limit') ORDER BY work.rowid LIMIT 20;
-- name: WorkStatuses :many
SELECT work.stream, work.operation, work.observed, work.observed_completed, work.observed_total, work.failure, work.next,
 coalesce(scheduled.enabled,0)=1 AS scheduled,
 -- Match PendingWork's terminal failures.
 CAST(work.failure_code IN ('unauthorized','invalid','unavailable','malformed','limit') AS INTEGER) AS retry_blocked,
 -- Find the newest receipt per retained body through receipts_body before comparing bodies.
 CAST(coalesce((SELECT receipt.retrieved FROM acquisition_receipts receipt WHERE receipt.sequence=(
  SELECT max((SELECT r.sequence FROM acquisition_receipts r WHERE r.artifact_id=a.id AND r.operation=work.operation ORDER BY r.sequence DESC LIMIT 1))
  FROM artifacts a WHERE a.connection_id=work.connection_id AND a.stream=work.stream
 )), '') AS TEXT) AS last_retrieved
FROM work LEFT JOIN scheduled USING(connection_id,stream,operation) WHERE work.connection_id=? ORDER BY work.operation,work.stream;
-- name: DisableDiscovery :exec
UPDATE scheduled SET enabled=0 WHERE connection_id=? AND operation='discover';
-- name: Account :one
SELECT * FROM accounts ORDER BY rowid LIMIT 1;
-- name: AccountByConnection :one
SELECT * FROM accounts WHERE connection_id=?;
-- name: AccountByID :one
SELECT * FROM accounts WHERE id=?;
-- name: AccountCount :one
SELECT count(*) FROM accounts;
-- name: Accounts :many
SELECT * FROM accounts ORDER BY rowid;
-- name: AccountBySource :one
SELECT * FROM accounts WHERE provider=? AND authority=? AND kind=? AND scope=? AND value=?;
-- name: CandidateExists :one
SELECT count(*) FROM eligible_candidates WHERE connection_id=? AND artifact_id=? AND authority=? AND kind=? AND scope=? AND value=?;
-- name: PutAccount :exec
INSERT INTO accounts(id,name,currency,connection_id,provider,authority,kind,scope,value,artifact_id,rule) VALUES(?,?,?,?,?,?,?,?,?,?,?);
-- name: CandidateProfiles :many
SELECT artifact_id,authority,kind,scope,value,name,currency FROM (
 SELECT candidates.*,receipt.sequence AS appearance,row_number() OVER (PARTITION BY candidates.authority,candidates.kind,candidates.scope,candidates.value ORDER BY receipt.sequence DESC) AS ranking
 FROM candidates JOIN artifacts ON artifacts.id=candidates.artifact_id JOIN acquisition_receipts receipt ON receipt.id=candidates.receipt_id
 WHERE artifacts.connection_id=? AND receipt.operation='discover'
) WHERE ranking=1 ORDER BY appearance DESC,authority,kind,scope,value;
-- name: PendingArtifacts :many
SELECT artifacts.id,connections.provider FROM artifacts JOIN connections ON connections.id=artifacts.connection_id JOIN accounts ON accounts.connection_id=artifacts.connection_id AND accounts.provider=connections.provider WHERE (processing_next='' OR julianday(processing_next)<=julianday(sqlc.arg(now))) AND connections.provider IN (sqlc.slice('providers')) AND EXISTS(SELECT 1 FROM acquisition_receipts receipt WHERE receipt.artifact_id=artifacts.id AND receipt.operation='acquire') AND artifacts.processed=0 ORDER BY artifacts.rowid LIMIT 20;
-- name: ArtifactProcessed :exec
UPDATE artifacts SET processed=1,processing_failure='',processing_next='' WHERE id=?;
-- name: ArtifactFailure :exec
UPDATE artifacts SET processing_failure=?,processing_next=? WHERE id=?;
-- name: ProcessingStatuses :many
SELECT artifacts.id,artifacts.stream,receipt.id AS receipt_id,receipt.retrieved,receipt.hash,receipt.policy,artifacts.processed,artifacts.processing_failure,receipt.requested_from,receipt.requested_to,receipt.reported_from,receipt.reported_to,
 CAST(coalesce((SELECT json_extract(counts,'$.Activities') FROM normalization_receipts n WHERE n.artifact_id=artifacts.id ORDER BY n.rowid DESC LIMIT 1),0) AS INTEGER) AS activity_claims,
 CAST(coalesce((SELECT json_extract(counts,'$.Rejected') FROM normalization_receipts n WHERE n.artifact_id=artifacts.id ORDER BY n.rowid DESC LIMIT 1),0) AS INTEGER) AS excluded_rows,
 (SELECT count(*) FROM execution_observations WHERE execution_observations.artifact_id=artifacts.id AND outcome IN ('identity-review','native-review','incomplete','incomplete-conflict')) AS needs_review
FROM acquisition_receipts receipt JOIN artifacts ON artifacts.id=receipt.artifact_id WHERE artifacts.connection_id=? AND receipt.operation='acquire' ORDER BY receipt.rowid DESC LIMIT 10;
-- name: InstrumentBySource :one
SELECT target.* FROM instruments original JOIN instruments target ON target.id=coalesce((SELECT target_id FROM resource_aliases WHERE kind='instruments' AND id=original.id),original.id) WHERE original.source_key=?;
-- name: QualifyInstrument :exec
UPDATE instruments SET source_key=sqlc.arg(source_key) WHERE id=sqlc.arg(id);
-- name: CompetingFIGI :many
SELECT instruments.* FROM instruments
JOIN (
 SELECT projected.id FROM instruments projected WHERE projected.reference_figi=sqlc.arg(figi)
 UNION
 SELECT retained.instrument_id FROM instrument_selections retained WHERE retained.figi=sqlc.arg(figi)
) claims ON claims.id=instruments.id
WHERE instruments.id<>sqlc.arg(current_id) AND instruments.id NOT IN (SELECT id FROM resource_aliases WHERE kind='instruments') ORDER BY instruments.id;
-- name: PutInstrumentRoot :exec
INSERT INTO instruments(id,source_key,name,ticker,kind,reported,rule,reference_status) VALUES(?,?,?,?,?,?,?,'provisional');
-- name: Instruments :many
SELECT * FROM instruments WHERE id NOT IN (SELECT id FROM resource_aliases WHERE kind='instruments') ORDER BY rowid;
-- name: Instrument :one
SELECT * FROM instruments WHERE id=?;
-- name: PutListing :exec
INSERT INTO listings(id,instrument_id,venue,ticker,currency) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING;
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
INSERT INTO event_links(account_id,reference,execution_id,distinct_event) VALUES(?,?,?,?) ON CONFLICT(account_id,reference,execution_id) DO UPDATE SET distinct_event=max(event_links.distinct_event,excluded.distinct_event);
-- name: PutObservation :exec
INSERT INTO execution_observations(artifact_id,version,location,execution_id,outcome,rule) VALUES(?,?,?,?,?,?);
-- name: ObservationExists :one
SELECT count(*) FROM execution_observations WHERE artifact_id=? AND version=? AND location=?;
-- name: ConflictExecution :exec
UPDATE executions SET review_reasons=review_reasons|1 WHERE id=?;
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
SELECT * FROM main.reference_cache WHERE key=?;
-- name: CacheReference :exec
INSERT INTO main.reference_cache(key,status,records,expires,evidence_id) VALUES(?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET status=excluded.status,records=excluded.records,expires=excluded.expires,evidence_id=excluded.evidence_id;
-- name: PendingReference :many
SELECT * FROM instruments WHERE (SELECT paused FROM profile WHERE singleton=1)=0 AND id NOT IN (SELECT id FROM resource_aliases WHERE kind='instruments') AND reference_reason<>'missing_identifier' AND reference_status<>'conflict' AND (reference_next='' OR julianday(reference_next)<=julianday(?)) ORDER BY rowid LIMIT 100;
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
SELECT reference FROM event_links WHERE execution_id=? AND distinct_event=1;
-- name: UpdateConnection :exec
INSERT INTO access.configurations(id,provider,name,settings) VALUES(sqlc.arg(id),sqlc.arg(provider),sqlc.arg(name),sqlc.arg(settings)) ON CONFLICT(id) DO UPDATE SET settings=excluded.settings,name=excluded.name WHERE configurations.provider=excluded.provider;

-- name: SetUnderlier :exec
UPDATE instruments SET underlier_id=sqlc.narg(underlier_id),revision=revision+1 WHERE id=sqlc.arg(id) AND underlier_id IS NOT sqlc.narg(underlier_id);

-- name: EnrichInstrument :exec
UPDATE instruments SET revision=revision+1,kind=sqlc.arg(kind),reported=sqlc.arg(reported),name=CASE WHEN name='' THEN sqlc.arg(name) ELSE name END,ticker=CASE WHEN ticker='' THEN sqlc.arg(ticker) ELSE ticker END, reference_next='',reference_reason=CASE WHEN reference_reason='missing_identifier' THEN '' ELSE reference_reason END WHERE id=sqlc.arg(id);

-- name: PutLifecycleActivity :exec
INSERT INTO executions(kind,id,account_id,instrument_id,accepted,status,rule) VALUES('lifecycle',?,?,?,?,'accepted',?);
-- name: PutRelationship :exec
INSERT INTO activity_relationships(activity_id,kind,target) VALUES(?,?,?) ON CONFLICT DO NOTHING;
-- name: Relationships :many
SELECT r.kind,r.target,coalesce(e.execution_id,'') AS target_id FROM activity_relationships r LEFT JOIN executions a ON a.id=r.activity_id LEFT JOIN event_links e ON e.account_id=a.account_id AND e.reference=r.target WHERE r.activity_id=? ORDER BY r.kind,r.target,e.execution_id;

-- name: PutConversionActivity :exec
INSERT INTO executions(kind,id,account_id,accepted,status,rule) VALUES('conversion',?,?,?,'accepted',?);

-- name: ActivityBase :one
SELECT * FROM activity_bases WHERE activity_id=?;
-- name: ActivityInputs :many
SELECT record_values.* FROM execution_observations o JOIN record_values USING(artifact_id,version,location) JOIN claim_processing p ON p.claim_id=record_values.claim_id AND p.ordinal=record_values.ordinal WHERE o.execution_id=? AND record_values.kind='activity' ORDER BY p.rowid;
-- name: ProjectActivity :exec
UPDATE executions SET accepted=sqlc.arg(accepted),status=sqlc.arg(status),review_reasons=sqlc.arg(review_reasons),selection_authority=sqlc.arg(selection_authority),instrument_id=sqlc.narg(instrument_id),listing_id=sqlc.narg(listing_id),revision=revision+1 WHERE id=sqlc.arg(id) AND (accepted<>sqlc.arg(accepted) OR status<>sqlc.arg(status) OR review_reasons<>sqlc.arg(review_reasons) OR selection_authority<>sqlc.arg(selection_authority) OR instrument_id IS NOT sqlc.narg(instrument_id) OR listing_id IS NOT sqlc.narg(listing_id));
-- name: PutSourceCorrection :exec
INSERT INTO source_corrections(artifact_id,version,location,account_id,kind,target,replacement) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING;
-- name: SourceCorrections :many
SELECT * FROM source_corrections ORDER BY artifact_id,version,location;
-- name: CorrectionStatus :exec
UPDATE source_corrections SET status=? WHERE artifact_id=? AND version=? AND location=?;
-- name: SetObservationOutcome :exec
UPDATE execution_observations SET outcome=? WHERE artifact_id=? AND version=? AND location=?;

-- name: Decisions :many
SELECT sqlc.embed(d), EXISTS(SELECT 1 FROM decision_supersessions x WHERE x.previous_id=d.id) AS superseded FROM operator_decisions d WHERE d.target_kind=sqlc.arg(target_kind) AND (d.target_id=sqlc.arg(target_id) OR (d.target_kind='instrument' AND d.target_id IN (SELECT a.id FROM resource_aliases a WHERE a.kind='instruments' AND a.target_id=sqlc.arg(target_id)))) ORDER BY d.rowid;
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
UPDATE instruments SET name=sqlc.arg(name),ticker=sqlc.arg(ticker),reference_figi=sqlc.arg(figi),evidence_id=sqlc.arg(evidence),reference_status=sqlc.arg(status),selection_authority=sqlc.arg(selection_authority),underlier_id=sqlc.narg(underlier_id),revision=revision+1 WHERE id=sqlc.arg(id) AND (name<>sqlc.arg(name) OR ticker<>sqlc.arg(ticker) OR reference_figi<>sqlc.arg(figi) OR reference_status<>sqlc.arg(status) OR selection_authority<>sqlc.arg(selection_authority) OR underlier_id IS NOT sqlc.narg(underlier_id));
-- name: Listing :one
SELECT * FROM listings WHERE id=?;
-- name: RetainedReferenceCandidates :many
SELECT e.id,e.request,e.response FROM instrument_reference_attempts a JOIN reference_evidence e ON a.evidence_id=e.id WHERE a.instrument_id=? ORDER BY e.rowid;

-- name: ReferenceNext :exec
UPDATE instruments SET reference_next=? WHERE id=?;

-- name: EnsureInstrumentSelection :exec
INSERT INTO instrument_selections(instrument_id,name,ticker,figi,evidence,status,underlier_id) SELECT id,name,ticker,reference_figi,evidence_id,reference_status,underlier_id FROM instruments WHERE id=? ON CONFLICT DO NOTHING;

-- name: RetryProcessing :execrows
UPDATE artifacts SET processed=0,processing_failure='',processing_next='' WHERE artifacts.id=? AND EXISTS(SELECT 1 FROM acquisition_receipts receipt WHERE receipt.artifact_id=artifacts.id AND receipt.operation='acquire') AND EXISTS(SELECT 1 FROM accounts WHERE accounts.connection_id=artifacts.connection_id);
-- name: PutDescriptor :exec
INSERT INTO connections(id,provider,name) VALUES(?,?,?);
-- name: SaveNormalizationReceipt :exec
INSERT INTO normalization_receipts(artifact_id,version,counts) VALUES(?,?,?) ON CONFLICT DO NOTHING;
-- name: Profile :one
SELECT * FROM profile WHERE singleton=1;
-- name: MissingAccess :many
SELECT c.id,c.provider FROM connections c LEFT JOIN access.configurations a ON a.id=c.id WHERE a.id IS NULL ORDER BY c.rowid;
-- name: ReviewReference :exec
UPDATE profile SET reference_review=0 WHERE singleton=1;
-- name: ResumeStorage :execrows
UPDATE profile SET paused=0,warehouse_reset=0,resumed_by=?,resumed_at=? WHERE singleton=1 AND paused=1 AND recovery_revision=? AND reference_review=0 AND NOT EXISTS(SELECT 1 FROM connections c LEFT JOIN access.configurations a ON a.id=c.id WHERE a.id IS NULL);
-- name: Descriptor :one
SELECT * FROM connections WHERE id=?;
-- name: ConnectionAccess :one
SELECT count(*) FROM access.configurations c WHERE c.id=?;

-- name: AccessReadiness :one
SELECT EXISTS(SELECT 1 FROM access.configurations WHERE id=sqlc.arg(connection_id)) AS settings_stored, EXISTS(SELECT 1 FROM access.credentials WHERE connection_id=sqlc.arg(connection_id)) AS credential_stored;

-- name: RelatedSourceCorrections :many
SELECT c.* FROM event_links e JOIN source_corrections c ON c.account_id=e.account_id AND c.target=e.reference WHERE e.execution_id=sqlc.arg(execution_id)
UNION
SELECT c.* FROM event_links e JOIN source_corrections c ON c.account_id=e.account_id AND c.replacement=e.reference WHERE e.execution_id=sqlc.arg(execution_id)
ORDER BY artifact_id,version,location;

-- name: UpdateDescriptorName :exec
UPDATE connections SET name=? WHERE id=?;
-- name: CredentialKeys :many
SELECT key FROM access.credentials WHERE connection_id=? ORDER BY key;

-- name: ArtifactProvider :one
SELECT connections.provider FROM artifacts JOIN connections ON connections.id=artifacts.connection_id WHERE artifacts.id=?;

-- name: ReportComparison :one
SELECT artifact_id FROM report_comparisons WHERE connection_id=? AND stream=? AND format=? AND media_type=? AND policy=? AND digest=?;
-- name: PutReportComparison :exec
INSERT INTO report_comparisons(connection_id,stream,format,media_type,policy,digest,artifact_id) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING;
-- name: PutAcquisitionReceipt :exec
INSERT INTO acquisition_receipts(id,artifact_id,operation,retrieved,hash,policy,digest,requested_from,requested_to,reported_from,reported_to,candidate_snapshot) VALUES(?,?,?,?,?,?,?,?,?,?,?,?);
-- name: AcquisitionReceipts :many
SELECT * FROM acquisition_receipts WHERE artifact_id=? ORDER BY rowid;
-- name: PutSourceClaim :exec
INSERT INTO source_claims(id,connection_id,stream,version,kind,data) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING;
-- name: ClaimProcessing :one
SELECT artifact_id,version,location FROM claim_processing WHERE claim_id=? AND ordinal=?;
-- name: PutClaimProcessing :exec
INSERT INTO claim_processing(claim_id,ordinal,artifact_id,version,location) VALUES(?,?,?,?,?);
-- name: CopyClaimCorrection :exec
INSERT INTO source_corrections(artifact_id,version,location,account_id,kind,target,replacement,status) SELECT sqlc.arg(artifact_id),sqlc.arg(version),sqlc.arg(location),account_id,kind,target,replacement,status FROM source_corrections prior WHERE prior.artifact_id=sqlc.arg(previous_artifact) AND prior.version=sqlc.arg(previous_version) AND prior.location=sqlc.arg(previous_location) ON CONFLICT DO NOTHING;

-- name: ProcessingComplete :one
SELECT EXISTS(SELECT 1 FROM processing_receipts receipt WHERE receipt.artifact_id=artifacts.id AND receipt.version=sqlc.arg(version)) AS complete FROM artifacts WHERE artifacts.id=sqlc.arg(artifact_id);

-- name: RecentAcquisitionReceipts :many
SELECT * FROM acquisition_receipts WHERE artifact_id=? ORDER BY rowid DESC LIMIT 50;
-- name: CountAcquisitionReceipts :one
SELECT count(*) FROM acquisition_receipts WHERE artifact_id=?;

-- name: SaveProcessingReceipt :exec
INSERT INTO processing_receipts(artifact_id,version) VALUES(?,?) ON CONFLICT DO NOTHING;

-- name: SeedArtifactBases :exec
INSERT INTO activity_bases(activity_id,data,instrument_id,listing_id) SELECT e.id,e.accepted,e.instrument_id,e.listing_id FROM executions e WHERE e.id IN (SELECT execution_id FROM execution_observations WHERE artifact_id=?) ON CONFLICT DO NOTHING;
-- name: NewClaimActivityIDs :many
SELECT DISTINCT o.execution_id FROM execution_observations o JOIN records r USING(artifact_id,version,location) JOIN claim_processing p ON p.claim_id=r.claim_id AND p.ordinal=r.ordinal AND p.artifact_id=r.artifact_id AND p.version=r.version AND p.location=r.location WHERE o.artifact_id=? AND o.execution_id IS NOT NULL ORDER BY o.execution_id;

-- name: CalculationInputs :many
SELECT record_values.*,o.outcome AS link_outcome FROM execution_observations o JOIN record_values USING(artifact_id,version,location) JOIN claim_processing p ON p.claim_id=record_values.claim_id AND p.ordinal=record_values.ordinal AND p.artifact_id=o.artifact_id AND p.version=o.version AND p.location=o.location WHERE o.execution_id=? AND record_values.kind='activity' ORDER BY p.rowid;
-- name: SelectedObservation :one
SELECT record_values.* FROM record_values JOIN execution_observations o USING(artifact_id,version,location) WHERE o.execution_id=? AND o.artifact_id=? AND o.version=? AND o.location=?;


-- name: ArtifactInstrumentClaims :many
SELECT r.kind,r.data,r.location,r.claim_id,r.ordinal,b.instrument_id FROM execution_observations o JOIN record_values r USING(artifact_id,version,location) JOIN claim_processing p ON p.claim_id=r.claim_id AND p.ordinal=r.ordinal AND p.artifact_id=r.artifact_id AND p.version=r.version AND p.location=r.location JOIN activity_bases b ON b.activity_id=o.execution_id WHERE o.execution_id IN (SELECT affected.execution_id FROM execution_observations affected WHERE affected.artifact_id=?) AND r.kind='activity' ORDER BY o.rowid;
-- name: PutInstrumentClaim :exec
INSERT INTO instrument_claims(instrument_id,claim_id,ordinal,role) VALUES(?,?,?,?) ON CONFLICT DO NOTHING;
-- name: ArtifactRootIDs :many
WITH RECURSIVE affected(instrument_id) AS (
 SELECT i.instrument_id FROM records r JOIN source_claims c ON c.id=r.claim_id JOIN claim_processing p ON p.claim_id=r.claim_id AND p.ordinal=r.ordinal JOIN instrument_claims i ON i.claim_id=r.claim_id AND i.ordinal=r.ordinal WHERE r.artifact_id=sqlc.arg(artifact_id) AND (c.kind='position-instrument' OR (p.artifact_id=r.artifact_id AND p.version=r.version AND p.location=r.location))
 UNION
 SELECT b.instrument_id FROM execution_observations o JOIN activity_bases b ON b.activity_id=o.execution_id WHERE o.artifact_id=sqlc.arg(artifact_id) AND b.instrument_id IS NOT NULL
 UNION
 SELECT coalesce(a.target_id,b.instrument_id) FROM position_bindings b JOIN records r ON r.claim_id=b.claim_id AND r.ordinal=b.ordinal LEFT JOIN resource_aliases a ON a.kind='instruments' AND a.id=b.instrument_id WHERE r.artifact_id=sqlc.arg(artifact_id)
 UNION
 SELECT u.underlier_id FROM position_bindings b JOIN records r ON r.claim_id=b.claim_id AND r.ordinal=b.ordinal LEFT JOIN resource_aliases a ON a.kind='instruments' AND a.id=b.instrument_id JOIN instrument_underliers u ON u.parent_id=coalesce(a.target_id,b.instrument_id) WHERE r.artifact_id=sqlc.arg(artifact_id)
 UNION
 SELECT parent.parent_id FROM affected JOIN instrument_underliers parent ON parent.underlier_id=affected.instrument_id
)
SELECT DISTINCT coalesce((SELECT target_id FROM resource_aliases a WHERE a.kind='instruments' AND a.id=affected.instrument_id),instrument_id) AS instrument_id FROM affected ORDER BY instrument_id;
-- name: RootCalculationInputs :many
SELECT c.id AS claim_id,i.ordinal,c.kind,c.data,p.location,i.role,p.sequence AS sequence FROM instrument_claims i JOIN source_claims c ON c.id=i.claim_id JOIN claim_processing p ON p.claim_id=i.claim_id AND p.ordinal=i.ordinal WHERE i.instrument_id=sqlc.arg(id) AND c.kind<>'position-instrument'
UNION ALL
SELECT i.claim_id,i.ordinal,i.kind,i.data,i.location,'subject' AS role,p.sequence AS sequence FROM current_position_instruments i JOIN claim_processing p ON p.claim_id=i.claim_id AND p.ordinal=i.ordinal WHERE i.instrument_id=sqlc.arg(id)
ORDER BY sequence,role;
-- name: InstrumentContributionInputs :many
SELECT c.id AS claim_id,i.ordinal,c.kind,c.data,p.location,i.role FROM instrument_claims i JOIN source_claims c ON c.id=i.claim_id JOIN claim_processing p ON p.claim_id=i.claim_id AND p.ordinal=i.ordinal WHERE i.instrument_id=? ORDER BY p.rowid,i.role;
-- name: PositionUnderlierInputs :many
SELECT p.*,CAST(coalesce(s.underlier_id,(SELECT CASE WHEN count(*)=1 THEN min(underlier_id) END FROM instrument_underliers WHERE parent_id=p.instrument_id),'') AS TEXT) AS underlier_id,processing.sequence AS sequence FROM current_position_instruments p JOIN instrument_underliers u ON u.parent_id=p.instrument_id JOIN claim_processing processing ON processing.claim_id=p.claim_id AND processing.ordinal=p.ordinal LEFT JOIN instrument_selections s ON s.instrument_id=p.instrument_id WHERE u.underlier_id=? ORDER BY processing.rowid;

-- name: ClaimRootIDs :many
WITH RECURSIVE affected(instrument_id) AS (
 SELECT selected.instrument_id FROM instrument_claims selected WHERE selected.claim_id=sqlc.arg(claim_id) AND selected.ordinal=sqlc.arg(ordinal)
 UNION
 SELECT parent.parent_id FROM affected JOIN instrument_underliers parent ON parent.underlier_id=affected.instrument_id
)
SELECT DISTINCT coalesce((SELECT target_id FROM resource_aliases a WHERE a.kind='instruments' AND a.id=affected.instrument_id),instrument_id) AS instrument_id FROM affected ORDER BY instrument_id;

-- name: ResourceAlias :one
SELECT target_id FROM resource_aliases WHERE kind=? AND id=?;
-- name: PutResourceAlias :exec
INSERT INTO resource_aliases(kind,id,target_id) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET target_id=excluded.target_id;
-- name: FlattenAliases :exec
UPDATE resource_aliases SET target_id=sqlc.arg(target) WHERE kind=sqlc.arg(kind) AND target_id=sqlc.arg(previous);
-- name: IdentityMetadata :exec
UPDATE instruments SET reference_allocation=sqlc.arg(allocation),reference_reason=sqlc.arg(reason),kind=sqlc.arg(family) WHERE id=sqlc.arg(id) AND (reference_allocation<>sqlc.arg(allocation) OR reference_reason<>sqlc.arg(reason) OR kind<>sqlc.arg(family));
-- name: IdentityRoots :many
SELECT i.* FROM instruments i JOIN (SELECT original.id FROM instruments original WHERE original.reference_figi=sqlc.arg(figi) UNION SELECT selected.instrument_id AS id FROM instrument_selections selected WHERE selected.figi=sqlc.arg(figi)) roots ON roots.id=i.id WHERE i.id NOT IN (SELECT id FROM resource_aliases WHERE kind='instruments') ORDER BY i.rowid;
-- name: MoveInstrumentClaims :exec
INSERT INTO instrument_claims(instrument_id,claim_id,ordinal,role,outcome,rule) SELECT sqlc.arg(target),c.claim_id,c.ordinal,c.role,c.outcome,c.rule FROM instrument_claims c WHERE c.instrument_id=sqlc.arg(previous) ON CONFLICT DO NOTHING;
-- name: MoveReferenceAttempts :exec
INSERT INTO instrument_reference_attempts(instrument_id,evidence_id,status,rule) SELECT sqlc.arg(target),a.evidence_id,a.status,a.rule FROM instrument_reference_attempts a WHERE a.instrument_id=sqlc.arg(previous) ON CONFLICT DO NOTHING;
-- name: MoveActivityInstrument :exec
UPDATE executions SET instrument_id=sqlc.arg(target) WHERE instrument_id=sqlc.arg(previous);
-- name: MoveBaseInstrument :exec
UPDATE activity_bases SET instrument_id=sqlc.arg(target) WHERE instrument_id=sqlc.arg(previous);
-- name: MoveUnderliers :exec
UPDATE instruments SET underlier_id=sqlc.arg(target) WHERE underlier_id=sqlc.arg(previous);
-- name: MoveSelectedUnderliers :exec
UPDATE instrument_selections SET underlier_id=sqlc.arg(target) WHERE underlier_id=sqlc.arg(previous);
-- name: MoveListing :exec
UPDATE listings SET instrument_id=? WHERE id=?;
-- name: MoveActivityListing :exec
UPDATE executions SET listing_id=sqlc.arg(target) WHERE listing_id=sqlc.arg(previous);
-- name: MoveBaseListing :exec
UPDATE activity_bases SET listing_id=sqlc.arg(target) WHERE listing_id=sqlc.arg(previous);
-- name: RetireListing :exec
DELETE FROM listings WHERE id=?;
-- name: InstrumentBindings :many
SELECT i.* FROM instruments i WHERE i.id=sqlc.arg(id) OR i.id IN (SELECT a.id FROM resource_aliases a WHERE a.kind='instruments' AND a.target_id=sqlc.arg(id)) ORDER BY i.rowid;

-- name: SupersedeDecisions :exec
INSERT INTO decision_supersessions(decision_id,previous_id)
SELECT sqlc.arg(decision_id), d.id FROM operator_decisions d
WHERE d.id<>sqlc.arg(decision_id) AND d.target_kind=sqlc.arg(target_kind) AND d.scope=sqlc.arg(scope)
AND (d.target_id=sqlc.arg(target_id) OR (d.target_kind='instrument' AND d.target_id IN (SELECT a.id FROM resource_aliases a WHERE a.kind='instruments' AND a.target_id=sqlc.arg(target_id))))
AND NOT EXISTS(SELECT 1 FROM decision_supersessions x WHERE x.previous_id=d.id);

-- name: ListingsByTerms :many
SELECT * FROM listings WHERE instrument_id=? AND venue=? AND ticker=? AND currency=? ORDER BY id;

-- name: IdentityDependents :many
WITH bindings(id) AS (SELECT CAST(sqlc.arg(id) AS TEXT) AS id UNION SELECT alias.id FROM resource_aliases alias WHERE alias.kind='instruments' AND alias.target_id=sqlc.arg(id)), affected(id) AS (
 SELECT CAST(sqlc.arg(id) AS TEXT) AS id UNION SELECT p.parent_id FROM instrument_underliers p WHERE p.underlier_id IN (SELECT id FROM bindings)
)
SELECT DISTINCT CAST(coalesce(a.target_id,affected.id) AS TEXT) AS id FROM affected LEFT JOIN resource_aliases a ON a.kind='instruments' AND a.id=affected.id ORDER BY id;

-- name: DisableAcquisition :exec
UPDATE scheduled SET enabled=0 WHERE connection_id=? AND operation='acquire';
-- name: ResetStreamWork :exec
UPDATE work SET revision=revision+1,state_version='',state=X'',next=CASE WHEN (SELECT paused FROM profile WHERE singleton=1)=1 THEN next ELSE '' END,failure='',failure_code='',observed='',acknowledged='' WHERE connection_id=? AND stream=?;
-- name: PutActivityReference :exec
INSERT INTO activity_references(claim_id,ordinal,account_id,reference) VALUES(?,?,?,?) ON CONFLICT DO NOTHING;
-- name: ActivityToMatch :many
-- Traverse claim and qualified-reference nodes separately. UNION expands each
-- shared reference once, rather than revisiting its entire claim set per claim.
WITH RECURSIVE seeds(node,claim_id,ordinal,account_id,reference) AS (
 SELECT 'claim' AS node,r.claim_id,r.ordinal,CAST(NULL AS TEXT) AS account_id,CAST(NULL AS TEXT) AS reference FROM records occurrence JOIN activity_references r ON r.claim_id=occurrence.claim_id AND r.ordinal=occurrence.ordinal WHERE occurrence.artifact_id=sqlc.arg(artifact_id)
 UNION
 SELECT 'reference',NULL,NULL,e.account_id,e.reference FROM execution_observations o JOIN event_links e ON e.execution_id=o.execution_id WHERE o.artifact_id=sqlc.arg(artifact_id)
), affected(node,claim_id,ordinal,account_id,reference) AS (
 SELECT node,claim_id,ordinal,account_id,reference FROM seeds
 UNION
 SELECT 'reference',NULL,NULL,r.account_id,r.reference FROM affected a JOIN activity_references r ON r.claim_id=a.claim_id AND r.ordinal=a.ordinal WHERE a.node='claim'
 UNION
 SELECT 'claim',r.claim_id,r.ordinal,NULL,NULL FROM affected a JOIN activity_references r ON r.account_id=a.account_id AND r.reference=a.reference WHERE a.node='reference'
)
SELECT p.artifact_id,p.version,p.location,p.claim_id,p.ordinal,c.kind,c.data FROM affected a JOIN claim_processing p ON p.claim_id=a.claim_id AND p.ordinal=a.ordinal JOIN source_claims c ON c.id=p.claim_id WHERE a.node='claim' AND p.activity_support=1 ORDER BY p.rowid;
-- name: RelinkActivityObservation :exec
UPDATE execution_observations SET execution_id=sqlc.narg(execution_id),outcome=sqlc.arg(outcome) WHERE (artifact_id,version,location) IN (SELECT artifact_id,version,location FROM records WHERE claim_id=sqlc.arg(claim_id) AND ordinal=sqlc.arg(ordinal));
-- name: ActivityObservation :one
SELECT * FROM execution_observations WHERE artifact_id=? AND version=? AND location=?;
-- name: ActivitySelection :one
SELECT * FROM activity_selections WHERE activity_id=?;
-- name: SaveActivitySelection :exec
INSERT INTO activity_selections(activity_id,baseline,selected) VALUES(?,?,?) ON CONFLICT(activity_id) DO UPDATE SET baseline=excluded.baseline,selected=excluded.selected WHERE baseline<>excluded.baseline OR selected<>excluded.selected;

-- name: PutActivityBase :exec
INSERT INTO activity_bases(activity_id,data,instrument_id,listing_id) VALUES(?,?,?,?) ON CONFLICT DO NOTHING;

-- name: BindActivityInstrument :exec
UPDATE activity_bases SET instrument_id=sqlc.arg(instrument_id) WHERE activity_id=sqlc.arg(activity_id) AND instrument_id IS NULL;

-- name: PutMovementActivity :exec
INSERT INTO executions(kind,id,account_id,instrument_id,accepted,status,rule) VALUES(?,?,?,?,?,'accepted',?);

-- name: QueueInterpretations :exec
UPDATE artifacts SET processed=0
WHERE processed=1 AND EXISTS(SELECT 1 FROM acquisition_receipts r WHERE r.artifact_id=artifacts.id AND r.operation='acquire')
AND EXISTS(SELECT 1 FROM accounts a JOIN connections c ON c.id=a.connection_id JOIN json_each(CAST(? AS TEXT)) v ON v.key=c.provider WHERE a.connection_id=artifacts.connection_id AND a.provider=c.provider AND NOT EXISTS(SELECT 1 FROM processing_receipts p WHERE p.artifact_id=artifacts.id AND p.version=v.value));

-- name: PutPositionSet :exec
INSERT INTO position_sets(claim_id,account_id,effective_date,source_reference,supersedes) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING;
-- name: PutPositionBinding :exec
INSERT INTO position_bindings(claim_id,ordinal,account_id,instrument_id) VALUES(?,?,?,?) ON CONFLICT DO NOTHING;
-- name: PositionBindings :many
SELECT c.data,i.id AS instrument_id,i.kind,i.reference_status FROM position_bindings p JOIN source_claims c ON c.id=p.claim_id LEFT JOIN resource_aliases a ON a.kind='instruments' AND a.id=p.instrument_id JOIN instruments i ON i.id=coalesce(a.target_id,p.instrument_id) WHERE p.account_id=? AND EXISTS(SELECT 1 FROM current_position_claims active WHERE active.claim_id=p.claim_id AND active.ordinal=p.ordinal) ORDER BY p.rowid;
-- name: DeleteObservation :exec
DELETE FROM execution_observations WHERE artifact_id=? AND version=? AND location=?;
-- name: DeleteInstrumentClaim :exec
DELETE FROM instrument_claims WHERE instrument_id=? AND claim_id=? AND ordinal=? AND role=?;
-- name: SetInstrumentClaimOutcome :exec
UPDATE instrument_claims SET outcome=? WHERE instrument_id=? AND claim_id=? AND ordinal=? AND role=?;
-- name: DeleteSubjectInstrumentClaims :exec
DELETE FROM instrument_claims WHERE instrument_id=?;
-- name: PutInstrumentUnderlier :exec
INSERT INTO instrument_underliers(parent_id,underlier_id) VALUES(?,?) ON CONFLICT DO NOTHING;
-- name: MoveInstrumentUnderlierLinks :exec
INSERT INTO instrument_underliers(parent_id,underlier_id)
SELECT CASE WHEN links.parent_id=sqlc.arg(previous) THEN sqlc.arg(target) ELSE links.parent_id END AS parent_id,
 CASE WHEN links.underlier_id=sqlc.arg(previous) THEN sqlc.arg(target) ELSE links.underlier_id END AS underlier_id
FROM instrument_underliers links WHERE (links.parent_id=sqlc.arg(previous) OR links.underlier_id=sqlc.arg(previous))
 AND CASE WHEN links.parent_id=sqlc.arg(previous) THEN sqlc.arg(target) ELSE links.parent_id END <> CASE WHEN links.underlier_id=sqlc.arg(previous) THEN sqlc.arg(target) ELSE links.underlier_id END
ON CONFLICT DO NOTHING;
-- name: DeleteInstrumentUnderlierLinks :exec
DELETE FROM instrument_underliers WHERE parent_id=sqlc.arg(id) OR underlier_id=sqlc.arg(id);

-- name: DiscardActivitySupport :exec
UPDATE claim_processing SET activity_support=0 WHERE claim_id=? AND ordinal=?;
