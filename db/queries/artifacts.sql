-- name: InsertArtifact :one
INSERT INTO artifacts (
    artifact_id, artifact_type, named_slug, version,
    title, description, content_type,
    inline_content, blob_ref, sha256, size_bytes,
    creator, scope, scopes, labels, metadata, allowed_access, allowed_write
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
)
RETURNING *;

-- name: GetArtifact :one
SELECT * FROM artifacts WHERE artifact_id = $1;

-- name: GetArtifactBySlugVersion :one
SELECT * FROM artifacts
WHERE named_slug = $1 AND version = $2 AND deleted_at IS NULL;

-- name: GetLatestArtifactBySlug :one
SELECT * FROM artifacts
WHERE named_slug = $1 AND deleted_at IS NULL
ORDER BY version DESC
LIMIT 1;

-- name: GetArtifactBySHA :one
SELECT * FROM artifacts WHERE sha256 = $1 AND deleted_at IS NULL LIMIT 1;

-- name: NextVersionForSlug :one
SELECT COALESCE(MAX(version), 0)::int + 1 AS next_version
FROM artifacts WHERE named_slug = $1;

-- ListArtifacts / CountArtifacts / SearchArtifacts are intentionally
-- omitted: pgstore.Store.List builds them dynamically (sort keys + slug
-- filter + field:value search) directly via pgx so the ORDER BY column
-- stays first-class in the plan instead of being squeezed through a
-- CASE-WHEN.

-- name: ListArtifactVersions :many
SELECT * FROM artifacts
WHERE named_slug = $1 AND deleted_at IS NULL
ORDER BY version DESC;

-- GetArtifactsByIDs — batch fetch by a set of IDs, used by the OpenSearch
-- search path to hydrate full rows from Postgres after BM25 ranking. Ordering
-- is not significant (the caller re-orders by the OpenSearch hit order).
-- Defined as a sqlc query rather than a hand-written `SELECT *` + RowToStructByPos
-- so the column list stays in sync with the sqlc.Artifact struct via codegen.
--
-- name: GetArtifactsByIDs :many
SELECT * FROM artifacts WHERE artifact_id = ANY(sqlc.arg(ids)::uuid[]);

-- name: SoftDeleteArtifact :execrows
UPDATE artifacts SET deleted_at = now()
WHERE artifact_id = $1 AND deleted_at IS NULL;

-- name: SoftDeleteArtifactBySlug :execrows
UPDATE artifacts SET deleted_at = now()
WHERE named_slug = $1 AND deleted_at IS NULL;

-- name: UnarchiveArtifact :execrows
UPDATE artifacts SET deleted_at = NULL
WHERE artifact_id = $1 AND deleted_at IS NOT NULL;

-- name: HardDeleteArtifact :execrows
DELETE FROM artifacts WHERE artifact_id = $1;

-- name: UpdateArtifactTitle :execrows
UPDATE artifacts SET title = $2, modified_at = now()
WHERE artifact_id = $1;

-- name: UpdateArtifactDescription :execrows
UPDATE artifacts SET description = $2, modified_at = now()
WHERE artifact_id = $1;

-- name: UpdateArtifactLabels :execrows
UPDATE artifacts SET labels = $2, modified_at = now()
WHERE artifact_id = $1;

-- name: UpdateArtifactScopes :execrows
UPDATE artifacts SET scopes = $2, scope = $3, modified_at = now()
WHERE artifact_id = $1;

-- name: UpdateArtifactAccess :execrows
UPDATE artifacts SET allowed_access = $2, allowed_write = $3, modified_at = now()
WHERE artifact_id = $1;

-- GetLatestArtifactBySlugForCaller — `/s/foo` (no version pinned) for a
-- non-admin caller. Returns the freshest non-deleted version where the
-- caller is the creator, matches one of the allowed_access patterns, OR
-- the row references one of the caller's group tokens (caller_groups, an
-- array-overlap check). See pgstore.buildWhere for the matching semantics;
-- this query mirrors the exact same clause so single-row lookups stay
-- consistent with list filtering.
--
-- name: GetLatestArtifactBySlugForCaller :one
SELECT * FROM artifacts
WHERE named_slug = sqlc.arg(named_slug)
  AND deleted_at IS NULL
  AND (
    creator = sqlc.arg(caller)
    OR allowed_access && sqlc.arg(caller_groups)::text[]
    OR EXISTS (
      SELECT 1 FROM unnest(allowed_access) p
      WHERE p = '*'
         OR lower(p) = lower(sqlc.arg(caller))
         OR lower(sqlc.arg(caller)) LIKE lower(translate(replace(replace(p, '%', '\%'), '_', '\_'), '*?', '%_')) ESCAPE '\'
    )
  )
ORDER BY version DESC
LIMIT 1;
