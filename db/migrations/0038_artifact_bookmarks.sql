-- Likes were dropped before release; bookmarks are the only reaction left, so
-- 0037's table becomes a plain per-slug bookmark list. Bookmark counts are
-- public; whether a given person bookmarked a slug is shown only to them.

-- +goose Up
DELETE FROM artifact_reactions WHERE kind <> 'bookmark';
ALTER TABLE artifact_reactions RENAME TO artifact_bookmarks;
ALTER TABLE artifact_bookmarks RENAME COLUMN reactor TO email;
ALTER TABLE artifact_bookmarks DROP COLUMN kind;
ALTER TABLE artifact_bookmarks ADD PRIMARY KEY (view_key, email);
CREATE INDEX IF NOT EXISTS artifact_bookmarks_email_idx ON artifact_bookmarks (email);

-- +goose Down
DROP INDEX IF EXISTS artifact_bookmarks_email_idx;
ALTER TABLE artifact_bookmarks DROP CONSTRAINT artifact_bookmarks_pkey;
ALTER TABLE artifact_bookmarks ADD COLUMN kind TEXT NOT NULL DEFAULT 'bookmark'
    CHECK (kind IN ('like', 'bookmark'));
ALTER TABLE artifact_bookmarks ALTER COLUMN kind DROP DEFAULT;
ALTER TABLE artifact_bookmarks RENAME COLUMN email TO reactor;
ALTER TABLE artifact_bookmarks RENAME TO artifact_reactions;
ALTER TABLE artifact_reactions ADD PRIMARY KEY (view_key, kind, reactor);
CREATE INDEX IF NOT EXISTS artifact_reactions_reactor_idx ON artifact_reactions (reactor, kind);
