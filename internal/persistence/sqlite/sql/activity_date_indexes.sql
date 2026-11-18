-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- Publication owns family and date. Keep malformed data readable by the
-- regular query, which reports its error without affecting other workspaces.
CREATE INDEX IF NOT EXISTS activity_publication_date ON published_resources(
 CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.date') AS TEXT) END DESC,
 id ASC
) WHERE kind='activity';

CREATE INDEX IF NOT EXISTS activity_publication_family_date ON published_resources(
 CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.family') AS TEXT) END,
 CASE WHEN json_valid(CAST(snapshot AS TEXT)) THEN CAST(json_extract(CAST(snapshot AS TEXT),'$.activity.date') AS TEXT) END DESC,
 id ASC
) WHERE kind='activity';
