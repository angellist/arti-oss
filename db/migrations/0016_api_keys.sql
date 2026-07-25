-- 0016_api_keys.sql
-- Self-serve, scope-generic API keys (v1: upload-only). Opaque "arti_{scope}_…"
-- bearer; arti stores only sha256. EnforceUploadScope limits upload keys to
-- create/append/read ≤25 MiB. Revoke is a soft state; rows are never deleted.

-- +goose Up
CREATE TABLE api_keys (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    key_hash     bytea       NOT NULL UNIQUE,
    key_prefix   text        NOT NULL,
    owner_email  text        NOT NULL,
    name         text        NOT NULL,
    scopes       text[]      NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at   timestamptz
);
CREATE INDEX idx_api_keys_owner ON api_keys (LOWER(owner_email));

-- +goose Down
DROP TABLE IF EXISTS api_keys;
