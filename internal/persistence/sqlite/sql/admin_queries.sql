-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- name: AdminInstrumentCount :one
SELECT (SELECT count(*) FROM admin_instruments) AS total,count(*) AS matched FROM admin_instruments WHERE (sqlc.arg(kind_filter)='' OR kind=sqlc.arg(kind_filter))
 AND (sqlc.arg(state_filter)='' OR sqlc.arg(state_filter)='review' AND reference_status IN ('ambiguous','conflict') OR sqlc.arg(state_filter)='gaps' AND reference_status NOT IN ('resolved','ambiguous','conflict') OR reference_status=sqlc.arg(state_filter))
 AND (sqlc.arg(search)='' OR wake_contains(search_text,sqlc.arg(search))=1 OR sqlc.arg(missing_values)=1 AND (name='' OR ticker='')
 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(reference_matches)) match WHERE json_extract(match.value,'$.status')=reference_status AND json_extract(match.value,'$.authority')=selection_authority));
-- name: AdminInstrumentRows :many
WITH ordering AS (SELECT CAST(sqlc.arg(sort_field) AS TEXT) AS sort_field,CAST(sqlc.arg(direction) AS TEXT) AS direction)
SELECT snapshot,selection_authority FROM admin_instruments CROSS JOIN ordering WHERE (sqlc.arg(kind_filter)='' OR kind=sqlc.arg(kind_filter))
 AND (sqlc.arg(state_filter)='' OR sqlc.arg(state_filter)='review' AND reference_status IN ('ambiguous','conflict') OR sqlc.arg(state_filter)='gaps' AND reference_status NOT IN ('resolved','ambiguous','conflict') OR reference_status=sqlc.arg(state_filter))
 AND (sqlc.arg(search)='' OR wake_contains(search_text,sqlc.arg(search))=1 OR sqlc.arg(missing_values)=1 AND (name='' OR ticker='')
 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(reference_matches)) match WHERE json_extract(match.value,'$.status')=reference_status AND json_extract(match.value,'$.authority')=selection_authority))
 ORDER BY
 CASE ordering.sort_field WHEN 'name' THEN name='' ELSE 0 END ASC,
 CASE WHEN ordering.direction='asc' THEN CASE ordering.sort_field WHEN 'name' THEN name WHEN 'ticker' THEN coalesce(nullif(ticker,''),nullif(name,''),'Unknown') WHEN 'kind' THEN kind WHEN 'state' THEN reference_status END END COLLATE NOCASE ASC,
 CASE WHEN ordering.direction='desc' THEN CASE ordering.sort_field WHEN 'name' THEN name WHEN 'ticker' THEN coalesce(nullif(ticker,''),nullif(name,''),'Unknown') WHEN 'kind' THEN kind WHEN 'state' THEN reference_status END END COLLATE NOCASE DESC,
 CASE WHEN ordering.sort_field='state' AND ordering.direction='asc' THEN selection_authority END ASC,
 CASE WHEN ordering.sort_field='state' AND ordering.direction='desc' THEN selection_authority END DESC,
 id ASC LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);
-- name: AdminCounts :one
SELECT (SELECT count(*) FROM published_resources WHERE kind='activity') AS activity_count,(SELECT count(*) FROM published_resources WHERE kind='instruments') AS instrument_count,(SELECT count(*) FROM connections) AS connection_count;

-- name: AdminPositionOverview :one
SELECT count(*) AS available,
 CAST(coalesce(max(CASE WHEN account_id=sqlc.arg(account_id) THEN json_extract(CAST(data AS TEXT),'$.as_of') END),'') AS TEXT) AS as_of
FROM positions_current WHERE json_extract(CAST(data AS TEXT),'$.status')='available';

-- name: AdminEvidenceMetadata :one
SELECT c.name AS connection_name,c.provider,a.id,a.stream,a.format,a.media_type,a.retrieved,a.hash,a.operation,a.requested_from,a.requested_to,a.reported_from,a.reported_to
FROM artifacts a JOIN connections c ON c.id=a.connection_id WHERE a.id=?;

