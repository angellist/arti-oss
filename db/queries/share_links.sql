-- name: CreateShareLink :one
INSERT INTO share_links (token_hash, token_prefix, artifact_id, slug, created_by, note, expires_at, anchor_owner)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- GetShareLinkByHash is the serve path's ONLY lookup, and it deliberately
-- returns the row whatever its state. The handler decides on revoked_at and
-- expires_at so that a revoked link and an unknown token both cost exactly
-- one indexed query — the branch order in ResolveShare is what keeps those
-- two indistinguishable, and filtering here would move that decision out of
-- the one place it is documented.
-- name: GetShareLinkByHash :one
SELECT * FROM share_links WHERE token_hash = $1;

-- name: GetShareLink :one
SELECT * FROM share_links WHERE id = $1;

-- ListShareLinksForDoc returns every link for a DOCUMENT, which is not the
-- same as every link for one artifact row. A pinned link stores the
-- artifact_id of the version that was current when it was minted, so keying
-- only on the latest version's id would hide links pinned to earlier
-- versions — and the Share dialog promises the document-wide answer. Match
-- the slug directly, or any artifact_id in the slug's lineage, or (for a
-- slugless artifact) the id itself.
-- name: ListShareLinksForDoc :many
SELECT sl.* FROM share_links sl
WHERE sl.slug = sqlc.narg('slug')
   OR sl.artifact_id = sqlc.narg('artifact_id')
   OR sl.artifact_id IN (
        SELECT a.artifact_id FROM artifacts a WHERE a.named_slug = sqlc.narg('slug')
      )
ORDER BY sl.created_at DESC;

-- name: RevokeShareLink :exec
UPDATE share_links SET revoked_at = now(), revoked_by = $2
WHERE id = $1 AND revoked_at IS NULL;

-- name: RecordShareLinkOpen :exec
INSERT INTO share_link_opens (link_id, ip, peer_addr, user_agent)
VALUES ($1, $2, $3, $4);

-- name: BumpShareLinkOpened :exec
UPDATE share_links SET open_count = open_count + 1, last_opened_at = now()
WHERE id = $1;

-- name: ListShareLinkOpens :many
SELECT * FROM share_link_opens
WHERE link_id = $1
ORDER BY at DESC
LIMIT $2;
