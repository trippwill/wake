-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- Keep the equality predicate on the filtered path so SQLite can seek by kind.
-- Materialize only the requested page before joining live labels and state.
WITH all_page AS (
 SELECT id,CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.date') AS TEXT) END AS event_date
 FROM published_resources INDEXED BY activity_publication_date
 WHERE kind='activity' AND ?1=''
 ORDER BY event_date {{direction}},id ASC LIMIT ?2 OFFSET ?3
), kind_page AS (
 SELECT id,CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.date') AS TEXT) END AS event_date
 FROM published_resources INDEXED BY activity_publication_family_date
 WHERE kind='activity' AND ?1<>''
 AND CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.family') AS TEXT) END=?1
 ORDER BY event_date {{direction}},id ASC LIMIT ?2 OFFSET ?3
), page AS (
 SELECT * FROM all_page UNION ALL SELECT * FROM kind_page
), counts AS (
 SELECT (SELECT count(*) FROM published_resources WHERE kind='activity') AS total,
        (SELECT count(*) FROM published_resources WHERE kind='activity'
         AND CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.family') AS TEXT) END=?1) AS family_total
)
-- A missing index key indicates an invalid snapshot. Use the ordinary read
-- path to retain its error and filtering behavior.
SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM published_resources INDEXED BY activity_publication_date WHERE kind='activity'
        AND CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.date') AS TEXT) END IS NULL
       ) AND NOT EXISTS (
        SELECT 1 FROM published_resources INDEXED BY activity_publication_family_date WHERE kind='activity'
        AND CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.family') AS TEXT) END IS NULL
       ) THEN total END,
       CASE WHEN ?1='' THEN total ELSE family_total END,
       p.snapshot,coalesce(e.status,''),coalesce(e.review_reasons,0),coalesce(e.selection_authority,''),
       coalesce(nullif(i.name,''),i.ticker,''),coalesce(display.ticker,''),coalesce(i.reference_status,''),
       coalesce(i.selection_authority,'automatic'),coalesce(account.name,'')
FROM counts
LEFT JOIN page ON true
LEFT JOIN published_resources p ON p.kind='activity' AND p.id=page.id
LEFT JOIN executions e ON e.id=page.id
LEFT JOIN accounts account ON account.id=e.account_id
LEFT JOIN instruments i ON i.id=e.instrument_id
LEFT JOIN instruments display ON display.id=json_extract(p.snapshot,'$.activity.instrument_id')
ORDER BY page.event_date {{direction}},page.id ASC;