-- name: AdminReportCount :one
SELECT (SELECT count(*) FROM acquisition_receipts WHERE operation IN ('acquire','supply')) AS total,
 count(*) AS matched
FROM acquisition_receipts r JOIN artifacts a ON a.id=r.artifact_id
JOIN connections c ON c.id=a.connection_id LEFT JOIN accounts account ON account.connection_id=c.id AND account.provider=c.provider
WHERE r.operation IN ('acquire','supply') AND (sqlc.arg(search)='' OR wake_contains(c.name || ' ' || c.provider || ' ' || coalesce(account.name,'') || ' ' || a.stream || ' ' || wake_report_title(a.stream),sqlc.arg(search))=1);

-- name: AdminPendingReport :one
SELECT a.id AS artifact_id,c.id AS connection_id,
 CAST(coalesce(nullif(c.name,''),nullif(account.name,''),c.provider) AS TEXT) AS connection_name,
 CAST(account.id IS NOT NULL AS INTEGER) AS account_confirmed
FROM artifacts a JOIN connections c ON c.id=a.connection_id
LEFT JOIN accounts account ON account.connection_id=c.id AND account.provider=c.provider
JOIN json_each(CAST(sqlc.arg(versions) AS TEXT)) version ON version.key=c.provider AND version.value<>''
WHERE EXISTS(SELECT 1 FROM acquisition_receipts r WHERE r.artifact_id=a.id AND r.operation IN ('acquire','supply'))
 AND NOT EXISTS(SELECT 1 FROM processing_receipts p WHERE p.artifact_id=a.id AND p.version=version.value)
ORDER BY a.rowid DESC LIMIT 1;

-- name: AdminReportRows :many
WITH page AS (
 SELECT r.sequence,r.id,r.artifact_id,r.operation,r.retrieved,r.hash,r.policy,r.requested_from,r.requested_to,r.reported_from,r.reported_to,
  c.id AS connection_id,CAST(coalesce(nullif(c.name,''),nullif(account.name,''),c.provider) AS TEXT) AS connection_name,
  c.provider,a.stream
 FROM acquisition_receipts r JOIN artifacts a ON a.id=r.artifact_id
 JOIN connections c ON c.id=a.connection_id LEFT JOIN accounts account ON account.connection_id=c.id AND account.provider=c.provider
 WHERE r.operation IN ('acquire','supply') AND (sqlc.arg(search)='' OR wake_contains(c.name || ' ' || c.provider || ' ' || coalesce(account.name,'') || ' ' || a.stream || ' ' || wake_report_title(a.stream),sqlc.arg(search))=1)
 ORDER BY r.retrieved COLLATE wake_instant DESC,r.sequence DESC LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset)
), reports AS (
 SELECT page.*,CAST(coalesce(version.value,'') AS TEXT) AS version,
  CAST(EXISTS(SELECT 1 FROM acquisition_receipts prior WHERE prior.artifact_id=page.artifact_id AND prior.operation IN ('acquire','supply') AND prior.sequence<page.sequence) AS INTEGER) AS reused_body
 FROM page LEFT JOIN json_each(CAST(sqlc.arg(versions) AS TEXT)) version ON version.key=page.provider
), activity AS (
 SELECT r.id,count(*) FILTER (WHERE o.outcome='accepted') AS accepted,
  count(*) FILTER (WHERE o.outcome='enriched') AS enriched,
  count(*) FILTER (WHERE o.outcome='no-additional-contribution') AS repeated,
  count(*) FILTER (WHERE o.outcome IN ('identity-review','native-review','incomplete','incomplete-conflict','kind-conflict','terms-conflict')) AS needs_review
 FROM reports r JOIN report_observations o ON o.artifact_id=r.artifact_id AND o.version=r.version
 WHERE r.reused_body=0 GROUP BY r.id
)
SELECT r.id,r.artifact_id,r.operation,r.retrieved,r.hash,r.policy,r.requested_from,r.requested_to,r.reported_from,r.reported_to,
 r.connection_id,r.connection_name,r.provider,r.stream,r.reused_body,
 CAST(p.artifact_id IS NOT NULL AS INTEGER) AS processed,
 CAST(coalesce(json_extract(n.counts,'$.Activities'),0) AS INTEGER) AS activity_claims,
 CAST(coalesce(json_extract(n.counts,'$.PositionSets'),0) AS INTEGER) AS position_sets,
 CAST(coalesce(json_extract(n.counts,'$.Rejected'),0) AS INTEGER) AS excluded_rows,
 CAST(coalesce(a.accepted,0) AS INTEGER) AS accepted,CAST(coalesce(a.enriched,0) AS INTEGER) AS enriched,
 CAST(coalesce(a.repeated,0) AS INTEGER) AS repeated,CAST(coalesce(a.needs_review,0) AS INTEGER) AS needs_review
