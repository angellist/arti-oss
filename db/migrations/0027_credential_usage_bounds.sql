-- 0027_credential_usage_bounds.sql
-- Makes credential_usage survive an adversary and its own age.
--
-- The owner index added in 0026 cannot serve the query that reads it: the
-- lookup folds case (LOWER(owner_email)) and a plain btree on the raw column
-- does not match that expression, so every Settings → API Keys load scanned the
-- whole table. The day index is for the retention delete.

-- +goose Up
DROP INDEX IF EXISTS ix_credential_usage_owner;
CREATE INDEX ix_credential_usage_owner ON credential_usage (LOWER(owner_email), day DESC);
CREATE INDEX ix_credential_usage_day ON credential_usage (day);

-- Whether an owner has already been told about a key on a network, kept apart
-- from the usage rows on purpose. While "have we alerted?" was answered by
-- credential_usage, recording usage was what marked an alert as delivered — so
-- an alert held back by a rate limit was never sent at all. A row here is
-- written only when the DM is actually going out.
--
-- Keyed on the NETWORK, not the address: a phone or a laptop moves inside its
-- own /64 constantly, and alerting on that is how a person learns to ignore
-- the alert. The user agent is deliberately absent — a tool that upgrades
-- itself is not a new source.
CREATE TABLE credential_alert (
    cred             TEXT        NOT NULL,
    owner_email      TEXT        NOT NULL,
    network          TEXT        NOT NULL,
    first_alerted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (cred, owner_email, network)
);

-- ix_credential_usage_source served the old alert lookup and nothing reads it now.
DROP INDEX IF EXISTS ix_credential_usage_source;

-- +goose Down
CREATE INDEX IF NOT EXISTS ix_credential_usage_source ON credential_usage (cred, owner_email, ip, user_agent);
DROP TABLE IF EXISTS credential_alert;
DROP INDEX IF EXISTS ix_credential_usage_day;
DROP INDEX IF EXISTS ix_credential_usage_owner;
CREATE INDEX ix_credential_usage_owner ON credential_usage (owner_email, day DESC);
