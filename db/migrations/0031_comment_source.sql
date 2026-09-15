-- 0031_comment_source.sql
-- Records the transport that wrote each comment.

-- +goose Up
ALTER TABLE comments ADD COLUMN source TEXT NOT NULL DEFAULT 'web';

-- +goose Down
ALTER TABLE comments DROP COLUMN IF EXISTS source;
