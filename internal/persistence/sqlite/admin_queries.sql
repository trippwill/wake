-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- name: AdminActivityCount :one
SELECT (SELECT count(*) FROM admin_activity) AS total,count(*) AS matched FROM admin_activity WHERE (sqlc.arg(kind_filter)='' OR kind=sqlc.arg(kind_filter))
 AND (sqlc.arg(state_filter)='' OR sqlc.arg(state_filter)='review' AND review_reasons<>0 OR status=sqlc.arg(state_filter))
 AND (sqlc.arg(search)='' OR wake_contains(search_text,sqlc.arg(search))=1 OR wake_contains(label_sort,coalesce(nullif(sqlc.arg(label_search),''),sqlc.arg(search)))=1
 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(amount_matches)) match WHERE wake_contains(coalesce(json_extract(snapshot,'$.activity.' || json_extract(match.value,'$.role') || '.amount'),'') || ' ' || coalesce(json_extract(snapshot,'$.activity.' || json_extract(match.value,'$.role') || '.currency'),''),json_extract(match.value,'$.search'))=1 AND json_type(snapshot,'$.activity.' || json_extract(match.value,'$.role'))='object')
 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(kind_matches)) match WHERE match.value=admin_activity.kind)
 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(state_matches)) match WHERE json_extract(match.value,'$.lifecycle')=status AND json_extract(match.value,'$.review')=review_reasons AND json_extract(match.value,'$.authority')=selection_authority)
 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(reference_matches)) match WHERE json_extract(match.value,'$.status')=reference_status AND json_extract(match.value,'$.authority')=reference_authority));
-- name: AdminActivityRows :many
WITH ordering AS (SELECT CAST(sqlc.arg(sort_field) AS TEXT) AS sort_field,CAST(sqlc.arg(direction) AS TEXT) AS direction)
SELECT snapshot,status,review_reasons,selection_authority,instrument_name,reference_status,reference_authority FROM admin_activity CROSS JOIN ordering WHERE (sqlc.arg(kind_filter)='' OR kind=sqlc.arg(kind_filter))
 AND (sqlc.arg(state_filter)='' OR sqlc.arg(state_filter)='review' AND review_reasons<>0 OR status=sqlc.arg(state_filter))
 AND (sqlc.arg(search)='' OR wake_contains(search_text,sqlc.arg(search))=1 OR wake_contains(label_sort,coalesce(nullif(sqlc.arg(label_search),''),sqlc.arg(search)))=1
 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(amount_matches)) match WHERE wake_contains(coalesce(json_extract(snapshot,'$.activity.' || json_extract(match.value,'$.role') || '.amount'),'') || ' ' || coalesce(json_extract(snapshot,'$.activity.' || json_extract(match.value,'$.role') || '.currency'),''),json_extract(match.value,'$.search'))=1 AND json_type(snapshot,'$.activity.' || json_extract(match.value,'$.role'))='object')
 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(kind_matches)) match WHERE match.value=admin_activity.kind)
 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(state_matches)) match WHERE json_extract(match.value,'$.lifecycle')=status AND json_extract(match.value,'$.review')=review_reasons AND json_extract(match.value,'$.authority')=selection_authority)
 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(reference_matches)) match WHERE json_extract(match.value,'$.status')=reference_status AND json_extract(match.value,'$.authority')=reference_authority))
 ORDER BY
 CASE ordering.sort_field WHEN 'quantity' THEN quantity IS NULL WHEN 'price' THEN price IS NULL ELSE 0 END ASC,
 CASE ordering.sort_field WHEN 'quantity' THEN quantity_unit WHEN 'price' THEN price_unit ELSE '' END ASC,
 CASE WHEN ordering.sort_field='quantity' AND ordering.direction='asc' THEN quantity END COLLATE wake_decimal ASC,
 CASE WHEN ordering.sort_field='quantity' AND ordering.direction='desc' THEN quantity END COLLATE wake_decimal DESC,
 CASE WHEN ordering.sort_field='price' AND ordering.direction='asc' THEN price END COLLATE wake_decimal ASC,
 CASE WHEN ordering.sort_field='price' AND ordering.direction='desc' THEN price END COLLATE wake_decimal DESC,
 CASE WHEN ordering.direction='asc' THEN CASE ordering.sort_field WHEN 'date' THEN event_date WHEN 'label' THEN label_sort WHEN 'state' THEN status WHEN 'reference' THEN reference_status END END ASC,
 CASE WHEN ordering.direction='desc' THEN CASE ordering.sort_field WHEN 'date' THEN event_date WHEN 'label' THEN label_sort WHEN 'state' THEN status WHEN 'reference' THEN reference_status END END DESC,
 CASE WHEN ordering.sort_field='reference' AND ordering.direction='asc' THEN reference_authority END ASC,
 CASE WHEN ordering.sort_field='reference' AND ordering.direction='desc' THEN reference_authority END DESC,
 id ASC LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);
