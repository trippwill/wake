-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- name: CountTables :one
SELECT count(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%';
-- name: PutConnection :exec
INSERT INTO sync.configurations(id,provider,name,settings) VALUES(?,?,?,?);
-- name: Connection :one
SELECT c.*, s.revision AS access_revision FROM sync.configurations c CROSS JOIN sync.sync_association s WHERE c.id=?;
-- name: BindCredentialIdentity :execrows
INSERT INTO access.identities(id,provider) VALUES(?,?)
ON CONFLICT(id) DO UPDATE SET provider=excluded.provider WHERE identities.provider=excluded.provider;
-- name: PutSecret :exec
INSERT INTO access.credentials(connection_id,key,encrypted) VALUES(?,?,?);
-- name: Secret :one
SELECT encrypted FROM access.credentials WHERE connection_id=? AND key=?;
-- name: EnsureWork :exec
INSERT INTO sync.work(connection_id,stream,operation) VALUES(?,?,?) ON CONFLICT DO NOTHING;
-- name: EnsureCheckpointWork :execrows
INSERT INTO sync.work(connection_id,stream,operation) VALUES(?,?,?) ON CONFLICT DO NOTHING;
-- name: Checkpoint :one
SELECT * FROM sync.work WHERE connection_id=? AND stream=? AND operation=?;
-- name: ObserveWork :exec
INSERT INTO sync.work(connection_id,stream,operation,observed,observed_completed,observed_total)
SELECT prior.connection_id,prior.stream,prior.operation,sqlc.arg(observed),sqlc.arg(observed_completed),sqlc.arg(observed_total) FROM sync.work AS prior
WHERE prior.connection_id=sqlc.arg(connection_id) AND prior.stream=sqlc.arg(stream) AND prior.operation=sqlc.arg(operation)
ON CONFLICT(connection_id,stream,operation) DO UPDATE SET observed=excluded.observed, observed_completed=excluded.observed_completed, observed_total=excluded.observed_total;
-- name: FailWork :exec
INSERT INTO sync.work(connection_id,stream,operation,failure,failure_code)
SELECT prior.connection_id,prior.stream,prior.operation,sqlc.arg(failure),sqlc.arg(failure_code) FROM sync.work AS prior
WHERE prior.connection_id=sqlc.arg(connection_id) AND prior.stream=sqlc.arg(stream) AND prior.operation=sqlc.arg(operation)
ON CONFLICT(connection_id,stream,operation) DO UPDATE SET failure=excluded.failure, failure_code=excluded.failure_code;
-- name: AdvanceWork :execrows
INSERT INTO sync.work(connection_id,stream,operation,revision,state_version,state,next,acknowledged,acknowledged_completed,acknowledged_total,failure,failure_code)
SELECT prior.connection_id,prior.stream,prior.operation,prior.revision+1,sqlc.arg(state_version),sqlc.arg(state),sqlc.arg(next),sqlc.arg(acknowledged),sqlc.arg(acknowledged_completed),sqlc.arg(acknowledged_total),sqlc.arg(failure),sqlc.arg(failure_code) FROM sync.work AS prior
WHERE prior.connection_id=sqlc.arg(connection_id) AND prior.stream=sqlc.arg(stream) AND prior.operation=sqlc.arg(operation) AND prior.revision=sqlc.arg(revision)
ON CONFLICT(connection_id,stream,operation) DO UPDATE SET revision=excluded.revision, state_version=excluded.state_version, state=excluded.state, next=excluded.next, acknowledged=excluded.acknowledged, acknowledged_completed=excluded.acknowledged_completed, acknowledged_total=excluded.acknowledged_total, failure=excluded.failure, failure_code=excluded.failure_code;
-- name: PutArtifact :exec
INSERT INTO artifacts(id,connection_id,stream,operation,format,media_type,retrieved,hash,content,requested_from,requested_to,reported_from,reported_to) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?);
-- name: Retained :one
SELECT artifacts.*,connections.provider FROM artifacts JOIN connections ON connections.id=artifacts.connection_id WHERE artifacts.id=?;
-- name: PutCandidate :exec
INSERT INTO candidates(receipt_id,artifact_id,authority,kind,scope,value,name,currency) VALUES(?,?,?,?,?,?,?,?);
-- name: Candidates :many
SELECT DISTINCT candidates.artifact_id,candidates.authority,candidates.kind,candidates.scope,candidates.value,candidates.name,candidates.currency FROM candidates JOIN artifacts ON artifacts.id=candidates.artifact_id WHERE artifacts.connection_id=? ORDER BY candidates.artifact_id,candidates.authority,candidates.kind,candidates.scope,candidates.value;
-- name: StartAttempt :exec
INSERT INTO sync.attempts(id,artifact_id,version,outcome,counts) VALUES(?,?,?,'running',X'');
-- name: FinishAttempt :exec
INSERT INTO sync.attempts(id,artifact_id,version,outcome,counts) SELECT a.id,a.artifact_id,a.version,?,? FROM sync.attempts a WHERE a.id=? ON CONFLICT(id) DO UPDATE SET outcome=excluded.outcome,counts=excluded.counts;
-- name: Succeeded :one
SELECT counts FROM normalization_receipts WHERE artifact_id=? AND version=?;
-- name: Attempts :many
SELECT * FROM sync.attempts WHERE artifact_id=? ORDER BY rowid;
-- name: PutRecord :exec
INSERT INTO records(artifact_id,version,location,claim_id,ordinal) VALUES(?,?,?,?,?);
-- name: Records :many
SELECT * FROM record_values WHERE artifact_id=? AND version=? ORDER BY location;
-- name: Pacing :one
SELECT next FROM sync.pacing WHERE provider=? AND policy=? AND scope=?;
-- name: SetPacing :exec
INSERT INTO sync.pacing(provider,policy,scope,next) VALUES(?,?,?,?) ON CONFLICT(provider,policy,scope) DO UPDATE SET next=excluded.next;

