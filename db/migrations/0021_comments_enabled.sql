-- Per-document comment switch.
--
-- TRUE (the default, and the state of every pre-existing row) keeps today's
-- behavior: anyone who can read the artifact can read and add comments on it.
-- FALSE turns commenting off for the document — the viewer renders no comment
-- controls, arti-server stops injecting the in-page overlay into served HTML,
-- and the comments API refuses writes and reports no threads. Existing threads
-- are NOT deleted; flipping the switch back restores them.
--
-- Scoped per row (per version) like allowed_access, but written slug-wide by
-- the API so it reads as a per-DOC control: a new version inherits the
-- previous version's value, and toggling it updates every version of the slug.
-- Only the slug's owner (its earliest-version creator) or an admin may change
-- it — same authority rule as an ACL change.

-- +goose Up
ALTER TABLE artifacts ADD COLUMN comments_enabled BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose Down
ALTER TABLE artifacts DROP COLUMN comments_enabled;
