-- +goose Up

-- mcp_oauth_clients holds clients registered via RFC 7591 Dynamic
-- Client Registration (POST /oauth/register). Each MCP gateway (e.g.
-- Runlayer) registers once; subsequent end-user logins reuse the same
-- client_id with PKCE. Confidential clients (basic / post auth) get a
-- hashed secret; public clients (token_endpoint_auth_method=none) have
-- a NULL secret and rely on PKCE alone.
CREATE TABLE mcp_oauth_clients (
    id                          UUID PRIMARY KEY,
    client_id                   TEXT UNIQUE NOT NULL,
    client_secret_hash          TEXT,
    client_name                 TEXT,
    redirect_uris               TEXT[] NOT NULL DEFAULT '{}',
    grant_types                 TEXT[] NOT NULL DEFAULT '{}',
    scope                       TEXT,
    token_endpoint_auth_method  TEXT NOT NULL DEFAULT 'none',
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at                TIMESTAMPTZ
);

-- mcp_oauth_codes is a tiny short-lived store for authorization-code
-- grants. Codes live up to 10 minutes (cleanup is by `expires_at`
-- check at consume time, plus a periodic sweep — fine for v1).
CREATE TABLE mcp_oauth_codes (
    code                   TEXT PRIMARY KEY,
    client_id              TEXT NOT NULL,
    redirect_uri           TEXT NOT NULL,
    code_challenge         TEXT NOT NULL,
    code_challenge_method  TEXT NOT NULL,
    email                  TEXT NOT NULL,
    scope                  TEXT,
    expires_at             TIMESTAMPTZ NOT NULL,
    used                   BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX ix_mcp_oauth_codes_expires ON mcp_oauth_codes (expires_at);

-- +goose Down
DROP TABLE IF EXISTS mcp_oauth_codes;
DROP TABLE IF EXISTS mcp_oauth_clients;
