-- name: CountTables :one
SELECT count(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%';
-- name: ReadFormat :one
SELECT version FROM wake_format;
-- name: SetFormat :exec
INSERT INTO wake_format(version) VALUES (?);
