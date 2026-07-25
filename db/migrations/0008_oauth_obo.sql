-- Shared, multi-pod storage for arti's on-behalf-of (OBO) OAuth client.
-- Replaces the prototype's in-process maps so the consent callback and proxy
-- calls work regardless of which arti pod handles them. The PKCE state is NOT
-- stored here — it's a self-contained encrypted token. Secret columns
-- (client_secret, tokens) are envelope-encrypted by the app before insert.
--
-- +goose Up
CREATE TABLE oauth_client_registrations (
  issuer            TEXT PRIMARY KEY,         -- the authorization server's issuer
  client_id         TEXT NOT NULL,
  client_secret_enc BYTEA,                    -- AES-256-GCM (app-layer); NULL for public (secret-less) clients
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE oauth_obo_tokens (
  email             TEXT NOT NULL,
  resource          TEXT NOT NULL,            -- the upstream MCP server URL
  access_token_enc  BYTEA NOT NULL,           -- AES-256-GCM (app-layer)
  refresh_token_enc BYTEA,                    -- AES-256-GCM (app-layer); NULL if none
  expiry            TIMESTAMPTZ NOT NULL,
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (email, resource)
);

-- +goose Down
DROP TABLE oauth_obo_tokens;
DROP TABLE oauth_client_registrations;
