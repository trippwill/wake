-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

ALTER TABLE access.configurations ADD COLUMN fixture_label TEXT NOT NULL DEFAULT 'v2';
CREATE TABLE access.fixture_access(value TEXT NOT NULL) STRICT;
