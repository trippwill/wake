-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- name: Init
INSERT INTO publishing(singleton,generation) VALUES(1,?) ON CONFLICT DO NOTHING;
-- name: State
SELECT m.id,p.generation,coalesce((SELECT max(position) FROM published_changes),0), coalesce((SELECT status<>'complete' FROM recovery_jobs ORDER BY rowid DESC LIMIT 1),0) FROM store_metadata m CROSS JOIN publishing p WHERE m.singleton=1 AND p.singleton=1;
-- name: Rotate
UPDATE publishing SET generation=? WHERE singleton=1;
-- name: ClearJournal
DELETE FROM published_changes;
-- name: Dirty
SELECT kind,id FROM publishing_dirty ORDER BY kind,id;
-- name: ClearDirty
DELETE FROM publishing_dirty;
-- name: Current
SELECT revision,snapshot,explanation FROM published_resources WHERE kind=? AND id=?;
-- name: Save
INSERT INTO published_resources(kind,id,revision,snapshot,explanation) VALUES(?,?,?,?,?) ON CONFLICT(kind,id) DO UPDATE SET revision=excluded.revision,snapshot=excluded.snapshot,explanation=excluded.explanation;
-- name: Append
INSERT INTO published_changes(kind,id,revision,snapshot) VALUES(?,?,?,?);
-- name: Page
SELECT snapshot FROM published_resources WHERE kind=? AND id>? ORDER BY id LIMIT ?;
-- name: Changes
SELECT position,snapshot FROM published_changes WHERE position>? AND position<=? ORDER BY position LIMIT ?;
-- name: AccountRevision
UPDATE accounts SET revision=? WHERE id=? AND revision<?;
-- name: InstrumentRevision
UPDATE instruments SET revision=? WHERE id=? AND revision<?;
-- name: ListingRevision
UPDATE listings SET revision=? WHERE id=? AND revision<?;
-- name: ActivityRevision
UPDATE executions SET revision=? WHERE id=? AND revision<?;
