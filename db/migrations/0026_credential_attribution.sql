-- 0026_credential_attribution.sql
-- Records WHICH credential made a write, and where each credential is used
-- from. `creator` alone cannot answer either: an API key authenticates as its
-- owner's email, so an agent holding a copied key is indistinguishable from
-- that person working in a browser.

-- +goose Up

-- Credential reference, e.g. "apikey:<uuid>" | "device:<family>" |
-- "token" (a user bearer: CLI or MCP) | "session" | "service". NULL on rows
-- written before this migration, and on any path with no authenticated
-- credential.
ALTER TABLE artifacts ADD COLUMN written_via TEXT;

-- The credential's human name as it stood at write time (an API key's name;
-- empty for credentials nobody names). Denormalized on purpose: the alternative
-- is joining api_keys on every catalog row, and a viewer cannot list another
-- person's keys to resolve the id themselves.
ALTER TABLE artifacts ADD COLUMN written_via_name TEXT;

CREATE INDEX ix_artifacts_written_via ON artifacts (written_via)
    WHERE written_via IS NOT NULL AND deleted_at IS NULL;

-- One row per owner per credential per day per source. Deliberately NOT per
-- request:
-- arti serves far more reads than anything else, and the questions this has to
-- answer — where is this credential used, and has it appeared somewhere new —
-- need the source set, not the request list.
--
-- user_agent and ip are NOT NULL with '' standing in for absent, so they stay
-- usable in the primary key (NULL would let duplicate source rows accumulate).
CREATE TABLE credential_usage (
    cred        TEXT        NOT NULL,
    day         DATE        NOT NULL,
    ip          TEXT        NOT NULL,
    user_agent  TEXT        NOT NULL,
    owner_email TEXT        NOT NULL,
    reads       BIGINT      NOT NULL DEFAULT 0,
    writes      BIGINT      NOT NULL DEFAULT 0,
    first_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- owner_email is IN the key because `session`, `token` and `service` are
    -- shared references with no per-credential id: two people behind one
    -- address and client would otherwise collapse onto a single row, and the
    -- later flush would overwrite whose it was.
    PRIMARY KEY (cred, owner_email, day, ip, user_agent)
);

-- The owner's own credential list, newest activity first.
CREATE INDEX ix_credential_usage_owner ON credential_usage (owner_email, day DESC);

-- "Has this person used this credential from this source before?" — the
-- new-source alert's only query, run once per credential per source per day.
CREATE INDEX ix_credential_usage_source ON credential_usage (cred, owner_email, ip, user_agent);

-- +goose Down
DROP TABLE IF EXISTS credential_usage;
DROP INDEX IF EXISTS ix_artifacts_written_via;
ALTER TABLE artifacts DROP COLUMN IF EXISTS written_via_name;
ALTER TABLE artifacts DROP COLUMN IF EXISTS written_via;
