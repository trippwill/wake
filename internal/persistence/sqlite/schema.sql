-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

CREATE TABLE store_metadata(singleton INTEGER PRIMARY KEY CHECK(singleton=1), kind TEXT NOT NULL, version TEXT NOT NULL, id TEXT NOT NULL) STRICT;
CREATE TABLE profile(singleton INTEGER PRIMARY KEY CHECK(singleton=1), access_id TEXT NOT NULL, paused INTEGER NOT NULL CHECK(paused IN (0,1)), reference_review INTEGER NOT NULL CHECK(reference_review IN (0,1)), warehouse_reset INTEGER NOT NULL DEFAULT 0 CHECK(warehouse_reset IN (0,1)), recovery_revision INTEGER NOT NULL DEFAULT 0, resumed_by TEXT NOT NULL DEFAULT '', resumed_at TEXT NOT NULL DEFAULT '') STRICT;
CREATE TABLE normalization_receipts(artifact_id TEXT NOT NULL REFERENCES artifacts(id), version TEXT NOT NULL, counts BLOB NOT NULL, PRIMARY KEY(artifact_id,version)) STRICT;
CREATE TABLE processing_receipts(artifact_id TEXT NOT NULL REFERENCES artifacts(id), version TEXT NOT NULL, PRIMARY KEY(artifact_id,version)) STRICT;
CREATE TABLE connections(id TEXT PRIMARY KEY NOT NULL, provider TEXT NOT NULL, name TEXT NOT NULL DEFAULT '') STRICT;
CREATE TABLE work(connection_id TEXT NOT NULL REFERENCES connections(id), stream TEXT NOT NULL, operation TEXT NOT NULL, revision INTEGER NOT NULL DEFAULT 0, state_version TEXT NOT NULL DEFAULT '', state BLOB NOT NULL DEFAULT X'', next TEXT NOT NULL DEFAULT '', observed TEXT NOT NULL DEFAULT '', observed_completed INTEGER NOT NULL DEFAULT 0, observed_total INTEGER NOT NULL DEFAULT 0, acknowledged TEXT NOT NULL DEFAULT '', acknowledged_completed INTEGER NOT NULL DEFAULT 0, acknowledged_total INTEGER NOT NULL DEFAULT 0, failure TEXT NOT NULL DEFAULT '', failure_code TEXT NOT NULL DEFAULT '', PRIMARY KEY(connection_id,stream,operation)) STRICT;
CREATE TABLE artifacts(id TEXT PRIMARY KEY NOT NULL, connection_id TEXT NOT NULL REFERENCES connections(id), stream TEXT NOT NULL, operation TEXT NOT NULL, format TEXT NOT NULL, media_type TEXT NOT NULL, retrieved TEXT NOT NULL, hash TEXT NOT NULL, content BLOB NOT NULL, processed INTEGER NOT NULL DEFAULT 0, processing_next TEXT NOT NULL DEFAULT '', processing_failure TEXT NOT NULL DEFAULT '', requested_from TEXT NOT NULL DEFAULT '',requested_to TEXT NOT NULL DEFAULT '',reported_from TEXT NOT NULL DEFAULT '',reported_to TEXT NOT NULL DEFAULT '') STRICT;
CREATE TABLE report_comparisons(connection_id TEXT NOT NULL REFERENCES connections(id), stream TEXT NOT NULL, format TEXT NOT NULL, media_type TEXT NOT NULL, policy TEXT NOT NULL, digest TEXT NOT NULL, artifact_id TEXT NOT NULL REFERENCES artifacts(id), PRIMARY KEY(connection_id,stream,format,media_type,policy,digest)) STRICT;
CREATE TABLE acquisition_receipts(sequence INTEGER PRIMARY KEY, id TEXT UNIQUE NOT NULL, artifact_id TEXT NOT NULL REFERENCES artifacts(id), operation TEXT NOT NULL, retrieved TEXT NOT NULL, hash TEXT NOT NULL, policy TEXT NOT NULL, digest TEXT NOT NULL, requested_from TEXT NOT NULL,requested_to TEXT NOT NULL,reported_from TEXT NOT NULL,reported_to TEXT NOT NULL) STRICT;
CREATE INDEX receipts_body ON acquisition_receipts(artifact_id,operation);
CREATE TABLE candidates(receipt_id TEXT NOT NULL REFERENCES acquisition_receipts(id), artifact_id TEXT NOT NULL REFERENCES artifacts(id), authority TEXT NOT NULL, kind TEXT NOT NULL, scope TEXT NOT NULL, value TEXT NOT NULL, name TEXT NOT NULL, currency TEXT NOT NULL, PRIMARY KEY(receipt_id,authority,kind,scope,value)) STRICT;
CREATE TABLE source_claims(id TEXT PRIMARY KEY NOT NULL, connection_id TEXT NOT NULL REFERENCES connections(id), stream TEXT NOT NULL, version TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('trade','lifecycle','conversion','correction','instrument','rejected')), data BLOB NOT NULL) STRICT;
CREATE TABLE records(artifact_id TEXT NOT NULL REFERENCES artifacts(id), version TEXT NOT NULL, location TEXT NOT NULL, claim_id TEXT NOT NULL REFERENCES source_claims(id), ordinal INTEGER NOT NULL CHECK(ordinal>0), PRIMARY KEY(artifact_id,version,location)) STRICT;
CREATE VIEW record_values AS SELECT r.artifact_id,r.version,r.location,r.claim_id,r.ordinal,c.kind,c.data FROM records r JOIN source_claims c ON c.id=r.claim_id;
CREATE TABLE claim_processing(claim_id TEXT NOT NULL REFERENCES source_claims(id), ordinal INTEGER NOT NULL, artifact_id TEXT NOT NULL, version TEXT NOT NULL, location TEXT NOT NULL, PRIMARY KEY(claim_id,ordinal), FOREIGN KEY(artifact_id,version,location) REFERENCES records(artifact_id,version,location)) STRICT;
CREATE TABLE pacing(provider TEXT NOT NULL, policy TEXT NOT NULL, scope TEXT NOT NULL, next TEXT NOT NULL, PRIMARY KEY(provider,policy,scope)) STRICT;

