-- SPDX-FileCopyrightText: 2026 trippwill
-- SPDX-License-Identifier: MPL-2.0

-- name: Dirty
SELECT account_id FROM positions_dirty ORDER BY account_id;
-- name: Current
SELECT revision,data FROM positions_current WHERE account_id=?;
-- name: Revision
SELECT revision FROM positions_current WHERE account_id=?;
-- name: Save
INSERT INTO positions_current(account_id,revision,data) VALUES(?,?,?) ON CONFLICT(account_id) DO UPDATE SET revision=excluded.revision,data=excluded.data;
-- name: ClearDirty
DELETE FROM positions_dirty;
-- name: InterpretationChanged
INSERT OR IGNORE INTO positions_dirty SELECT DISTINCT p.account_id FROM position_sets p JOIN records r ON r.claim_id=p.claim_id WHERE r.artifact_id=?;
-- name: Account
SELECT id FROM accounts WHERE id=?;
-- name: Metadata
SELECT m.id,p.account_id,p.revision FROM store_metadata m CROSS JOIN positions_current p WHERE m.singleton=1 ORDER BY p.account_id;

-- name: Inputs
WITH current_sets AS (
 SELECT p.*,p.rowid AS sequence FROM position_sets p WHERE EXISTS (
  SELECT 1 FROM records r JOIN processing_receipts receipt ON receipt.artifact_id=r.artifact_id AND receipt.version=r.version
  WHERE r.claim_id=p.claim_id AND NOT EXISTS (
   SELECT 1 FROM processing_receipts newer WHERE newer.artifact_id=receipt.artifact_id AND newer.rowid>receipt.rowid
  )
 )
), superseded(reference) AS (
 SELECT ref.value FROM current_sets other JOIN json_each(other.supersedes) ref WHERE other.account_id=?
), live AS (
 SELECT p.* FROM current_sets p WHERE p.account_id=? AND (p.source_reference='' OR p.source_reference NOT IN (SELECT reference FROM superseded))
)
SELECT c.data,processing.location FROM current_sets p JOIN source_claims c ON c.id=p.claim_id JOIN claim_processing processing ON processing.claim_id=c.id AND processing.ordinal=1
WHERE p.account_id=? AND (p.effective_date='' OR p.claim_id IN (SELECT claim_id FROM live WHERE effective_date=(SELECT max(effective_date) FROM live)) OR json_array_length(p.supersedes)>0 OR p.source_reference IN (SELECT reference FROM superseded))
ORDER BY p.sequence;
