-- name: InsertArtifact :one
INSERT INTO artifacts (
    artifact_id, artifact_type, named_slug, version,
    title, description, content_type,
    inline_content, blob_ref, sha256, size_bytes,
    creator, scope, scopes, labels, metadata, allowed_access, allowed_write,
    comments_enabled
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19
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

-- GetLatestArtifactBySlugAnyState — the newest version of a slug REGARDLESS of
-- archival. Every other slug lookup filters `deleted_at IS NULL`, which is
-- right for reads: an archived doc is gone from the catalog. But version
-- numbering doesn't filter (see NextVersionForSlug), so a slug whose versions
-- are ALL archived can still be re-versioned — and the new version has to
-- inherit the document's settings from somewhere. Used only for that:
-- carrying comments_enabled forward. NOT an access-control read.
--
-- name: GetLatestArtifactBySlugAnyState :one
SELECT * FROM artifacts
WHERE named_slug = $1
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

-- UpdateArtifactAccessBySlug — the slug-wide ACL chokepoint (DD-0055). Access
-- is a property of the DOCUMENT, so every version — archived included — gets
-- the same pair; excluding archived rows would let an unarchive resurrect a
-- stale ACL. The change predicate makes an identical resend report zero rows,
-- so callers can skip the N-version reindex in the steady state. Attachments
-- are structurally slugless (applyAttachmentInvariants); the type guard is
-- defensive only. modified_at is deliberately NOT bumped — this is a per-doc
-- setting, not an edit, and bumping N rows would reorder the catalog (same
-- rule as UpdateArtifactCommentsEnabledBySlug above).
-- name: UpdateArtifactAccessBySlug :execrows
UPDATE artifacts SET allowed_access = $2, allowed_write = $3
WHERE named_slug = $1
  AND artifact_type <> 'ATTACHMENT'
  AND (allowed_access IS DISTINCT FROM $2 OR allowed_write IS DISTINCT FROM $3);

-- UpdateArtifactCommentsEnabled / …BySlug — the per-doc comment switch. The
-- by-slug form is what the API normally calls (including soft-deleted
-- versions, so an unarchive doesn't resurrect commenting the owner turned
-- off); the by-id form covers slugless artifacts, which have no lineage.
-- modified_at is deliberately NOT bumped: this is a per-doc setting, not an
-- edit to the document, and touching it would reorder the catalog.
--
-- name: UpdateArtifactCommentsEnabled :execrows
UPDATE artifacts SET comments_enabled = $2
WHERE artifact_id = $1;

-- name: UpdateArtifactCommentsEnabledBySlug :execrows
UPDATE artifacts SET comments_enabled = $2
WHERE named_slug = $1;

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

-- SlugLiveCreator returns the creator of a slug's earliest NON-ARCHIVED
-- version — who owns the slug's live lineage right now.
--
-- This is not the same as SlugCreator, which ignores deleted_at so that
-- archiving v1 cannot transfer ownership. That is right for a continuous
-- lineage and wrong across a reuse: an all-archived slug is free for anyone to
-- claim, and after someone does, SlugCreator still names the person who walked
-- away. Share links need the live answer, because acting on the slug now means
-- acting on whatever document currently occupies it.
--
-- name: SlugLiveCreator :one
SELECT creator FROM artifacts
WHERE named_slug = $1 AND deleted_at IS NULL
ORDER BY version ASC
LIMIT 1;
