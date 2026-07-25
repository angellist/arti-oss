-- Per-artifact access control via glob-on-email patterns. Default
-- '{*}' preserves the legacy "everyone authenticated reads everything"
-- behavior on existing rows.
--
-- Matching is case-insensitive, whole-string-anchored. A row with an
-- empty array is creator-only (creator + admins always have access).
--
-- +goose Up
ALTER TABLE artifacts
  ADD COLUMN allowed_access TEXT[] NOT NULL DEFAULT '{*}';

-- +goose Down
ALTER TABLE artifacts DROP COLUMN allowed_access;
