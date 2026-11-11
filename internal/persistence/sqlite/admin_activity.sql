-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- Read the indexed publication fields, then share the filtered IDs and sort keys
-- between the count and page. Fetch original snapshots only for the selected IDs.
-- Bind Kind through a one-row relation so changing the selection can reuse the
-- prepared query while still seeking the family index.
WITH selected_kind(kind) AS MATERIALIZED (VALUES (?2)), ordering AS (
 SELECT CAST(?11 AS TEXT) AS sort_field,
        CAST(?12 AS TEXT) AS direction,
        CAST(?5 AS TEXT) AS activity_text,
        wake_fold(?4) AS needle,
        instr(?4,' ')>0 AS spans_fields,
        json_array_length(?8)>0 AS has_kinds,
        json_array_length(?9)>0 AS has_states,
        json_array_length(?10)>0 AS has_references,
        wake_fold(json_extract(?5,'$.unknown')) AS unknown_text,
        CASE WHEN json_type(?5,'$.lifecycle_separator')='text' THEN wake_fold(json_extract(?5,'$.lifecycle_separator')) END AS lifecycle_separator,
        CASE WHEN json_type(?5,'$.movement_separator')='text' THEN wake_fold(json_extract(?5,'$.movement_separator')) END AS movement_separator,
        wake_fold(json_extract(?5,'$.transfer_prefix')) AS transfer_prefix
), labels AS MATERIALIZED (
 SELECT id,name,ticker,reference_status,selection_authority,
        wake_fold(coalesce(nullif(name,''),ticker,'')) AS folded_name,
        wake_fold(ticker) AS folded_ticker,
        wake_fold(coalesce(nullif(name,''),ticker,'') || ' ' || coalesce(ticker,'')) AS search_prefix
 FROM instruments
), indexed(id,family,instrument_id,event_date,price_value,price_group,search_suffix,movement_search,action,cash_category,cash_description,transfer_direction,snapshot) AS (
 SELECT id,CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.family') AS TEXT) END,
        {{activity_search_fields}},snapshot
 FROM published_resources INDEXED BY activity_publication_search_v1
 WHERE kind='activity' AND ?2=''
 UNION ALL
 SELECT id,CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.family') AS TEXT) END,
        {{activity_search_fields}},snapshot
 FROM selected_kind CROSS JOIN published_resources INDEXED BY activity_publication_search_v1
 WHERE published_resources.kind='activity' AND selected_kind.kind<>''
   AND CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.family') AS TEXT) END=selected_kind.kind
 UNION ALL
 SELECT id,CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.family') AS TEXT) END,
        {{activity_search_fields}},snapshot
 FROM published_resources INDEXED BY activity_publication_search_v1
 WHERE kind='activity' AND ?2<>'' AND CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.family') AS TEXT) END IS NULL
), candidates AS (
 SELECT p.id,coalesce(e.kind,p.family) AS activity_kind,
        e.status,e.review_reasons,e.selection_authority,
        coalesce(e.instrument_id,p.instrument_id) AS instrument_id,
        p.instrument_id AS published_instrument_id,jsonb(CAST(p.snapshot AS TEXT)) AS document,
        p.event_date,p.price_value,p.price_group,
        p.search_suffix,p.movement_search,p.action,p.cash_category,p.cash_description,p.transfer_direction
 FROM indexed p LEFT JOIN executions e ON e.id=p.id
   AND (p.family IS NULL OR ?1<>'' OR ?3<>'' OR json_array_length(?9)>0 OR ?11='state')
 WHERE (CAST(?1 AS TEXT)='' OR e.account_id=?1)
   AND (?2='' OR coalesce(e.kind,p.family)=?2)
   AND CASE WHEN p.family IS NULL THEN jsonb(CAST(p.snapshot AS TEXT)) IS NOT NULL ELSE true END
   AND (?3='' OR ?3='review' AND e.review_reasons<>0 OR e.status=?3)
), display_activity AS (
 SELECT e.id,e.document,e.activity_kind AS kind,e.status,e.review_reasons,e.selection_authority,
        e.event_date,e.price_value,e.price_group,e.search_suffix,e.movement_search,
        e.action,e.cash_category,e.cash_description,e.transfer_direction,
        coalesce(i.folded_name,'') AS folded_name,
        coalesce(CASE WHEN e.instrument_id=e.published_instrument_id THEN i.folded_ticker
         ELSE (SELECT wake_fold(ticker) FROM instruments WHERE id=e.published_instrument_id) END,'') AS folded_ticker,
        coalesce(CASE WHEN e.instrument_id=e.published_instrument_id THEN i.ticker
         ELSE (SELECT ticker FROM instruments WHERE id=e.published_instrument_id) END,'') AS instrument_ticker,
 CAST(coalesce(nullif(i.name,''),i.ticker,'') AS TEXT) AS instrument_name,
 CAST(coalesce(i.reference_status,'') AS TEXT) AS reference_status,
 CAST(coalesce(i.selection_authority,'automatic') AS TEXT) AS reference_authority,
 CAST(coalesce(json_extract(e.document,'$.activity.quantity.value'),json_extract(e.document,'$.activity.sent.amount')) AS TEXT) AS quantity,
 CAST(coalesce(json_extract(e.document,'$.activity.quantity.unit'),json_extract(e.document,'$.activity.sent.currency'),'') AS TEXT) AS quantity_unit,
 CAST(coalesce(json_extract(e.document,'$.activity.price.amount'),json_extract(e.document,'$.activity.rate.amount')) AS TEXT) AS price,
 CAST(json_extract(e.document,'$.activity.reported_realized_pnl.amount.amount') AS TEXT) AS realized,
 CAST(coalesce(json_extract(e.document,'$.activity.reported_realized_pnl.amount.currency'),'') AS TEXT) AS realized_currency,
 CAST(lower(CASE WHEN e.activity_kind='conversion' THEN e.activity_kind || ' ' || coalesce(json_extract(e.document,'$.activity.sent.currency'),'') || ' ' || coalesce(json_extract(e.document,'$.activity.received.currency'),'') ELSE coalesce(e.action,'') || ' ' || coalesce(i.folded_name,'') END) AS TEXT) AS label_sort,
 coalesce(i.search_prefix,' ') AS search_prefix
 FROM candidates e
 LEFT JOIN labels i ON i.id=e.instrument_id
), matching AS MATERIALIZED (
 SELECT id,
  CASE ordering.sort_field WHEN 'quantity' THEN quantity IS NULL WHEN 'price' THEN price_value IS NULL WHEN 'amount' THEN json_extract(document,'$.activity.amount.amount') IS NULL WHEN 'realized' THEN realized IS NULL ELSE 0 END AS missing,
  CASE ordering.sort_field WHEN 'quantity' THEN quantity_unit WHEN 'price' THEN price_group WHEN 'amount' THEN json_extract(document,'$.activity.amount.currency') WHEN 'realized' THEN realized_currency ELSE '' END AS unit,
  CASE WHEN ordering.direction='asc' THEN CASE ordering.sort_field
   WHEN 'quantity' THEN quantity
   WHEN 'price' THEN price_value
   WHEN 'amount' THEN json_extract(document,'$.activity.amount.amount')
   WHEN 'realized' THEN realized
  END END AS decimal_asc,
  CASE WHEN ordering.direction='desc' THEN CASE ordering.sort_field
   WHEN 'quantity' THEN quantity
   WHEN 'price' THEN price_value
   WHEN 'amount' THEN json_extract(document,'$.activity.amount.amount')
   WHEN 'realized' THEN realized
  END END AS decimal_desc,
  CASE WHEN ordering.direction='asc' THEN CASE ordering.sort_field WHEN 'date' THEN event_date WHEN 'label' THEN lower(CASE WHEN kind='cash' THEN replace(json_extract(document,'$.activity.category'),'-',' ') || coalesce(nullif(json_extract(ordering.activity_text,'$.movement_separator'),''),char(32,183,32)) || coalesce(nullif(json_extract(document,'$.activity.description'),''),nullif(json_extract(ordering.activity_text,'$.unknown'),''),'Unknown') WHEN kind='security-transfer' THEN coalesce(nullif(json_extract(ordering.activity_text,'$.transfer_prefix'),''),'Transfer ') || json_extract(document,'$.activity.direction') || coalesce(nullif(json_extract(ordering.activity_text,'$.movement_separator'),''),char(32,183,32)) || coalesce(nullif(instrument_ticker,''),nullif(instrument_name,''),nullif(json_extract(ordering.activity_text,'$.unknown'),''),'Unknown') WHEN kind IN ('trade','lifecycle') THEN coalesce(json_extract(document,'$.activity.side'),json_extract(document,'$.activity.lifecycle_kind')) || CASE WHEN kind='lifecycle' THEN coalesce(nullif(json_extract(ordering.activity_text,'$.lifecycle_separator'),''),char(32,183,32)) ELSE ' ' END || coalesce(nullif(instrument_ticker,''),nullif(instrument_name,''),nullif(json_extract(ordering.activity_text,'$.unknown'),''),'Unknown') ELSE label_sort END) WHEN 'state' THEN status WHEN 'reference' THEN reference_status END END AS text_asc,
  CASE WHEN ordering.direction='desc' THEN CASE ordering.sort_field WHEN 'date' THEN event_date WHEN 'label' THEN lower(CASE WHEN kind='cash' THEN replace(json_extract(document,'$.activity.category'),'-',' ') || coalesce(nullif(json_extract(ordering.activity_text,'$.movement_separator'),''),char(32,183,32)) || coalesce(nullif(json_extract(document,'$.activity.description'),''),nullif(json_extract(ordering.activity_text,'$.unknown'),''),'Unknown') WHEN kind='security-transfer' THEN coalesce(nullif(json_extract(ordering.activity_text,'$.transfer_prefix'),''),'Transfer ') || json_extract(document,'$.activity.direction') || coalesce(nullif(json_extract(ordering.activity_text,'$.movement_separator'),''),char(32,183,32)) || coalesce(nullif(instrument_ticker,''),nullif(instrument_name,''),nullif(json_extract(ordering.activity_text,'$.unknown'),''),'Unknown') WHEN kind IN ('trade','lifecycle') THEN coalesce(json_extract(document,'$.activity.side'),json_extract(document,'$.activity.lifecycle_kind')) || CASE WHEN kind='lifecycle' THEN coalesce(nullif(json_extract(ordering.activity_text,'$.lifecycle_separator'),''),char(32,183,32)) ELSE ' ' END || coalesce(nullif(instrument_ticker,''),nullif(instrument_name,''),nullif(json_extract(ordering.activity_text,'$.unknown'),''),'Unknown') ELSE label_sort END) WHEN 'state' THEN status WHEN 'reference' THEN reference_status END END AS text_desc,
  CASE WHEN ordering.sort_field='reference' AND ordering.direction='asc' THEN reference_authority END AS authority_asc,
  CASE WHEN ordering.sort_field='reference' AND ordering.direction='desc' THEN reference_authority END AS authority_desc
 FROM display_activity CROSS JOIN ordering
 WHERE (?4=''
 OR instr(search_suffix,ordering.needle)>0
 OR instr(movement_search,ordering.needle)>0
 OR CASE WHEN ordering.spans_fields
    THEN instr(search_prefix || coalesce(search_suffix,''),ordering.needle)>0 OR instr(label_sort,ordering.needle)>0
    ELSE instr(search_prefix,ordering.needle)>0 END
 OR kind='security-transfer' AND ordering.transfer_prefix<>'' AND instr(
    ordering.transfer_prefix || coalesce(transfer_direction,'') || ordering.movement_separator ||
    coalesce(nullif(folded_ticker,''),nullif(folded_name,''),ordering.unknown_text),ordering.needle)>0
 OR kind='cash' AND ordering.movement_separator<>'' AND instr(
    cash_category || ordering.movement_separator || coalesce(cash_description,ordering.unknown_text),ordering.needle)>0
 OR ?6=1 AND (kind<>'cash' AND realized IS NULL OR kind='cash' AND coalesce(json_extract(document,'$.activity.description'),'')='' OR (kind NOT IN ('conversion','cash') AND instrument_name='') OR kind='trade' AND (quantity IS NULL OR price IS NULL) OR kind='conversion' AND (price IS NULL OR json_extract(document,'$.activity.sent.amount') IS NULL OR json_extract(document,'$.activity.received.amount') IS NULL OR coalesce(json_extract(document,'$.activity.sent.currency'),'')='' OR coalesce(json_extract(document,'$.activity.received.currency'),'')='' OR json_extract(document,'$.activity.rate_base') IS NULL))
 OR ?7=1
 OR kind IN ('trade','lifecycle') AND ordering.unknown_text<>'' AND instr(
    action || CASE WHEN kind='lifecycle' THEN ordering.lifecycle_separator ELSE ' ' END ||
    coalesce(nullif(folded_ticker,''),nullif(folded_name,''),ordering.unknown_text),ordering.needle)>0
 OR kind='conversion' AND json_extract(?5,'$.pair_separator')<>'' AND (wake_contains(json_extract(?5,'$.conversion_prefix') || coalesce(nullif(json_extract(document,'$.activity.sent.currency'),''),json_extract(?5,'$.unknown')) || json_extract(?5,'$.pair_separator') || coalesce(nullif(json_extract(document,'$.activity.received.currency'),''),json_extract(?5,'$.unknown')),?4)=1 OR wake_contains(coalesce(json_extract(document,'$.activity.sent.amount'),json_extract(?5,'$.unknown')) || ' ' || coalesce(nullif(json_extract(document,'$.activity.sent.currency'),''),json_extract(?5,'$.unknown')) || ' ' || json_extract(?5,'$.sent') || json_extract(?5,'$.amount_separator') || coalesce(json_extract(document,'$.activity.received.amount'),json_extract(?5,'$.unknown')) || ' ' || coalesce(nullif(json_extract(document,'$.activity.received.currency'),''),json_extract(?5,'$.unknown')) || ' ' || json_extract(?5,'$.received'),?4)=1)
 OR ordering.has_kinds AND EXISTS(SELECT 1 FROM json_each(?8) match WHERE match.value=display_activity.kind)
 OR ordering.has_states AND EXISTS(SELECT 1 FROM json_each(?9) match WHERE json_extract(match.value,'$.lifecycle')=status AND json_extract(match.value,'$.review')=review_reasons AND json_extract(match.value,'$.authority')=selection_authority)
 OR ordering.has_references AND EXISTS(SELECT 1 FROM json_each(?10) match WHERE json_extract(match.value,'$.status')=reference_status AND json_extract(match.value,'$.authority')=reference_authority))
), page AS (
 SELECT * FROM matching
 ORDER BY missing ASC, unit ASC, decimal_asc COLLATE wake_decimal ASC, decimal_desc COLLATE wake_decimal DESC,
          text_asc ASC, text_desc DESC, authority_asc ASC, authority_desc DESC, id ASC
 LIMIT ?13 OFFSET ?14
)
SELECT (SELECT count(*) FROM published_resources WHERE kind='activity'), (SELECT count(*) FROM matching),
       p.snapshot,coalesce(e.status,''),coalesce(e.review_reasons,0),coalesce(e.selection_authority,''),
       coalesce(nullif(i.name,''),i.ticker,''),coalesce(display.ticker,''),coalesce(i.reference_status,''),
       coalesce(i.selection_authority,'automatic'),coalesce(account.name,'')
FROM (SELECT 1) counts
LEFT JOIN page ON true
LEFT JOIN published_resources p ON p.kind='activity' AND p.id=page.id
LEFT JOIN executions e ON e.id=page.id
LEFT JOIN accounts account ON account.id=e.account_id
LEFT JOIN instruments i ON i.id=e.instrument_id
LEFT JOIN instruments display ON display.id=json_extract(p.snapshot,'$.activity.instrument_id')
ORDER BY missing ASC, unit ASC, decimal_asc COLLATE wake_decimal ASC, decimal_desc COLLATE wake_decimal DESC,
          text_asc ASC, text_desc DESC, authority_asc ASC, authority_desc DESC, page.id ASC;
