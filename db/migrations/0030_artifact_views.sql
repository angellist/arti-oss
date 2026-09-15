-- Numbered 0030 because migrations 0026 through 0029 are already in main.
--
-- A view is one delivery of a human-facing artifact rendering.  view_key is
-- separate from artifact_id so all versions of a named slug share counts
-- while each row still retains the exact version that was served.
-- viewer is empty for structurally anonymous deliveries such as share links
-- and user-mode embeds; those views are intentionally not deduplicated.
-- Counts are queried from this append-only table instead of an artifacts
-- counter column so slug-wide history and viewer details remain available.

-- +goose Up
CREATE TABLE IF NOT EXISTS artifact_views (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    artifact_id UUID        NOT NULL REFERENCES artifacts(artifact_id) ON DELETE CASCADE,
    view_key    TEXT        NOT NULL,
    version     INT,
    viewer      TEXT        NOT NULL DEFAULT '',
    surface     TEXT        NOT NULL,
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS artifact_views_key_at_idx
    ON artifact_views (view_key, at DESC);
CREATE INDEX IF NOT EXISTS artifact_views_artifact_at_idx
    ON artifact_views (artifact_id, at DESC);

-- +goose Down
DROP TABLE IF EXISTS artifact_views;
