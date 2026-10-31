-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- name: Init
INSERT INTO publishing(singleton,version) VALUES(1,0) ON CONFLICT DO NOTHING;
-- name: State
SELECT m.id,p.version FROM store_metadata m CROSS JOIN publishing p WHERE m.singleton=1 AND p.singleton=1;
-- name: Dirty
SELECT kind,id FROM publishing_dirty ORDER BY kind,id;
-- name: ClearDirty
DELETE FROM publishing_dirty;
-- name: Current
SELECT revision,snapshot,explanation FROM published_resources WHERE kind=? AND id=?;
-- name: Save
INSERT INTO published_resources(kind,id,revision,snapshot,explanation) VALUES(?,?,?,?,?) ON CONFLICT(kind,id) DO UPDATE SET revision=excluded.revision,snapshot=excluded.snapshot,explanation=excluded.explanation;
-- name: AccountRevision
UPDATE accounts SET revision=? WHERE id=? AND revision<?;
-- name: InstrumentRevision
UPDATE instruments SET revision=? WHERE id=? AND revision<?;
-- name: ListingRevision
UPDATE listings SET revision=? WHERE id=? AND revision<?;
-- name: ActivityRevision
UPDATE executions SET revision=? WHERE id=? AND revision<?;

-- name: Snapshot
SELECT kind,id,snapshot FROM published_resources ORDER BY kind,id;

-- name: Advance
UPDATE publishing SET version=version+1 WHERE singleton=1;