-- name: ReplaceSecret :exec
INSERT INTO access.credentials(connection_id,key,encrypted) VALUES(?,?,?) ON CONFLICT(connection_id,key) DO UPDATE SET encrypted=excluded.encrypted;
-- name: Connections :many
SELECT id FROM connections ORDER BY rowid;
-- name: SetScheduled :exec
INSERT INTO sync.scheduled(connection_id,stream,operation,enabled) VALUES(?,?,?,?) ON CONFLICT(connection_id,stream,operation) DO UPDATE SET enabled=excluded.enabled;
-- name: InvalidateChangedSchedule :exec
INSERT INTO sync.work(connection_id,stream,operation,revision)
SELECT prior.connection_id,prior.stream,prior.operation,prior.revision+1 FROM sync.work AS prior
WHERE prior.connection_id=sqlc.arg(connection_id) AND prior.stream=sqlc.arg(stream) AND prior.operation=sqlc.arg(operation)
AND EXISTS(SELECT 1 FROM sync.scheduled WHERE scheduled.connection_id=prior.connection_id
 AND scheduled.stream=prior.stream AND scheduled.operation=prior.operation AND enabled<>sqlc.arg(enabled))
ON CONFLICT(connection_id,stream,operation) DO UPDATE SET revision=excluded.revision;
-- name: EnsureAccountAcquisitionWork :execrows
INSERT INTO sync.work(connection_id,stream,operation)
SELECT connection_id,sqlc.arg(stream),'acquire' FROM accounts WHERE provider=sqlc.arg(provider)
ON CONFLICT DO NOTHING;
-- name: ScheduleMissingAccountAcquisition :execrows
INSERT INTO sync.scheduled(connection_id,stream,operation,enabled)
SELECT connection_id,sqlc.arg(stream),'acquire',1 FROM accounts WHERE provider=sqlc.arg(provider)
ON CONFLICT DO NOTHING;
-- name: RetryWork :exec
INSERT INTO sync.work(connection_id,stream,operation,revision,failure,failure_code,next)
SELECT prior.connection_id,prior.stream,prior.operation,prior.revision+1,'','',CASE WHEN (SELECT paused FROM sync.profile WHERE singleton=1)=1 THEN prior.next ELSE '' END FROM sync.work AS prior
WHERE prior.connection_id=sqlc.arg(connection_id) AND prior.stream=sqlc.arg(stream) AND prior.operation=sqlc.arg(operation)
ON CONFLICT(connection_id,stream,operation) DO UPDATE SET revision=excluded.revision, failure=excluded.failure, failure_code=excluded.failure_code, next=excluded.next;
-- name: RestartDiscovery :exec
INSERT INTO sync.work(connection_id,stream,operation,revision,state_version,state,failure,failure_code,next)
SELECT prior.connection_id,prior.stream,prior.operation,prior.revision+1,CASE WHEN (SELECT paused FROM sync.profile WHERE singleton=1)=1 THEN prior.state_version ELSE '' END,CASE WHEN (SELECT paused FROM sync.profile WHERE singleton=1)=1 THEN prior.state ELSE X'' END,'','',CASE WHEN (SELECT paused FROM sync.profile WHERE singleton=1)=1 THEN prior.next ELSE '' END FROM sync.work AS prior
WHERE prior.connection_id=sqlc.arg(connection_id) AND prior.stream=sqlc.arg(stream) AND prior.operation='discover'
ON CONFLICT(connection_id,stream,operation) DO UPDATE SET revision=excluded.revision, state_version=excluded.state_version, state=excluded.state, failure=excluded.failure, failure_code=excluded.failure_code, next=excluded.next;
-- name: RefreshPolls :execrows
INSERT INTO sync.work(connection_id,stream,operation,next)
SELECT prior.connection_id,prior.stream,prior.operation,CASE WHEN json_extract(CAST(prior.state AS TEXT), '$.not_before')>CAST(sqlc.arg(now) AS TEXT)
              THEN json_extract(CAST(prior.state AS TEXT), '$.not_before') ELSE CAST(sqlc.arg(now) AS TEXT) END FROM sync.work AS prior
