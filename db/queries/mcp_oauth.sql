-- name: InsertMCPClient :exec
INSERT INTO mcp_oauth_clients (
    id, client_id, client_secret_hash, client_name,
    redirect_uris, grant_types, scope, token_endpoint_auth_method
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetMCPClient :one
SELECT * FROM mcp_oauth_clients WHERE client_id = $1;

-- name: TouchMCPClient :exec
UPDATE mcp_oauth_clients SET last_used_at = now() WHERE client_id = $1;

-- name: InsertMCPCode :exec
INSERT INTO mcp_oauth_codes (
    code, client_id, redirect_uri, code_challenge, code_challenge_method,
    email, scope, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: TakeMCPCode :one
UPDATE mcp_oauth_codes
SET used = TRUE
WHERE code = $1 AND used = FALSE AND expires_at > now()
RETURNING *;

-- name: SweepExpiredMCPCodes :execrows
DELETE FROM mcp_oauth_codes WHERE expires_at < now() - INTERVAL '1 hour';
