-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

CREATE INDEX IF NOT EXISTS activity_publication_search_v1 ON published_resources(
 CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.family') AS TEXT) END,
 id,
 {{activity_search_fields}}
) WHERE kind='activity';