WHERE prior.operation='acquire'
  AND (SELECT paused FROM sync.profile WHERE singleton=1)=0
  AND prior.failure='' AND prior.failure_code=''
  AND julianday(prior.next)>julianday(sqlc.arg(now))
  AND julianday(prior.next)>CASE WHEN prior.state_version=sqlc.arg(poll_version)
                           THEN julianday(json_extract(CAST(prior.state AS TEXT), '$.not_before')) END
  AND EXISTS(SELECT 1 FROM sync.scheduled WHERE scheduled.connection_id=prior.connection_id
             AND scheduled.stream=prior.stream AND scheduled.operation=prior.operation AND enabled=1)
  AND EXISTS(SELECT 1 FROM accounts WHERE accounts.connection_id=prior.connection_id)
ON CONFLICT(connection_id,stream,operation) DO UPDATE SET next=excluded.next;
-- name: QueuedPoll :one
SELECT work.state FROM sync.work JOIN sync.scheduled USING(connection_id,stream,operation)
WHERE work.connection_id=? AND work.stream=? AND work.operation='acquire' AND work.state_version=?
  AND enabled=1 AND work.next<>'' AND work.failure='' AND work.failure_code=''
  AND EXISTS(SELECT 1 FROM accounts WHERE accounts.connection_id=work.connection_id);
-- name: SetPollDeadline :exec
INSERT INTO sync.work(connection_id,stream,operation,next,state)
SELECT prior.connection_id,prior.stream,prior.operation,sqlc.arg(next),sqlc.arg(state) FROM sync.work AS prior
WHERE prior.connection_id=sqlc.arg(connection_id) AND prior.stream=sqlc.arg(stream) AND prior.operation='acquire'
ON CONFLICT(connection_id,stream,operation) DO UPDATE SET next=excluded.next, state=excluded.state;
-- name: QueuedPollConnections :many
SELECT id,provider,settings FROM sync.configurations
WHERE EXISTS(SELECT 1 FROM sync.work JOIN sync.scheduled USING(connection_id,stream,operation)
   WHERE work.connection_id=configurations.id AND work.operation='acquire' AND work.state_version=?
    AND enabled=1 AND work.next<>'' AND work.failure='' AND work.failure_code='')
  AND EXISTS(SELECT 1 FROM accounts WHERE accounts.connection_id=configurations.id);