CREATE TABLE scheduled(connection_id TEXT NOT NULL, stream TEXT NOT NULL, operation TEXT NOT NULL, enabled INTEGER NOT NULL, PRIMARY KEY(connection_id,stream,operation), FOREIGN KEY(connection_id,stream,operation) REFERENCES work(connection_id,stream,operation)) STRICT;
CREATE TABLE accounts(revision INTEGER NOT NULL DEFAULT 1,id TEXT PRIMARY KEY NOT NULL, singleton INTEGER NOT NULL UNIQUE CHECK(singleton=1), name TEXT NOT NULL, currency TEXT NOT NULL, connection_id TEXT NOT NULL REFERENCES connections(id), provider TEXT NOT NULL, stream TEXT NOT NULL, authority TEXT NOT NULL, kind TEXT NOT NULL, scope TEXT NOT NULL, value TEXT NOT NULL, artifact_id TEXT NOT NULL REFERENCES artifacts(id),rule TEXT NOT NULL DEFAULT 'account-authority/1') STRICT;
CREATE TABLE instruments(revision INTEGER NOT NULL DEFAULT 1,id TEXT PRIMARY KEY NOT NULL, source_key TEXT NOT NULL UNIQUE, name TEXT NOT NULL, ticker TEXT NOT NULL, kind TEXT NOT NULL, reported BLOB NOT NULL, reference_status TEXT NOT NULL, selection_authority TEXT NOT NULL DEFAULT 'automatic' CHECK(selection_authority IN ('automatic','operator')), reference_figi TEXT NOT NULL DEFAULT '', evidence_id TEXT NOT NULL DEFAULT '', reference_next TEXT NOT NULL DEFAULT '',rule TEXT NOT NULL DEFAULT 'qualified-instrument-reference/1', underlier_id TEXT REFERENCES instruments(id)) STRICT;
CREATE UNIQUE INDEX instrument_figi ON instruments(reference_figi) WHERE reference_figi<>'';
CREATE TABLE listings(revision INTEGER NOT NULL DEFAULT 1,id TEXT PRIMARY KEY NOT NULL, instrument_id TEXT NOT NULL REFERENCES instruments(id), venue TEXT NOT NULL, ticker TEXT NOT NULL, currency TEXT NOT NULL, figi TEXT NOT NULL DEFAULT '', UNIQUE(instrument_id,venue,ticker,currency)) STRICT;
CREATE TABLE executions(revision INTEGER NOT NULL DEFAULT 1,kind TEXT NOT NULL DEFAULT 'trade', id TEXT PRIMARY KEY NOT NULL, account_id TEXT NOT NULL REFERENCES accounts(id), instrument_id TEXT REFERENCES instruments(id), listing_id TEXT REFERENCES listings(id), accepted BLOB NOT NULL, status TEXT NOT NULL, review_reasons INTEGER NOT NULL DEFAULT 0 CHECK(review_reasons BETWEEN 0 AND 3), selection_authority TEXT NOT NULL DEFAULT 'automatic' CHECK(selection_authority IN ('automatic','operator')), rule TEXT NOT NULL) STRICT;
CREATE TABLE event_links(distinct_event INTEGER NOT NULL DEFAULT 0 CHECK(distinct_event IN (0,1)),account_id TEXT NOT NULL REFERENCES accounts(id), reference TEXT NOT NULL, execution_id TEXT NOT NULL REFERENCES executions(id), PRIMARY KEY(account_id,reference,execution_id)) STRICT;
CREATE TABLE execution_observations(artifact_id TEXT NOT NULL REFERENCES artifacts(id), version TEXT NOT NULL, location TEXT NOT NULL, execution_id TEXT REFERENCES executions(id), outcome TEXT NOT NULL, rule TEXT NOT NULL, PRIMARY KEY(artifact_id,version,location)) STRICT;
CREATE TABLE reference_budget(singleton INTEGER PRIMARY KEY CHECK(singleton=1), window_start TEXT NOT NULL, used INTEGER NOT NULL, cooldown TEXT NOT NULL) STRICT;
CREATE TABLE reference_evidence(id TEXT PRIMARY KEY NOT NULL, retrieved TEXT NOT NULL, request BLOB NOT NULL, response BLOB NOT NULL, http_status INTEGER NOT NULL, headers BLOB NOT NULL) STRICT;
CREATE TABLE instrument_reference_attempts(instrument_id TEXT NOT NULL REFERENCES instruments(id), evidence_id TEXT NOT NULL REFERENCES reference_evidence(id), status TEXT NOT NULL,rule TEXT NOT NULL, PRIMARY KEY(instrument_id,evidence_id)) STRICT;

