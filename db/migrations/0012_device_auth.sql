-- 0012_device_auth.sql
-- RFC 8628 Device Authorization Grant for headless agents (couch sandboxes).
-- Postgres-backed (not in-mem) because the code → approve → poll steps can
-- each land on a different replica under the HPA.

-- +goose Up

-- Pending grant. device_code is the agent's polling secret; user_code is the
-- short human-typed code. Created on POST /auth/device/code, approved on the
-- SSO confirm page, consumed once at POST /auth/device/token.
CREATE TABLE device_auth (
    device_code  TEXT PRIMARY KEY,
    user_code    TEXT NOT NULL UNIQUE,
    status       TEXT NOT NULL DEFAULT 'pending',  -- pending | approved | consumed
    duration     TEXT NOT NULL DEFAULT 'short',     -- short | long (requested lifetime class)
    email        TEXT,                              -- set at approval (SSO-verified)
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,              -- grant window (~10m)
    consumed     BOOLEAN NOT NULL DEFAULT FALSE
);

-- Issued long-lived token families (duration='long' only). One active row per
-- user — re-approval revokes the prior. family_id is embedded as the `fam`
-- claim; current_refresh_jti is the only refresh token that may rotate the
-- family; revoked is the kill switch the upload-scope guard + refresh handler
-- consult. duration='short' tokens are ephemeral and create NO row here.
CREATE TABLE device_token (
    family_id           TEXT PRIMARY KEY,            -- uuid
    email               TEXT NOT NULL,
    current_refresh_jti TEXT NOT NULL,
    scope               TEXT NOT NULL DEFAULT 'upload',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at          TIMESTAMPTZ NOT NULL,         -- ~30d hard cap
    revoked             BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX idx_device_token_email_active ON device_token (email) WHERE NOT revoked;

-- +goose Down
DROP TABLE IF EXISTS device_token;
DROP TABLE IF EXISTS device_auth;