FROM reports r
LEFT JOIN processing_receipts p ON p.artifact_id=r.artifact_id AND p.version=r.version
LEFT JOIN normalization_receipts n ON n.artifact_id=p.artifact_id AND n.version=p.version AND r.reused_body=0
LEFT JOIN activity a ON a.id=r.id
ORDER BY r.retrieved COLLATE wake_instant DESC,r.sequence DESC;

-- name: AdminReferenceStatus :one
SELECT CAST(length(coalesce((SELECT encrypted FROM access.reference_settings WHERE singleton=1),X''))>0 AS INTEGER) AS has_key,
 CAST(coalesce((SELECT failure FROM sync.reference_diagnostics WHERE singleton=1),'') AS TEXT) AS failure;
-- name: AdminSourceCount :one
SELECT count(*) FROM connections WHERE sqlc.arg(search)='' OR wake_contains(name || ' ' || provider,sqlc.arg(search))=1;
-- name: AdminSourceRows :many
SELECT * FROM connections WHERE sqlc.arg(search)='' OR wake_contains(name || ' ' || provider,sqlc.arg(search))=1 ORDER BY name COLLATE NOCASE,id LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: AdminCandidateCount :one
SELECT count(*) FROM (SELECT authority,kind,scope,value FROM eligible_candidates WHERE connection_id=? GROUP BY authority,kind,scope,value);
-- name: AdminCandidates :many
SELECT artifact_id,authority,kind,scope,value,name,currency FROM (
 SELECT c.*,c.sequence AS appearance,row_number() OVER(PARTITION BY c.authority,c.kind,c.scope,c.value ORDER BY c.sequence DESC) AS ranking
 FROM eligible_candidates c WHERE c.connection_id=sqlc.arg(connection_id)
) WHERE ranking=1 ORDER BY appearance DESC,authority,kind,scope,value LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: AdminReviewCount :one
WITH display_review AS (SELECT r.*, CAST(coalesce((SELECT i.ticker FROM instruments i WHERE i.id=CASE WHEN r.kind='instrument' THEN r.id ELSE json_extract(nullif(r.activity,''),'$.instrument_id') END),'') AS TEXT) AS ticker FROM admin_review r)
SELECT (SELECT count(*) FROM admin_review) AS total,count(*) AS matched FROM display_review WHERE sqlc.arg(search)='' OR wake_contains(search_text || ' ' || kind || ' ' || id,sqlc.arg(search))=1 OR label_key<>'' AND wake_contains(label_key,sqlc.arg(search))=1 OR kind='activity' AND json_extract(activity,'$.family')='cash' AND wake_contains(coalesce(json_extract(activity,'$.description'),''),sqlc.arg(search))=1 OR kind='activity' AND json_extract(activity,'$.family')='security-transfer' AND json_extract(sqlc.arg(activity_text),'$.transfer_prefix')<>'' AND wake_contains(json_extract(sqlc.arg(activity_text),'$.transfer_prefix') || coalesce(json_extract(activity,'$.direction'),'') || json_extract(sqlc.arg(activity_text),'$.movement_separator') || coalesce(nullif(ticker,''),nullif(name,''),json_extract(sqlc.arg(activity_text),'$.unknown')),sqlc.arg(search))=1 OR kind='activity' AND json_extract(activity,'$.family')='cash' AND json_extract(sqlc.arg(activity_text),'$.movement_separator')<>'' AND wake_contains(replace(json_extract(activity,'$.category'),'-',' ') || json_extract(sqlc.arg(activity_text),'$.movement_separator') || coalesce(nullif(json_extract(activity,'$.description'),''),json_extract(sqlc.arg(activity_text),'$.unknown')),sqlc.arg(search))=1 OR kind='activity' AND json_extract(activity,'$.family') IN ('trade','lifecycle') AND json_extract(sqlc.arg(activity_text),'$.unknown')<>'' AND wake_contains(coalesce(json_extract(activity,'$.side'),json_extract(activity,'$.lifecycle_kind')) || CASE WHEN json_extract(activity,'$.family')='lifecycle' THEN json_extract(sqlc.arg(activity_text),'$.lifecycle_separator') ELSE ' ' END || coalesce(nullif(ticker,''),nullif(name,''),json_extract(sqlc.arg(activity_text),'$.unknown')),sqlc.arg(search))=1 OR kind='activity' AND json_extract(activity,'$.family')='conversion' AND json_extract(sqlc.arg(activity_text),'$.pair_separator')<>'' AND wake_contains(json_extract(sqlc.arg(activity_text),'$.conversion_prefix') || coalesce(nullif(json_extract(activity,'$.sent.currency'),''),json_extract(sqlc.arg(activity_text),'$.unknown')) || json_extract(sqlc.arg(activity_text),'$.pair_separator') || coalesce(nullif(json_extract(activity,'$.received.currency'),''),json_extract(sqlc.arg(activity_text),'$.unknown')),sqlc.arg(search))=1 OR sqlc.arg(missing_values)=1 AND (kind='instrument' AND name='' OR kind='activity' AND (json_extract(activity,'$.family')='cash' AND coalesce(json_extract(activity,'$.description'),'')='' OR json_extract(activity,'$.family') NOT IN ('conversion','cash') AND name='')) OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(kind_matches)) match WHERE kind='activity' AND json_extract(activity,'$.family')=match.value) OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(review_matches)) match WHERE json_extract(match.value,'$.kind')=display_review.kind AND (json_extract(match.value,'$.status')='' OR json_extract(match.value,'$.status')=display_review.status) AND (json_extract(match.value,'$.count_known')<>1 OR json_extract(match.value,'$.count')=claim_count));
-- name: AdminReviewRows :many
WITH display_review AS (SELECT r.*, CAST(coalesce((SELECT i.ticker FROM instruments i WHERE i.id=CASE WHEN r.kind='instrument' THEN r.id ELSE json_extract(nullif(r.activity,''),'$.instrument_id') END),'') AS TEXT) AS ticker FROM admin_review r)
SELECT CAST(kind AS TEXT) AS kind,CAST(id AS TEXT) AS id,CAST(name AS TEXT) AS name,ticker,CAST(artifact AS TEXT) AS artifact,CAST(location AS TEXT) AS location,CAST(status AS TEXT) AS status,CAST(claim_count AS INTEGER) AS claim_count,CAST(activity AS TEXT) AS activity FROM display_review WHERE sqlc.arg(search)='' OR wake_contains(search_text || ' ' || kind || ' ' || id,sqlc.arg(search))=1 OR label_key<>'' AND wake_contains(label_key,sqlc.arg(search))=1 OR kind='activity' AND json_extract(activity,'$.family')='cash' AND wake_contains(coalesce(json_extract(activity,'$.description'),''),sqlc.arg(search))=1 OR kind='activity' AND json_extract(activity,'$.family')='security-transfer' AND json_extract(sqlc.arg(activity_text),'$.transfer_prefix')<>'' AND wake_contains(json_extract(sqlc.arg(activity_text),'$.transfer_prefix') || coalesce(json_extract(activity,'$.direction'),'') || json_extract(sqlc.arg(activity_text),'$.movement_separator') || coalesce(nullif(ticker,''),nullif(name,''),json_extract(sqlc.arg(activity_text),'$.unknown')),sqlc.arg(search))=1 OR kind='activity' AND json_extract(activity,'$.family')='cash' AND json_extract(sqlc.arg(activity_text),'$.movement_separator')<>'' AND wake_contains(replace(json_extract(activity,'$.category'),'-',' ') || json_extract(sqlc.arg(activity_text),'$.movement_separator') || coalesce(nullif(json_extract(activity,'$.description'),''),json_extract(sqlc.arg(activity_text),'$.unknown')),sqlc.arg(search))=1 OR kind='activity' AND json_extract(activity,'$.family') IN ('trade','lifecycle') AND json_extract(sqlc.arg(activity_text),'$.unknown')<>'' AND wake_contains(coalesce(json_extract(activity,'$.side'),json_extract(activity,'$.lifecycle_kind')) || CASE WHEN json_extract(activity,'$.family')='lifecycle' THEN json_extract(sqlc.arg(activity_text),'$.lifecycle_separator') ELSE ' ' END || coalesce(nullif(ticker,''),nullif(name,''),json_extract(sqlc.arg(activity_text),'$.unknown')),sqlc.arg(search))=1 OR kind='activity' AND json_extract(activity,'$.family')='conversion' AND json_extract(sqlc.arg(activity_text),'$.pair_separator')<>'' AND wake_contains(json_extract(sqlc.arg(activity_text),'$.conversion_prefix') || coalesce(nullif(json_extract(activity,'$.sent.currency'),''),json_extract(sqlc.arg(activity_text),'$.unknown')) || json_extract(sqlc.arg(activity_text),'$.pair_separator') || coalesce(nullif(json_extract(activity,'$.received.currency'),''),json_extract(sqlc.arg(activity_text),'$.unknown')),sqlc.arg(search))=1 OR sqlc.arg(missing_values)=1 AND (kind='instrument' AND name='' OR kind='activity' AND (json_extract(activity,'$.family')='cash' AND coalesce(json_extract(activity,'$.description'),'')='' OR json_extract(activity,'$.family') NOT IN ('conversion','cash') AND name='')) OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(kind_matches)) match WHERE kind='activity' AND json_extract(activity,'$.family')=match.value) OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(review_matches)) match WHERE json_extract(match.value,'$.kind')=display_review.kind AND (json_extract(match.value,'$.status')='' OR json_extract(match.value,'$.status')=display_review.status) AND (json_extract(match.value,'$.count_known')<>1 OR json_extract(match.value,'$.count')=claim_count)) ORDER BY kind,id LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);
-- name: AdminCandidate :one
SELECT artifact_id,authority,kind,scope,value,name,currency FROM eligible_candidates WHERE connection_id=? AND artifact_id=? AND authority=? AND kind=? AND scope=? AND value=? ORDER BY sequence DESC LIMIT 1;