-- name: AdminInstrumentCount :one
SELECT (SELECT count(*) FROM admin_instruments) AS total,count(*) AS matched FROM admin_instruments WHERE (sqlc.arg(kind_filter)='' OR kind=sqlc.arg(kind_filter))
 AND (sqlc.arg(state_filter)='' OR sqlc.arg(state_filter)='review' AND reference_status IN ('ambiguous','conflict') OR sqlc.arg(state_filter)='gaps' AND reference_status NOT IN ('resolved','ambiguous','conflict') OR reference_status=sqlc.arg(state_filter))
 AND (sqlc.arg(search)='' OR wake_contains(search_text,sqlc.arg(search))=1
 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(reference_matches)) match WHERE json_extract(match.value,'$.status')=reference_status AND json_extract(match.value,'$.authority')=selection_authority));
-- name: AdminInstrumentRows :many
WITH ordering AS (SELECT CAST(sqlc.arg(sort_field) AS TEXT) AS sort_field,CAST(sqlc.arg(direction) AS TEXT) AS direction)
SELECT snapshot,selection_authority FROM admin_instruments CROSS JOIN ordering WHERE (sqlc.arg(kind_filter)='' OR kind=sqlc.arg(kind_filter))
 AND (sqlc.arg(state_filter)='' OR sqlc.arg(state_filter)='review' AND reference_status IN ('ambiguous','conflict') OR sqlc.arg(state_filter)='gaps' AND reference_status NOT IN ('resolved','ambiguous','conflict') OR reference_status=sqlc.arg(state_filter))
 AND (sqlc.arg(search)='' OR wake_contains(search_text,sqlc.arg(search))=1
 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(reference_matches)) match WHERE json_extract(match.value,'$.status')=reference_status AND json_extract(match.value,'$.authority')=selection_authority))
 ORDER BY
 CASE ordering.sort_field WHEN 'name' THEN name='' WHEN 'ticker' THEN ticker='' ELSE 0 END ASC,
 CASE WHEN ordering.direction='asc' THEN CASE ordering.sort_field WHEN 'name' THEN name WHEN 'ticker' THEN ticker WHEN 'kind' THEN kind WHEN 'state' THEN reference_status END END COLLATE NOCASE ASC,
 CASE WHEN ordering.direction='desc' THEN CASE ordering.sort_field WHEN 'name' THEN name WHEN 'ticker' THEN ticker WHEN 'kind' THEN kind WHEN 'state' THEN reference_status END END COLLATE NOCASE DESC,
 CASE WHEN ordering.sort_field='state' AND ordering.direction='asc' THEN selection_authority END ASC,
 CASE WHEN ordering.sort_field='state' AND ordering.direction='desc' THEN selection_authority END DESC,
 id ASC LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);
