-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

WITH current AS MATERIALIZED (
 SELECT revision, jsonb(CAST(data AS TEXT)) AS data FROM positions_current WHERE account_id=?1
), reported AS (
 SELECT p.key AS ordinal, p.value AS position,
  json_extract(p.value,'$.instrument_id') AS id,
  coalesce(nullif(i.name,''),nullif(i.ticker,''),i.id) AS name,
  i.ticker,i.kind,
  json_extract(p.value,'$.side') AS side,
  json_extract(p.value,'$.quantity.unit') AS unit,
  json_extract(p.value,'$.quantity.value') AS quantity,
  json_extract(p.value,'$.unit_cost_basis.amount') AS cost,
  json_extract(p.value,'$.unit_cost_basis.currency') AS currency,
  json_extract(p.value,'$.unit_cost_basis.quote') AS quote
 FROM current c JOIN jsonb_each(c.data,'$.positions') p
 JOIN instruments i ON i.id=json_extract(p.value,'$.instrument_id')
 WHERE json_extract(c.data,'$.status')='available'
), matching AS (
 SELECT * FROM reported
 WHERE (?2='' OR wake_contains(name || ' ' || ticker,?2))
 AND (?3='' OR kind=?3) AND (?4='' OR side=?4)
), counts AS (
 SELECT (SELECT count(*) FROM reported) AS total,count(*) AS matched FROM matching
), ordered AS (
 SELECT *,row_number() OVER (ORDER BY
 CASE WHEN ?5='name' AND ?6='asc' THEN coalesce(nullif(ticker,''),name) END COLLATE NOCASE ASC,
 CASE WHEN ?5='name' AND ?6='desc' THEN coalesce(nullif(ticker,''),name) END COLLATE NOCASE DESC,
 CASE WHEN ?5='side' AND ?6='asc' THEN side END ASC,
 CASE WHEN ?5='side' AND ?6='desc' THEN side END DESC,
 CASE WHEN ?5='quantity' THEN unit END ASC,
 CASE WHEN ?5='quantity' AND ?6='asc' THEN quantity END COLLATE wake_decimal ASC,
 CASE WHEN ?5='quantity' AND ?6='desc' THEN quantity END COLLATE wake_decimal DESC,
 CASE WHEN ?5='cost' THEN cost IS NULL END ASC,
 CASE WHEN ?5='cost' THEN currency END ASC,
 CASE WHEN ?5='cost' THEN quote END ASC,
 CASE WHEN ?5='cost' AND ?6='asc' THEN cost END COLLATE wake_decimal ASC,
 CASE WHEN ?5='cost' AND ?6='desc' THEN cost END COLLATE wake_decimal DESC,
 coalesce(nullif(ticker,''),name) COLLATE NOCASE ASC,id ASC,ordinal ASC) AS position_order
 FROM matching
), page AS (
 SELECT * FROM ordered ORDER BY position_order LIMIT ?7 OFFSET ?8
)
SELECT c.revision,json_extract(c.data,'$.status'),json_extract(c.data,'$.as_of'),
 json_extract(c.data,'$.reason'),counts.total,counts.matched,
 page.ordinal,page.name,page.ticker,page.kind,json(page.position)
FROM current c CROSS JOIN counts LEFT JOIN page ON true
ORDER BY page.position_order;
