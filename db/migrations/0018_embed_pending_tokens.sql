-- 0018_embed_pending_tokens.sql
-- Cross-replica handoff for the user-mode embed handshake. A sandboxed embed
-- popup (e.g. inside Front) can't reliably postMessage back to its opener, so
-- the mint stashes the freshly-minted per-user app token here and the embedded
-- app polls for it by its own (surface, state) nonce. Postgres-backed because
-- prod runs 2-6 replicas under the HPA: the mint and the poll can land on
-- different pods. Single-use + short-lived: taken exactly once, expires fast.

-- +goose Up
CREATE TABLE embed_pending_tokens (
    surface     TEXT NOT NULL,
    state       TEXT NOT NULL,              -- the app-generated nonce (opaque)
    token       TEXT NOT NULL,              -- the minted per-user app token
    email       TEXT NOT NULL,              -- who it was minted for (audit)
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,       -- ~2m; a poll after this misses
    PRIMARY KEY (surface, state)
);
CREATE INDEX embed_pending_tokens_expires_at_idx ON embed_pending_tokens (expires_at);

-- +goose Down
DROP TABLE embed_pending_tokens;