-- name: PendingWork :many
SELECT work.connection_id,work.stream,work.operation
FROM sync.work JOIN sync.scheduled USING(connection_id,stream,operation)
WHERE (SELECT paused FROM sync.profile WHERE singleton=1)=0
 AND enabled=1 AND (next='' OR julianday(next)<=julianday(sqlc.arg(now)))
 AND failure_code NOT IN ('unauthorized','invalid','unavailable','malformed','limit')
 AND (NOT EXISTS(SELECT 1 FROM sync.recovery_sources AS recovery WHERE recovery.connection_id=work.connection_id)
   OR (work.operation='acquire' AND EXISTS(
      SELECT 1 FROM sync.restart_scopes r
      JOIN sync.configurations c ON c.id=r.connection_id AND c.settings=r.settings
      JOIN accounts a ON a.connection_id=r.connection_id AND a.id=r.account_id AND a.revision=r.account_revision
      WHERE r.connection_id=work.connection_id AND r.stream=work.stream
        AND julianday(r.not_before)<=julianday(sqlc.arg(now)))))
ORDER BY work.rowid LIMIT 20;
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
FROM sync.work LEFT JOIN sync.scheduled USING(connection_id,stream,operation) WHERE work.connection_id=? ORDER BY work.operation,work.stream;
-- name: DisableDiscovery :exec
INSERT INTO sync.scheduled(connection_id,stream,operation,enabled)
SELECT prior.connection_id,prior.stream,prior.operation,0 FROM sync.scheduled AS prior
WHERE prior.connection_id=sqlc.arg(connection_id) AND prior.operation='discover'
ON CONFLICT(connection_id,stream,operation) DO UPDATE SET enabled=excluded.enabled;
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
SELECT artifacts.id,connections.provider
FROM artifacts
JOIN connections ON connections.id=artifacts.connection_id
JOIN accounts ON accounts.connection_id=artifacts.connection_id AND accounts.provider=connections.provider
JOIN json_each(CAST(sqlc.arg(versions) AS TEXT)) version ON version.key=connections.provider
WHERE EXISTS(SELECT 1 FROM acquisition_receipts receipt WHERE receipt.artifact_id=artifacts.id AND receipt.operation IN ('acquire','supply'))
AND NOT EXISTS(SELECT 1 FROM processing_receipts receipt WHERE receipt.artifact_id=artifacts.id AND receipt.version=version.value)
AND NOT EXISTS(SELECT 1 FROM json_each(CAST(sqlc.arg(deferred) AS TEXT)) retry WHERE retry.value=artifacts.id)
ORDER BY artifacts.rowid LIMIT 20;
-- name: ProcessingStatuses :many
SELECT artifacts.id,artifacts.stream,receipt.id AS receipt_id,receipt.retrieved,receipt.hash,receipt.policy,CAST(EXISTS(SELECT 1 FROM processing_receipts p WHERE p.artifact_id=artifacts.id
 AND p.version=sqlc.arg(version)) AS INTEGER) AS processed,receipt.requested_from,receipt.requested_to,receipt.reported_from,receipt.reported_to,
 CAST(coalesce(json_extract(n.counts,'$.Activities'),0) AS INTEGER) AS activity_claims,
 CAST(coalesce(json_extract(n.counts,'$.Rejected'),0) AS INTEGER) AS excluded_rows,
 (SELECT count(*) FROM execution_observations o WHERE o.artifact_id=artifacts.id AND o.version=sqlc.arg(version) AND outcome IN ('identity-review','native-review','incomplete','incomplete-conflict')) AS needs_review
