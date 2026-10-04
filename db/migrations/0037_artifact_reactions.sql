-- A reaction belongs to a slug (view_key, the same key artifact_views uses),
-- not to one version, so it survives new versions. Likes are counted
-- publicly; bookmarks are only ever read back for their own reactor.

-- +goose Up
CREATE TABLE IF NOT EXISTS artifact_reactions (
    view_key TEXT        NOT NULL,
    reactor  TEXT        NOT NULL,
    kind     TEXT        NOT NULL CHECK (kind IN ('like', 'bookmark')),
    at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (view_key, kind, reactor)
);
CREATE INDEX IF NOT EXISTS artifact_reactions_reactor_idx
    ON artifact_reactions (reactor, kind);

-- +goose Down
DROP TABLE IF EXISTS artifact_reactions;
