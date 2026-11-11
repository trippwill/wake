-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.instrument_id') AS TEXT) END,
CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.date') AS TEXT) END,
CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.price.amount'),json_extract(CAST(snapshot AS TEXT),'$.activity.rate.amount')) AS TEXT) END,
CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN coalesce(nullif(CAST(coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.price.currency'),json_extract(CAST(snapshot AS TEXT),'$.activity.rate.currency'),'') || CASE WHEN coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.quotation_basis'),json_extract(CAST(snapshot AS TEXT),'$.activity.rate_base'),'') = '' THEN '' ELSE '/' || coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.quotation_basis'),json_extract(CAST(snapshot AS TEXT),'$.activity.rate_base'),'') END AS TEXT),''),json_extract(CAST(snapshot AS TEXT),'$.activity.price.currency'),'') END,
CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN
 wake_fold(' ' || coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.family'),'') || ' ' || id || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.date'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.at'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.side'),json_extract(CAST(snapshot AS TEXT),'$.activity.lifecycle_kind'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.quantity.value'),json_extract(CAST(snapshot AS TEXT),'$.activity.sent.amount'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.quantity.unit'),json_extract(CAST(snapshot AS TEXT),'$.activity.sent.currency'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.price.amount'),json_extract(CAST(snapshot AS TEXT),'$.activity.rate.amount'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.price.currency'),json_extract(CAST(snapshot AS TEXT),'$.activity.rate.currency'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.sent.currency'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.received.currency'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.received.amount'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.received.currency'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.reported_realized_pnl.amount.amount'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.reported_realized_pnl.amount.currency'),'')) END,
CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN
 wake_fold(coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.direction'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.method'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.price.amount'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.price.currency'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.category'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.description'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.amount.amount'),'') || ' ' ||
  coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.amount.currency'),'')) END,
CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CASE WHEN coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.side'),json_extract(CAST(snapshot AS TEXT),'$.activity.lifecycle_kind')) IS NOT NULL THEN wake_fold(coalesce(json_extract(CAST(snapshot AS TEXT),'$.activity.side'),json_extract(CAST(snapshot AS TEXT),'$.activity.lifecycle_kind'))) END END,
CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CASE WHEN json_extract(CAST(snapshot AS TEXT),'$.activity.category') IS NOT NULL THEN wake_fold(replace(json_extract(CAST(snapshot AS TEXT),'$.activity.category'),'-',' ')) END END,
CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CASE WHEN nullif(json_extract(CAST(snapshot AS TEXT),'$.activity.description'),'') IS NOT NULL THEN wake_fold(json_extract(CAST(snapshot AS TEXT),'$.activity.description')) END END,
CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CASE WHEN json_extract(CAST(snapshot AS TEXT),'$.activity.direction') IS NOT NULL THEN wake_fold(json_extract(CAST(snapshot AS TEXT),'$.activity.direction')) END END
