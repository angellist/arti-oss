-- +goose Up

-- idempotency_keys lets clients de-dupe retried writes. Pattern:
--   1. Client generates a stable key per logical write (e.g.
--      sha256(author+body+posted_at) for a feedback entry).
--   2. Server checks (key, creator) on every write; if present and
--      young enough, returns the cached response without writing again.
--   3. Otherwise, write the artifact, then INSERT this row.
--
-- TTL is 24h, enforced by a periodic cleanup or the WHERE clause on
-- lookups (see GetIdempotencyKey query). We index on created_at so the
-- cleanup is cheap and the lookup short-circuits stale rows.
--
-- Keyed by (idempotency_key, creator): two different users with the
-- same key never collide. Creator pulled from auth context, never from
-- the request body.
CREATE TABLE idempotency_keys (
    idempotency_key      TEXT        NOT NULL,
    creator              TEXT        NOT NULL,
    response_artifact_id UUID        NOT NULL,
    response_version     INT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (idempotency_key, creator)
);

CREATE INDEX ix_idempotency_keys_created
    ON idempotency_keys (created_at DESC);

-- +goose Down

DROP TABLE IF EXISTS idempotency_keys;
