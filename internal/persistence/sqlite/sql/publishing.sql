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

-- name: ResetJournal
DELETE FROM publication_changes;
DELETE FROM publication_versions;
-- name: RecordBase
INSERT INTO publication_versions(sequence,token,change_count) VALUES(?,?,0) ON CONFLICT(sequence) DO NOTHING;
-- name: RecordVersion
INSERT INTO publication_versions(sequence,token,change_count) VALUES(?,?,?);
-- name: RecordChanges
INSERT INTO publication_changes(sequence,kind,id)
SELECT ?,value->>'kind',value->>'id' FROM json_each(?);
-- name: PruneVersions
DELETE FROM publication_versions WHERE sequence < (
 SELECT min(sequence) FROM (
  SELECT sequence,coalesce(sum(change_count) OVER (
   ORDER BY sequence DESC ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING
  ),0) AS later_changes
  FROM publication_versions ORDER BY sequence DESC LIMIT ?
 ) WHERE later_changes <= ?
);
-- name: PruneBaseChanges
DELETE FROM publication_changes WHERE sequence <= (SELECT min(sequence) FROM publication_versions);
-- name: SnapshotBase
SELECT sequence FROM publication_versions WHERE token=?;
-- name: SnapshotChanges
WITH changed AS (
 SELECT DISTINCT kind,id FROM publication_changes WHERE sequence>? AND sequence<=? LIMIT ?
)
SELECT changed.kind,changed.id,p.snapshot FROM changed
LEFT JOIN published_resources p ON p.kind=changed.kind AND p.id=changed.id
ORDER BY changed.kind,changed.id;

-- name: RetireResource
DELETE FROM published_resources WHERE kind=? AND id=?;
