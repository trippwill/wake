-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

ALTER TABLE main.profile ADD COLUMN fixture_label TEXT NOT NULL DEFAULT 'v2';
CREATE TABLE main.fixture_warehouse(value TEXT NOT NULL) STRICT;
