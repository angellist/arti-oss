-- Add optional profile columns to comments so the frontend can render
-- real display names and avatars (populated at write time from the
-- caller's OIDC token). Existing rows stay NULL and fall back to
-- email-derived display names.
--
-- +goose Up
ALTER TABLE comments ADD COLUMN author_name    TEXT;
ALTER TABLE comments ADD COLUMN author_picture TEXT;

-- +goose Down
ALTER TABLE comments DROP COLUMN author_picture;
ALTER TABLE comments DROP COLUMN author_name;
