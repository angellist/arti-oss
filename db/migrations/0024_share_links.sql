-- External timed share links.
--
-- Numbered 0024, not 0022. This started life as 0022 while main was still at
-- 0021; main then merged 0023 first. goose refuses to apply a migration older
-- than the database's current version — "found 1 missing migrations before
-- current version 23" — which would have left the migrate init container
-- failing and halted the rollout with the tables never created.
--
-- CREATE ... IF NOT EXISTS is deliberate rather than habitual: the staging
-- database already ran this exact content as 0022 during pre-merge
-- verification, so re-running it there under the new number must be a no-op
-- instead of a duplicate-table error. On a database that has never seen it,
-- the effect is unchanged.
--
-- A share link is a CAPABILITY, not an identity. Holding the URL grants read
-- of exactly one document until the link expires, is revoked, or the document
-- is archived. Nothing about the holder is known or checked, which is the
-- point: the recipient has no AngelList account and cannot get one.
--
-- The token is never stored. token_hash is sha256(token) and is the lookup
-- key, so a database dump yields no live links and there is no
-- secret-dependent comparison anywhere in the serve path. Same contract as
-- api_keys (internal/apikeys/key.go).
--
-- Exactly one of artifact_id / slug is set. artifact_id PINS the link to one
-- version; slug makes it TRACK the slug's newest version, which means the
-- recipient sees versions published after the link was sent.

-- +goose Up
CREATE TABLE IF NOT EXISTS share_links (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash     BYTEA       NOT NULL UNIQUE,
    token_prefix   TEXT        NOT NULL,
    artifact_id    UUID        REFERENCES artifacts(artifact_id) ON DELETE CASCADE,
    slug           TEXT,
    created_by     TEXT        NOT NULL,
    note           TEXT        NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ NOT NULL,
    revoked_at     TIMESTAMPTZ,
    revoked_by     TEXT,
    last_opened_at TIMESTAMPTZ,
    open_count     BIGINT      NOT NULL DEFAULT 0,
    CONSTRAINT share_links_target_exactly_one
        CHECK ((artifact_id IS NULL) <> (slug IS NULL))
);

CREATE INDEX IF NOT EXISTS share_links_artifact_id_idx ON share_links (artifact_id);
CREATE INDEX IF NOT EXISTS share_links_slug_idx        ON share_links (slug);

-- One row per open.
--
-- `ip` is the client address as chimid.RealIP resolves it from the forwarded
-- headers. It is trustworthy only because auth.StripSpoofableIPHeaders removes
-- True-Client-IP first: that header is not managed by this deployment's edge,
-- and RealIP prefers it over every other, so without the strip a caller chose
-- what got recorded here.
--
-- `peer_addr` is the TCP peer. Behind the load balancer that is the ingress
-- pod, NOT the client — one client was measured producing three different
-- peers. It records which path a request took through the edge and nothing
-- about who sent it.
--
-- Both are TEXT rather than INET: they are display-only and never queried by
-- subnet, and INET's pgx mapping is version-dependent with no existing column
-- in this schema to copy.
CREATE TABLE IF NOT EXISTS share_link_opens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    link_id    UUID        NOT NULL REFERENCES share_links(id) ON DELETE CASCADE,
    at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    ip         TEXT        NOT NULL DEFAULT '',
    peer_addr  TEXT        NOT NULL DEFAULT '',
    user_agent TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS share_link_opens_link_id_at_idx ON share_link_opens (link_id, at DESC);

-- +goose Down
DROP TABLE IF EXISTS share_link_opens;
DROP TABLE IF EXISTS share_links;