-- name: AdminCounts :one
SELECT (SELECT count(*) FROM published_resources WHERE kind='activity') AS activity_count,(SELECT count(*) FROM published_resources WHERE kind='instruments') AS instrument_count,(SELECT count(*) FROM connections) AS connection_count;
-- name: AdminReferenceStatus :one
SELECT length(encrypted)>0 AS has_key,failure FROM access.reference_settings WHERE singleton=1;
-- name: AdminSourceCount :one
SELECT count(*) FROM connections WHERE sqlc.arg(search)='' OR wake_contains(name || ' ' || provider,sqlc.arg(search))=1;
-- name: AdminSourceRows :many
SELECT * FROM connections WHERE sqlc.arg(search)='' OR wake_contains(name || ' ' || provider,sqlc.arg(search))=1 ORDER BY name COLLATE NOCASE,id LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: AdminCandidateCount :one
SELECT count(*) FROM (SELECT candidates.authority,candidates.kind,candidates.scope,candidates.value FROM candidates JOIN artifacts ON artifacts.id=candidates.artifact_id JOIN acquisition_receipts r ON r.id=candidates.receipt_id WHERE artifacts.connection_id=? AND r.operation='discover' GROUP BY candidates.authority,candidates.kind,candidates.scope,candidates.value);
-- name: AdminCandidates :many
SELECT artifact_id,authority,kind,scope,value,name,currency FROM (
 SELECT candidates.*,r.sequence AS appearance,row_number() OVER(PARTITION BY candidates.authority,candidates.kind,candidates.scope,candidates.value ORDER BY r.sequence DESC) AS ranking
 FROM candidates JOIN artifacts ON artifacts.id=candidates.artifact_id JOIN acquisition_receipts r ON r.id=candidates.receipt_id WHERE artifacts.connection_id=sqlc.arg(connection_id) AND r.operation='discover'
) WHERE ranking=1 ORDER BY appearance DESC,authority,kind,scope,value LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: AdminReviewCount :one
SELECT (SELECT count(*) FROM admin_review) AS total,count(*) AS matched FROM admin_review WHERE sqlc.arg(search)='' OR wake_contains(search_text || ' ' || kind || ' ' || id,sqlc.arg(search))=1 OR label_key<>'' AND wake_contains(label_key,coalesce(nullif(sqlc.arg(label_search),''),sqlc.arg(search)))=1 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(kind_matches)) match WHERE kind='activity' AND json_extract(activity,'$.family')=match.value);
-- name: AdminReviewRows :many
SELECT CAST(kind AS TEXT) AS kind,CAST(id AS TEXT) AS id,CAST(name AS TEXT) AS name,CAST(artifact AS TEXT) AS artifact,CAST(location AS TEXT) AS location,CAST(status AS TEXT) AS status,CAST(claim_count AS INTEGER) AS claim_count,CAST(activity AS TEXT) AS activity FROM admin_review WHERE sqlc.arg(search)='' OR wake_contains(search_text || ' ' || kind || ' ' || id,sqlc.arg(search))=1 OR label_key<>'' AND wake_contains(label_key,coalesce(nullif(sqlc.arg(label_search),''),sqlc.arg(search)))=1 OR EXISTS(SELECT 1 FROM json_each(sqlc.arg(kind_matches)) match WHERE kind='activity' AND json_extract(activity,'$.family')=match.value) ORDER BY kind,id LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);
-- name: AdminCandidate :one
SELECT c.artifact_id,c.authority,c.kind,c.scope,c.value,c.name,c.currency FROM candidates c JOIN artifacts a ON a.id=c.artifact_id JOIN acquisition_receipts r ON r.id=c.receipt_id WHERE a.connection_id=? AND c.artifact_id=? AND c.authority=? AND c.kind=? AND c.scope=? AND c.value=? AND r.operation='discover' ORDER BY r.sequence DESC LIMIT 1;

-- name: AdminAccessSources :many
SELECT * FROM connections ORDER BY id;