-- name: AdminAccessSources :many
SELECT * FROM connections ORDER BY id;

-- name: AdminObservationRows :many
WITH subject AS (
 SELECT CAST(sqlc.arg(subject_kind) AS TEXT) AS kind,CAST(sqlc.arg(subject_id) AS TEXT) AS id
), scoped AS (
 SELECT o.artifact_id,o.version,o.location,o.rule,o.outcome,o.execution_id
 FROM subject s JOIN admin_observations o ON o.subject_kind=s.kind AND o.subject_id=s.id
 UNION ALL
 SELECT o.artifact_id,o.version,o.location,o.rule,o.outcome,o.execution_id
 FROM subject s JOIN report_observations o ON s.kind='activity-appearances' AND o.execution_id=s.id
), details AS (
 SELECT o.*,a.retrieved,a.stream,c.name AS source_name,c.provider AS source_provider,r.claim_id,r.ordinal,claim.data AS reported,
  CASE WHEN sqlc.arg(sort_field)='date' THEN (
   SELECT json_extract(f.value,'$.Date') FROM json_each(CAST(claim.data AS TEXT),'$.Fields') f
   WHERE json_extract(f.value,'$.Name')='date'
  ) END AS activity_date
 FROM scoped o JOIN artifacts a ON a.id=o.artifact_id JOIN connections c ON c.id=a.connection_id
 LEFT JOIN records r USING(artifact_id,version,location)
 LEFT JOIN source_claims claim ON claim.id=r.claim_id AND claim.kind='activity'
), matched AS (
 SELECT o.* FROM details o
 WHERE sqlc.arg(search)='' OR wake_contains(o.artifact_id || ' ' || coalesce(o.execution_id,'') || ' ' ||
  o.location || ' ' || o.rule || ' ' || char(183) || ' ' || o.version || ' ' || o.retrieved || ' ' ||
  o.source_name || ' ' || o.stream,sqlc.arg(search))=1
 OR wake_contains(o.source_name || ' ' || char(183) || ' ' || wake_report_title(o.stream),sqlc.arg(search))=1
 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(outcome_matches)) m WHERE m.value=o.outcome)
 OR EXISTS(
  SELECT 1 FROM json_each(CAST(o.reported AS TEXT),'$.Fields') f
  WHERE wake_contains(concat_ws(' ',json_extract(f.value,'$.Instrument.Ticker'),json_extract(f.value,'$.Instrument.Name'),
   nullif(json_extract(f.value,'$.Text'),''),nullif(json_extract(f.value,'$.Date'),''),
   concat_ws(' ',nullif(json_extract(f.value,'$.Price.Amount'),''),nullif(json_extract(f.value,'$.Price.Currency'),'')),
   concat_ws(' ',nullif(json_extract(f.value,'$.Money.Amount'),''),nullif(json_extract(f.value,'$.Money.Currency'),'')),
   concat_ws(' ',nullif(json_extract(f.value,'$.Quantity'),''),nullif(json_extract(f.value,'$.QuantityUnit'),''))),sqlc.arg(search))=1
 )
 OR EXISTS(
  SELECT 1 FROM json_each(CAST(o.reported AS TEXT),'$.Fields') i
  CROSS JOIN json_each(CAST(o.reported AS TEXT),'$.Fields') t
  WHERE json_extract(i.value,'$.Name')='instrument' AND json_extract(t.value,'$.Name') IN ('side','kind')
   AND wake_contains(json_extract(t.value,'$.Text') || ' ' ||
    coalesce(nullif(json_extract(i.value,'$.Instrument.Ticker'),''),json_extract(i.value,'$.Instrument.Name')),
    sqlc.arg(search))=1
 )
 OR wake_contains(json_extract(CAST(o.reported AS TEXT),'$.Event[3]'),sqlc.arg(search))=1
 OR EXISTS(
  SELECT 1 FROM json_each(CAST(o.reported AS TEXT),'$.EventAliases') alias
  WHERE wake_contains(json_extract(alias.value,'$[3]'),sqlc.arg(search))=1
 )
), ordering AS (
 SELECT CAST(sqlc.arg(sort_field) AS TEXT) AS sort_field,CAST(sqlc.arg(direction) AS TEXT) AS direction,
  CAST(sqlc.arg(outcome_labels) AS TEXT) AS outcome_labels
), ordered AS (
 SELECT o.*,row_number() OVER (ORDER BY
  CASE WHEN sort_field='report' AND direction='asc' THEN o.retrieved END COLLATE wake_instant ASC,
  CASE WHEN sort_field='report' AND direction='desc' THEN o.retrieved END COLLATE wake_instant DESC,
  CASE WHEN sort_field='outcome' AND direction='asc' THEN coalesce(labels.value,o.outcome) END COLLATE NOCASE ASC,
  CASE WHEN sort_field='outcome' AND direction='desc' THEN coalesce(labels.value,o.outcome) END COLLATE NOCASE DESC,
  CASE WHEN sort_field='activity' AND direction='asc' THEN o.execution_id END ASC,
  CASE WHEN sort_field='activity' AND direction='desc' THEN o.execution_id END DESC,
  CASE WHEN sort_field='date' AND direction='asc' THEN o.activity_date END ASC,
  CASE WHEN sort_field='date' AND direction='desc' THEN o.activity_date END DESC,
  CASE WHEN sort_field='location' AND direction='asc' THEN o.location END ASC,
  CASE WHEN sort_field='location' AND direction='desc' THEN o.location END DESC,
  CASE WHEN sort_field='rule' AND direction='asc' THEN o.rule || ' ' || char(183) || ' ' || o.version END ASC,
  CASE WHEN sort_field='rule' AND direction='desc' THEN o.rule || ' ' || char(183) || ' ' || o.version END DESC,
  o.artifact_id,o.version,CASE WHEN s.kind='artifact' THEN (SELECT r.rowid FROM records r
    WHERE r.artifact_id=o.artifact_id AND r.version=o.version AND r.location=o.location) END,o.location) AS position
 FROM matched o CROSS JOIN ordering CROSS JOIN subject s LEFT JOIN json_each(ordering.outcome_labels) labels ON labels.key=o.outcome
), page AS (
 SELECT * FROM ordered ORDER BY position LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset)
), counts AS (
 SELECT (SELECT count(*) FROM scoped) AS total,(SELECT count(*) FROM matched) AS matched
)
SELECT CAST(coalesce(p.artifact_id,'') AS TEXT) AS artifact_id,CAST(coalesce(p.version,'') AS TEXT) AS version,
 CAST(coalesce(p.location,'') AS TEXT) AS location,CAST(coalesce(p.rule,'') AS TEXT) AS rule,
 CAST(coalesce(p.outcome,'') AS TEXT) AS outcome,CAST(coalesce(p.execution_id,'') AS TEXT) AS execution_id,
 CAST(coalesce(p.retrieved,'') AS TEXT) AS retrieved,CAST(coalesce(p.source_name,'') AS TEXT) AS source_name,
 CAST(coalesce(p.source_provider,'') AS TEXT) AS source_provider,CAST(coalesce(p.stream,'') AS TEXT) AS stream,
 CAST(coalesce(p.reported,'') AS BLOB) AS reported,
 CAST(CASE WHEN p.outcome='no-additional-contribution' THEN EXISTS (
  SELECT 1 FROM records prior JOIN artifacts other ON other.id=prior.artifact_id
  JOIN processing_receipts done ON done.artifact_id=prior.artifact_id AND done.version=prior.version
  WHERE prior.claim_id=p.claim_id AND prior.ordinal=p.ordinal AND other.retrieved COLLATE wake_instant < p.retrieved
 ) ELSE 0 END AS INTEGER) AS earlier_appearance,
 CAST(counts.total AS INTEGER) AS total,CAST(counts.matched AS INTEGER) AS matched