FROM acquisition_receipts receipt JOIN artifacts ON artifacts.id=receipt.artifact_id
LEFT JOIN normalization_receipts n ON n.artifact_id=artifacts.id AND n.version=sqlc.arg(version)
WHERE artifacts.connection_id=sqlc.arg(connection_id) AND receipt.operation IN ('acquire','supply') ORDER BY receipt.rowid DESC LIMIT 10;
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
INSERT INTO instruments(id,source_key,name,ticker,kind,reported,rule,reference_status,calculation_rule) VALUES(?,?,?,?,?,?,?,'provisional',?);
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
SELECT CAST(coalesce((SELECT encrypted FROM access.reference_settings WHERE singleton=1),X'') AS BLOB) AS encrypted,
 CAST(coalesce((SELECT failure FROM sync.reference_diagnostics WHERE singleton=1),'') AS TEXT) AS failure;
-- name: SetReferenceKey :exec
INSERT INTO access.reference_settings(singleton,encrypted) VALUES(1,?) ON CONFLICT(singleton) DO UPDATE SET encrypted=excluded.encrypted;
-- name: ReferenceFailure :exec
INSERT INTO sync.reference_diagnostics(singleton,failure) VALUES(1,?) ON CONFLICT(singleton) DO UPDATE SET failure=excluded.failure;
-- name: ReferenceBudget :one
SELECT * FROM sync.reference_budget WHERE singleton=1;
-- name: SetReferenceBudget :exec
INSERT INTO sync.reference_budget(singleton,window_start,used,cooldown) VALUES(1,?,?,?) ON CONFLICT(singleton) DO UPDATE SET window_start=excluded.window_start,used=excluded.used,cooldown=excluded.cooldown;
-- name: PutReferenceEvidence :exec
INSERT INTO reference_evidence(id,retrieved,request,response,http_status,headers) VALUES(?,?,?,?,?,?);
-- name: ReferenceEvidence :one
SELECT * FROM reference_evidence WHERE id=?;
-- name: CachedReference :one
SELECT * FROM sync.reference_cache WHERE key=?;
-- name: CacheReference :exec
INSERT INTO sync.reference_cache(key,status,records,expires,evidence_id) VALUES(?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET status=excluded.status,records=excluded.records,expires=excluded.expires,evidence_id=excluded.evidence_id;
-- name: PendingReference :many
SELECT instruments.* FROM instruments LEFT JOIN sync.reference_deadlines d ON d.instrument_id=instruments.id AND d.input_revision=instruments.reference_input_revision WHERE (SELECT paused FROM sync.profile WHERE singleton=1)=0 AND coalesce((SELECT reference_granted FROM sync.recovery_state WHERE singleton=1),1)=1 AND id NOT IN (SELECT id FROM resource_aliases WHERE kind='instruments') AND reference_reason<>'missing_identifier' AND reference_status<>'conflict' AND (d.next IS NULL OR julianday(d.next)<=julianday(?)) ORDER BY instruments.rowid LIMIT 100;
-- name: ResolveInstrument :exec
UPDATE instruments SET name=sqlc.arg(name),ticker=sqlc.arg(ticker),reference_figi=sqlc.arg(reference_figi),evidence_id=sqlc.arg(evidence_id),reference_status=sqlc.arg(reference_status),revision=revision+CASE WHEN name<>sqlc.arg(name) OR ticker<>sqlc.arg(ticker) OR reference_figi<>sqlc.arg(reference_figi) OR reference_status<>sqlc.arg(reference_status) THEN 1 ELSE 0 END WHERE id=sqlc.arg(id);
-- name: SetListingFIGI :exec
UPDATE listings SET figi=? WHERE id=?;
-- name: PutInstrumentReferenceAttempt :exec
INSERT INTO instrument_reference_attempts(instrument_id,evidence_id,status,rule) VALUES(?,?,?,?) ON CONFLICT DO NOTHING;
-- name: WakeReference :exec
DELETE FROM sync.reference_deadlines WHERE (SELECT paused FROM sync.profile WHERE singleton=1)=0 AND instrument_id IN (SELECT id FROM instruments WHERE reference_status<>'conflict');
-- name: InstrumentReferenceAttempts :many
SELECT instrument_reference_attempts.*,reference_evidence.retrieved FROM instrument_reference_attempts JOIN reference_evidence ON reference_evidence.id=evidence_id WHERE instrument_id=? ORDER BY reference_evidence.rowid DESC;
-- name: EventLinks :many
SELECT execution_id FROM event_links WHERE account_id=? AND reference=? ORDER BY execution_id;
-- name: ExecutionLinks :many
SELECT reference FROM event_links WHERE execution_id=? AND distinct_event=1;
-- name: UpdateConnection :exec
INSERT INTO sync.configurations(id,provider,name,settings) VALUES(sqlc.arg(id),sqlc.arg(provider),sqlc.arg(name),sqlc.arg(settings)) ON CONFLICT(id) DO UPDATE SET settings=excluded.settings,name=excluded.name WHERE configurations.provider=excluded.provider;

-- name: SetUnderlier :exec
UPDATE instruments SET underlier_id=sqlc.narg(underlier_id),revision=revision+1 WHERE id=sqlc.arg(id) AND underlier_id IS NOT sqlc.narg(underlier_id);

-- name: EnrichInstrument :exec
UPDATE instruments SET revision=revision+1,reference_input_revision=reference_input_revision+1,kind=sqlc.arg(kind),reported=sqlc.arg(reported),name=CASE WHEN name='' THEN sqlc.arg(name) ELSE name END,ticker=CASE WHEN ticker='' THEN sqlc.arg(ticker) ELSE ticker END,reference_reason=CASE WHEN reference_reason='missing_identifier' THEN '' ELSE reference_reason END WHERE id=sqlc.arg(id);

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
UPDATE executions SET accepted=sqlc.arg(accepted),status=sqlc.arg(status),review_reasons=sqlc.arg(review_reasons),selection_authority=sqlc.arg(selection_authority),instrument_id=sqlc.narg(instrument_id),listing_id=sqlc.narg(listing_id),calculation_rule=sqlc.arg(calculation_rule),revision=revision+1 WHERE id=sqlc.arg(id) AND (accepted<>sqlc.arg(accepted) OR status<>sqlc.arg(status) OR review_reasons<>sqlc.arg(review_reasons) OR selection_authority<>sqlc.arg(selection_authority) OR instrument_id IS NOT sqlc.narg(instrument_id) OR listing_id IS NOT sqlc.narg(listing_id) OR calculation_rule<>sqlc.arg(calculation_rule));
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
UPDATE instruments SET name=sqlc.arg(name),ticker=sqlc.arg(ticker),reference_figi=sqlc.arg(figi),evidence_id=sqlc.arg(evidence),reference_status=sqlc.arg(status),selection_authority=sqlc.arg(selection_authority),underlier_id=sqlc.narg(underlier_id),calculation_rule=sqlc.arg(calculation_rule),revision=revision+1 WHERE id=sqlc.arg(id) AND (name<>sqlc.arg(name) OR ticker<>sqlc.arg(ticker) OR reference_figi<>sqlc.arg(figi) OR reference_status<>sqlc.arg(status) OR selection_authority<>sqlc.arg(selection_authority) OR underlier_id IS NOT sqlc.narg(underlier_id) OR calculation_rule<>sqlc.arg(calculation_rule));
-- name: Listing :one
SELECT * FROM listings WHERE id=?;
-- name: RetainedReferenceCandidates :many
SELECT e.id,e.request,e.response FROM instrument_reference_attempts a JOIN reference_evidence e ON a.evidence_id=e.id WHERE a.instrument_id=? ORDER BY e.rowid;

-- name: ReferenceNext :exec
INSERT INTO sync.reference_deadlines(instrument_id,input_revision,next) VALUES(?,?,?) ON CONFLICT(instrument_id) DO UPDATE SET input_revision=excluded.input_revision,next=excluded.next;

-- name: EnsureInstrumentSelection :exec
INSERT INTO instrument_selections(instrument_id,name,ticker,figi,evidence,status,underlier_id) SELECT id,name,ticker,reference_figi,evidence_id,reference_status,underlier_id FROM instruments WHERE id=? ON CONFLICT DO NOTHING;

-- name: CanRetryProcessing :one
SELECT EXISTS(SELECT 1 FROM artifacts
 WHERE artifacts.id=?
 AND EXISTS(SELECT 1 FROM acquisition_receipts receipt WHERE receipt.artifact_id=artifacts.id AND receipt.operation IN ('acquire','supply'))
 AND EXISTS(SELECT 1 FROM accounts WHERE accounts.connection_id=artifacts.connection_id));
-- name: PutDescriptor :exec
INSERT INTO connections(id,provider,name) VALUES(?,?,?);
-- name: SaveNormalizationReceipt :exec
INSERT INTO normalization_receipts(artifact_id,version,counts) VALUES(?,?,?) ON CONFLICT DO NOTHING;
-- name: Profile :one
SELECT * FROM sync.profile WHERE singleton=1;
-- name: MissingAccess :many
SELECT c.id,c.provider FROM connections c LEFT JOIN sync.configurations a ON a.id=c.id WHERE a.id IS NULL ORDER BY c.rowid;
-- name: ReviewReference :exec
INSERT INTO sync.profile(singleton,access_id,paused,reference_review)
SELECT prior.singleton,prior.access_id,prior.paused,0 FROM sync.profile AS prior
WHERE prior.singleton=1
ON CONFLICT(singleton) DO UPDATE SET reference_review=excluded.reference_review;
-- name: ResumeStorage :execrows
INSERT INTO sync.profile(singleton,access_id,paused,reference_review,warehouse_reset,resumed_by,resumed_at)
SELECT prior.singleton,prior.access_id,0,prior.reference_review,0,sqlc.arg(resumed_by),sqlc.arg(resumed_at) FROM sync.profile AS prior
WHERE prior.singleton=1 AND prior.paused=1 AND prior.recovery_revision=sqlc.arg(recovery_revision) AND prior.reference_review=0 AND NOT EXISTS(SELECT 1 FROM connections c LEFT JOIN sync.configurations a ON a.id=c.id WHERE a.id IS NULL)
ON CONFLICT(singleton) DO UPDATE SET paused=excluded.paused, warehouse_reset=excluded.warehouse_reset, resumed_by=excluded.resumed_by, resumed_at=excluded.resumed_at;
-- name: Descriptor :one
SELECT * FROM connections WHERE id=?;
-- name: ConnectionAccess :one
SELECT count(*) FROM sync.configurations c WHERE c.id=?;

-- name: AccessReadiness :one
SELECT EXISTS(SELECT 1 FROM sync.configurations WHERE id=sqlc.arg(connection_id)) AS settings_stored, EXISTS(SELECT 1 FROM access.credentials WHERE connection_id=sqlc.arg(connection_id)) AS credential_stored;

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
INSERT INTO sync.scheduled(connection_id,stream,operation,enabled)
SELECT prior.connection_id,prior.stream,prior.operation,0 FROM sync.scheduled AS prior
WHERE prior.connection_id=sqlc.arg(connection_id) AND prior.operation='acquire'
ON CONFLICT(connection_id,stream,operation) DO UPDATE SET enabled=excluded.enabled;
-- name: ResetStreamWork :exec
INSERT INTO sync.work(connection_id,stream,operation,revision,state_version,state,next,failure,failure_code,observed,acknowledged)
SELECT prior.connection_id,prior.stream,prior.operation,prior.revision+1,'',X'',CASE WHEN (SELECT paused FROM sync.profile WHERE singleton=1)=1 THEN prior.next ELSE '' END,'','','','' FROM sync.work AS prior
WHERE prior.connection_id=sqlc.arg(connection_id) AND prior.stream=sqlc.arg(stream)
ON CONFLICT(connection_id,stream,operation) DO UPDATE SET revision=excluded.revision, state_version=excluded.state_version, state=excluded.state, next=excluded.next, failure=excluded.failure, failure_code=excluded.failure_code, observed=excluded.observed, acknowledged=excluded.acknowledged;
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

-- name: InitializeHistoryUpdate :exec
INSERT INTO history_update(singleton,applied) VALUES(1,?);
-- name: HistoryUpdate :one
SELECT * FROM history_update WHERE singleton=1;
-- name: StartHistoryUpdate :exec
UPDATE history_update SET target=?,phase='activity',cursor='',completed=0 WHERE singleton=1;
-- name: AdvanceHistoryUpdate :exec
UPDATE history_update SET phase=?,cursor=?,completed=completed+sqlc.arg(processed) WHERE singleton=1;
-- name: FinishHistoryUpdate :exec
UPDATE history_update SET applied=target,target=0,phase='',cursor='' WHERE singleton=1;
-- name: HistoryActivities :many
SELECT * FROM executions WHERE id>sqlc.arg(cursor) ORDER BY id LIMIT sqlc.arg(batch_size);
-- name: HistoryInstruments :many
SELECT i.id FROM instruments i WHERE i.id>sqlc.arg(cursor)
 AND NOT EXISTS(SELECT 1 FROM resource_aliases a WHERE a.kind='instruments' AND a.id=i.id)
 ORDER BY i.id LIMIT sqlc.arg(batch_size);

-- name: SavedCredentials :many
SELECT c.connection_id, a.provider, c.key, c.encrypted
FROM access.credentials c JOIN access.identities a ON a.id=c.connection_id;

-- name: SavedReferenceCredential :one
SELECT encrypted FROM access.reference_settings WHERE singleton=1;

-- name: PauseForCredentialKey :exec
INSERT INTO sync.profile(singleton,access_id,paused,reference_review,recovery_revision)
SELECT prior.singleton,prior.access_id,1,prior.reference_review,prior.recovery_revision+1 FROM sync.profile AS prior
WHERE prior.singleton=1 AND prior.paused=0
ON CONFLICT(singleton) DO UPDATE SET paused=excluded.paused, recovery_revision=excluded.recovery_revision;

-- name: RecoverySourceInputs :many
SELECT f.id,f.provider,f.name,c.settings,r.replan_at,CAST(coalesce(a.id,'') AS TEXT) AS account_id,CAST(coalesce(a.revision,0) AS INTEGER) AS account_revision,
 CAST(coalesce(a.authority,'') AS TEXT) AS authority,CAST(coalesce(a.kind,'') AS TEXT) AS kind,CAST(coalesce(a.scope,'') AS TEXT) AS scope,CAST(coalesce(a.value,'') AS TEXT) AS value
FROM sync.recovery_sources r JOIN main.connections f ON f.id=r.connection_id
LEFT JOIN sync.configurations c ON c.id=f.id AND c.provider=f.provider
LEFT JOIN main.accounts a ON a.connection_id=f.id AND a.provider=f.provider
ORDER BY f.id;

-- name: IsRecoverySource :one
SELECT EXISTS(SELECT 1 FROM sync.recovery_sources WHERE connection_id=?);

-- name: RecoveryCoverage :many
SELECT b.stream,CAST(max(r.reported_to) AS TEXT) AS through FROM main.acquisition_receipts r JOIN main.artifacts b ON b.id=r.artifact_id
WHERE b.connection_id=? AND r.operation IN ('acquire','supply') AND r.reported_to<>''
GROUP BY b.stream;

-- name: RecoveryPacingFloor :many
SELECT next FROM sync.pacing WHERE provider=?;

-- name: CurrentRestartScope :one
SELECT c.settings,a.id,a.revision
FROM sync.recovery_sources r
JOIN sync.configurations c ON c.id=r.connection_id
JOIN main.accounts a ON a.connection_id=c.id AND a.provider=c.provider
WHERE r.connection_id=?;

-- name: ApprovedRestartStreams :many
SELECT r.stream FROM sync.restart_scopes r
JOIN sync.configurations c ON c.id=r.connection_id AND c.settings=r.settings
JOIN main.accounts a ON a.connection_id=r.connection_id AND a.id=r.account_id AND a.revision=r.account_revision
WHERE r.connection_id=?;
