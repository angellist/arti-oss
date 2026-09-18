-- name: GetMapEntry :one
SELECT * FROM artifact_map_entry WHERE map_id = $1 AND key = $2;

-- ListMapEntries filters by prefix as a HALF-OPEN RANGE on key rather than a
-- LIKE, so the (map_id, key) primary-key btree is an index range scan
-- whatever the planner thinks of the pattern. The caller computes the
-- successor bound; an empty prefix passes '' and the successor is NULL,
-- which the predicate treats as unbounded. The cursor is EXCLUSIVE (key >
-- after_key) so a page never repeats its predecessor's last key; the first
-- page passes '' , which is below every non-empty key.
-- name: ListMapEntries :many
SELECT * FROM artifact_map_entry
WHERE map_id = @map_id::uuid
  AND key >= @prefix::text
  AND key > @after_key::text
  AND (sqlc.narg('prefix_end')::text IS NULL OR key < sqlc.narg('prefix_end')::text)
ORDER BY key
LIMIT @lim::int;

-- name: CountMapEntries :one
SELECT COUNT(*)::bigint AS n, COALESCE(SUM(size_bytes), 0)::bigint AS bytes
FROM artifact_map_entry WHERE map_id = $1;

-- PutMapEntry is the unguarded upsert: last write wins. One statement, so no
-- read-then-write window exists between replicas.
-- name: PutMapEntry :one
INSERT INTO artifact_map_entry (map_id, key, value, size_bytes, updated_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (map_id, key) DO UPDATE
SET value = EXCLUDED.value, size_bytes = EXCLUDED.size_bytes,
    updated_by = EXCLUDED.updated_by, updated_at = now(),
    rev = artifact_map_entry.rev + 1
RETURNING *;

-- PutMapEntryIfAbsent inserts only when the key is free. A zero-row result
-- means the key was taken; the caller then reads the incumbent to report it,
-- which is safe because the insert already lost.
-- name: PutMapEntryIfAbsent :one
INSERT INTO artifact_map_entry (map_id, key, value, size_bytes, updated_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (map_id, key) DO NOTHING
RETURNING *;

-- PutMapEntryIfRev is a plain conditional UPDATE, deliberately NOT an upsert.
-- An upsert with `ON CONFLICT ... DO UPDATE ... WHERE rev = $n` fires its
-- INSERT arm when no row exists, so a compare-and-set against a key that was
-- concurrently deleted would silently succeed and resurrect it at rev 1. A
-- CAS must fail when its subject is gone, so zero rows is the answer for both
-- "rev moved" and "key gone" — the caller asked about a specific revision and
-- that revision is not there either way.
-- name: PutMapEntryIfRev :one
UPDATE artifact_map_entry
SET value = $3, size_bytes = $4, updated_by = $5, updated_at = now(), rev = rev + 1
WHERE map_id = $1 AND key = $2 AND rev = @if_rev::bigint
RETURNING *;

-- name: DeleteMapEntries :execrows
DELETE FROM artifact_map_entry WHERE map_id = $1 AND key = ANY(@keys::text[]);

-- name: DeleteAllMapEntries :execrows
DELETE FROM artifact_map_entry WHERE map_id = $1;