FROM counts LEFT JOIN page p ON true ORDER BY p.position;

-- name: AdminSourceObservation :one
SELECT CAST(coalesce(o.execution_id, '') AS TEXT) AS execution_id,
 o.outcome, o.rule, c.data,
 CAST(CASE WHEN o.outcome='no-additional-contribution' THEN EXISTS (
  SELECT 1 FROM records prior JOIN artifacts other ON other.id=prior.artifact_id
  JOIN processing_receipts done ON done.artifact_id=prior.artifact_id AND done.version=prior.version
  WHERE prior.claim_id=r.claim_id AND prior.ordinal=r.ordinal AND other.retrieved COLLATE wake_instant < a.retrieved
 ) ELSE 0 END AS INTEGER) AS earlier_appearance,
 CAST(coalesce((SELECT p.location FROM records p JOIN report_observations e USING(artifact_id, version, location)
   WHERE p.artifact_id=r.artifact_id AND p.version=r.version AND p.rowid<r.rowid
   ORDER BY p.rowid DESC LIMIT 1), '') AS TEXT) AS previous,
 CAST(coalesce((SELECT n.location FROM records n JOIN report_observations e USING(artifact_id, version, location)
   WHERE n.artifact_id=r.artifact_id AND n.version=r.version AND n.rowid>r.rowid
   ORDER BY n.rowid ASC LIMIT 1), '') AS TEXT) AS next
FROM report_observations o
JOIN record_values c USING(artifact_id, version, location)
JOIN records r USING(artifact_id, version, location)
JOIN artifacts a ON a.id=o.artifact_id
WHERE o.artifact_id=? AND o.version=? AND o.location=?;
