-- name: InsertAPIKey :one
INSERT INTO api_keys (key_hash, key_prefix, owner_email, name, scopes, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetAPIKeyByHash :one
-- Active only: not revoked, not expired. Runs on every key-authed request.
SELECT * FROM api_keys
WHERE key_hash = $1 AND revoked_at IS NULL AND expires_at > now();

-- name: ListAPIKeysByOwner :many
-- Includes revoked (shown marked in the UI).
SELECT * FROM api_keys WHERE LOWER(owner_email) = LOWER($1) ORDER BY created_at DESC;

-- name: ListAllAPIKeys :many
-- Admin view. Pagination is a later follow-up.
SELECT * FROM api_keys ORDER BY created_at DESC;

-- name: RevokeAPIKey :execrows
-- Owner-scoped soft revoke; rows-affected lets the handler 404 a non-owner.
-- Named params so sqlc generates RevokeAPIKeyParams{ID, OwnerEmail} (not `Lower`).
UPDATE api_keys SET revoked_at = now()
WHERE id = @id AND LOWER(owner_email) = LOWER(@owner_email) AND revoked_at IS NULL;

-- name: RevokeAPIKeyByID :execrows
-- Admin soft revoke of any key.
UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: TouchAPIKey :exec
UPDATE api_keys SET last_used_at = now() WHERE id = $1;