CREATE TABLE activity_relationships(activity_id TEXT NOT NULL REFERENCES executions(id), kind TEXT NOT NULL, target TEXT NOT NULL, PRIMARY KEY(activity_id,kind,target)) STRICT;

CREATE TABLE activity_bases(activity_id TEXT PRIMARY KEY REFERENCES executions(id), data BLOB NOT NULL, instrument_id TEXT REFERENCES instruments(id), listing_id TEXT REFERENCES listings(id)) STRICT;
CREATE TABLE source_corrections(artifact_id TEXT NOT NULL, version TEXT NOT NULL, location TEXT NOT NULL, account_id TEXT NOT NULL REFERENCES accounts(id), kind TEXT NOT NULL, target TEXT NOT NULL, replacement TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', PRIMARY KEY(artifact_id,version,location), FOREIGN KEY(artifact_id,version,location) REFERENCES records(artifact_id,version,location)) STRICT;

CREATE TABLE operator_decisions(id TEXT PRIMARY KEY, target_kind TEXT NOT NULL, target_id TEXT NOT NULL, scope TEXT NOT NULL, text TEXT NOT NULL, currency TEXT NOT NULL, action TEXT NOT NULL, actor TEXT NOT NULL, at TEXT NOT NULL, rationale TEXT NOT NULL, supersedes TEXT NOT NULL, submission TEXT NOT NULL UNIQUE, request BLOB NOT NULL) STRICT;
CREATE INDEX decisions_target ON operator_decisions(target_kind,target_id,scope);
CREATE TABLE instrument_selections(instrument_id TEXT PRIMARY KEY REFERENCES instruments(id), name TEXT NOT NULL, ticker TEXT NOT NULL, figi TEXT NOT NULL, evidence TEXT NOT NULL, status TEXT NOT NULL, underlier_id TEXT REFERENCES instruments(id)) STRICT;

CREATE TABLE recovery_jobs(id TEXT PRIMARY KEY, status TEXT NOT NULL, completed INTEGER NOT NULL DEFAULT 0, total INTEGER NOT NULL, failure TEXT NOT NULL DEFAULT '', actor TEXT NOT NULL, created TEXT NOT NULL) STRICT;
CREATE UNIQUE INDEX one_active_recovery ON recovery_jobs((1)) WHERE status IN ('queued','running');
CREATE TABLE recovery_items(job_id TEXT NOT NULL REFERENCES recovery_jobs(id), group_id TEXT NOT NULL, kind TEXT NOT NULL, entity_id TEXT NOT NULL, complete INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(job_id,kind,entity_id)) STRICT;

CREATE TABLE publishing(singleton INTEGER PRIMARY KEY CHECK(singleton=1), generation TEXT NOT NULL) STRICT;
CREATE TABLE publishing_dirty(kind TEXT NOT NULL, id TEXT NOT NULL, PRIMARY KEY(kind,id)) STRICT;
CREATE TABLE published_resources(kind TEXT NOT NULL, id TEXT NOT NULL, revision INTEGER NOT NULL, snapshot BLOB NOT NULL, explanation BLOB NOT NULL, PRIMARY KEY(kind,id)) STRICT;
CREATE TABLE published_changes(position INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, id TEXT NOT NULL, revision INTEGER NOT NULL, snapshot BLOB NOT NULL) STRICT;
CREATE TRIGGER publish_accounts_insert AFTER INSERT ON accounts BEGIN INSERT OR IGNORE INTO publishing_dirty VALUES('accounts',NEW.id); END;
CREATE TRIGGER publish_accounts_update AFTER UPDATE ON accounts BEGIN INSERT OR IGNORE INTO publishing_dirty VALUES('accounts',NEW.id); END;
CREATE TRIGGER publish_instruments_insert AFTER INSERT ON instruments BEGIN INSERT OR IGNORE INTO publishing_dirty VALUES('instruments',NEW.id); END;
CREATE TRIGGER publish_instruments_update AFTER UPDATE ON instruments BEGIN INSERT OR IGNORE INTO publishing_dirty VALUES('instruments',NEW.id); END;
CREATE TRIGGER publish_listings_insert AFTER INSERT ON listings BEGIN INSERT OR IGNORE INTO publishing_dirty VALUES('listings',NEW.id); END;
CREATE TRIGGER publish_listings_update AFTER UPDATE ON listings BEGIN INSERT OR IGNORE INTO publishing_dirty VALUES('listings',NEW.id); END;
CREATE TRIGGER publish_executions_insert AFTER INSERT ON executions BEGIN INSERT OR IGNORE INTO publishing_dirty VALUES('activity',NEW.id); END;
CREATE TRIGGER publish_executions_update AFTER UPDATE ON executions BEGIN INSERT OR IGNORE INTO publishing_dirty VALUES('activity',NEW.id); END;
CREATE TRIGGER publish_relationship AFTER INSERT ON activity_relationships BEGIN
 INSERT OR IGNORE INTO publishing_dirty VALUES('activity',NEW.activity_id);
END;
CREATE TRIGGER publish_link AFTER INSERT ON event_links BEGIN
 INSERT OR IGNORE INTO publishing_dirty SELECT 'activity',r.activity_id FROM activity_relationships r JOIN executions a ON a.id=r.activity_id WHERE a.account_id=NEW.account_id AND r.target=NEW.reference;
 INSERT OR IGNORE INTO publishing_dirty SELECT 'activity',e.execution_id FROM (
  SELECT target,replacement FROM source_corrections WHERE account_id=NEW.account_id AND target=NEW.reference
  UNION
  SELECT target,replacement FROM source_corrections WHERE account_id=NEW.account_id AND replacement=NEW.reference
 ) c JOIN event_links e ON e.account_id=NEW.account_id AND e.reference IN (c.target,c.replacement);
END;
CREATE TRIGGER publish_correction_insert AFTER INSERT ON source_corrections BEGIN
 INSERT OR IGNORE INTO publishing_dirty SELECT 'activity',execution_id FROM event_links WHERE account_id=NEW.account_id AND reference IN (NEW.target,NEW.replacement);
END;
CREATE TRIGGER publish_correction_update AFTER UPDATE ON source_corrections BEGIN
 INSERT OR IGNORE INTO publishing_dirty SELECT 'activity',execution_id FROM event_links WHERE account_id=NEW.account_id AND reference IN (NEW.target,NEW.replacement);
END;

CREATE INDEX execution_observations_execution ON execution_observations(execution_id);

CREATE INDEX instrument_selections_figi ON instrument_selections(figi);

CREATE INDEX relationships_target ON activity_relationships(target,activity_id);
CREATE INDEX corrections_target ON source_corrections(account_id,target,replacement);
CREATE INDEX corrections_replacement ON source_corrections(account_id,replacement,target);
CREATE INDEX event_links_execution ON event_links(execution_id,account_id,reference);

-- Operational execution belongs to the warehouse's lifetime and transaction.
CREATE TABLE attempts(id TEXT PRIMARY KEY NOT NULL, artifact_id TEXT NOT NULL REFERENCES artifacts(id), version TEXT NOT NULL, outcome TEXT NOT NULL, counts BLOB NOT NULL) STRICT;
CREATE INDEX attempts_artifact ON attempts(artifact_id,version,outcome);
CREATE TABLE reference_cache(key TEXT PRIMARY KEY NOT NULL, status TEXT NOT NULL, records BLOB NOT NULL, expires TEXT NOT NULL, evidence_id TEXT NOT NULL REFERENCES reference_evidence(id)) STRICT;

CREATE TABLE instrument_claims(instrument_id TEXT NOT NULL REFERENCES instruments(id),claim_id TEXT NOT NULL REFERENCES source_claims(id),ordinal INTEGER NOT NULL,role TEXT NOT NULL CHECK(role IN ('activity','underlier')),PRIMARY KEY(instrument_id,claim_id,ordinal,role),FOREIGN KEY(claim_id,ordinal) REFERENCES claim_processing(claim_id,ordinal)) STRICT;
CREATE INDEX instrument_claim_occurrence ON instrument_claims(claim_id,ordinal);
CREATE INDEX observation_execution ON execution_observations(execution_id,artifact_id,version,location);


CREATE VIEW admin_activity AS SELECT p.id,p.snapshot,e.kind,e.status,e.review_reasons,e.selection_authority,
 CAST(coalesce(i.name,'') AS TEXT) AS instrument_name,
 CAST(coalesce(i.reference_status,'') AS TEXT) AS reference_status,
 CAST(coalesce(i.selection_authority,'automatic') AS TEXT) AS reference_authority,
 CAST(json_extract(p.snapshot,'$.activity.date') AS TEXT) AS event_date,
 CAST(coalesce(json_extract(p.snapshot,'$.activity.quantity.value'),json_extract(p.snapshot,'$.activity.sent.amount')) AS TEXT) AS quantity,
 CAST(coalesce(json_extract(p.snapshot,'$.activity.quantity.unit'),json_extract(p.snapshot,'$.activity.sent.currency'),'') AS TEXT) AS quantity_unit,
 CAST(coalesce(json_extract(p.snapshot,'$.activity.price.amount'),json_extract(p.snapshot,'$.activity.rate.amount')) AS TEXT) AS price,
 CAST(coalesce(json_extract(p.snapshot,'$.activity.price.currency'),json_extract(p.snapshot,'$.activity.rate.currency'),'') || '/' || coalesce(json_extract(p.snapshot,'$.activity.price_basis'),json_extract(p.snapshot,'$.activity.rate_base'),'') AS TEXT) AS price_unit,
 CAST(lower(coalesce(i.name,'') || ' ' || coalesce(json_extract(p.snapshot,'$.activity.side'),json_extract(p.snapshot,'$.activity.lifecycle_kind'),json_extract(p.snapshot,'$.activity.sent.currency'),'') || ' ' || coalesce(json_extract(p.snapshot,'$.activity.received.currency'),'')) AS TEXT) AS label_sort,
 CAST(lower(coalesce(i.name,'') || ' ' || coalesce(i.ticker,'') || ' ' || e.id || ' ' || coalesce(json_extract(p.snapshot,'$.activity.date'),'') || ' ' || coalesce(json_extract(p.snapshot,'$.activity.at'),'') || ' ' || coalesce(json_extract(p.snapshot,'$.activity.side'),json_extract(p.snapshot,'$.activity.lifecycle_kind'),'') || ' ' || coalesce(json_extract(p.snapshot,'$.activity.quantity.value'),json_extract(p.snapshot,'$.activity.sent.amount'),'') || ' ' || coalesce(json_extract(p.snapshot,'$.activity.price.amount'),json_extract(p.snapshot,'$.activity.rate.amount'),'') || ' ' || coalesce(json_extract(p.snapshot,'$.activity.price.currency'),json_extract(p.snapshot,'$.activity.rate.currency'),'') || ' ' || coalesce(json_extract(p.snapshot,'$.activity.sent.currency'),'') || ' ' || coalesce(json_extract(p.snapshot,'$.activity.received.currency'),'') || ' ' || coalesce(json_extract(p.snapshot,'$.activity.received.amount'),'')) AS TEXT) AS search_text
 FROM published_resources p JOIN executions e ON e.id=p.id LEFT JOIN instruments i ON i.id=e.instrument_id WHERE p.kind='activity';
CREATE VIEW admin_instruments AS SELECT p.id,p.snapshot,i.name,i.ticker,i.kind,i.reference_status,i.selection_authority,lower(i.name || ' ' || i.ticker || ' ' || i.kind || ' ' || i.id) AS search_text FROM published_resources p JOIN instruments i ON i.id=p.id WHERE p.kind='instruments';
CREATE INDEX activity_state ON executions(status,review_reasons,selection_authority,id);
CREATE INDEX observation_identity_review ON execution_observations(artifact_id,outcome);
CREATE INDEX activity_base_instrument ON activity_bases(instrument_id);
CREATE VIEW admin_review AS
 SELECT 'activity' AS kind,a.id,a.instrument_name AS name,'' AS artifact,'' AS location,'' AS status,1 AS claim_count FROM admin_activity a WHERE a.review_reasons<>0
 UNION ALL SELECT 'instrument',i.id,i.name,'','','',1 FROM admin_instruments i WHERE i.reference_status IN ('ambiguous','conflict')
 UNION ALL SELECT 'instruction',c.artifact_id || '/' || c.version || '/' || c.location,c.kind,c.artifact_id,c.location,c.status,1 FROM source_corrections c JOIN records r ON r.artifact_id=c.artifact_id AND r.version=c.version AND r.location=c.location JOIN claim_processing p ON p.claim_id=r.claim_id AND p.ordinal=r.ordinal AND p.artifact_id=r.artifact_id AND p.version=r.version AND p.location=r.location WHERE c.status IN ('unresolved','review') AND NOT EXISTS(SELECT 1 FROM event_links l WHERE l.account_id=c.account_id AND (l.reference=c.target OR l.reference=c.replacement))
 UNION ALL SELECT 'identity',o.artifact_id,'Unresolved source identity',o.artifact_id,'','',count(*) FROM execution_observations o JOIN records r ON r.artifact_id=o.artifact_id AND r.version=o.version AND r.location=o.location JOIN claim_processing p ON p.claim_id=r.claim_id AND p.ordinal=r.ordinal AND p.artifact_id=r.artifact_id AND p.version=r.version AND p.location=r.location WHERE o.execution_id IS NULL AND o.outcome IN ('identity-review','native-review') GROUP BY o.artifact_id;

CREATE INDEX artifacts_connection ON artifacts(connection_id);
CREATE INDEX candidate_artifact ON candidates(artifact_id,authority,kind,scope,value,receipt_id);
CREATE INDEX source_instruction_review ON source_corrections(status,artifact_id,version,location);
