-- Explicit, transferable document ownership.
--
-- Ownership was derived, not stored: the owner of a slug was whoever created
-- its earliest version, recomputed per call by an ORDER BY version ASC LIMIT 1
-- over `artifacts`. That made ownership unstateable (no field to read, no
-- field to change) and re-derived it independently in three places.
--
-- One row per slug, so ownership cannot disagree between versions of a
-- document the way a per-version column could. Slugless artifacts have no
-- lineage and no row here: their creator is their owner.
--
-- The backfill preserves the derived answer exactly, including its deliberate
-- disregard for deleted_at — archiving v1 must not transfer ownership.

-- +goose Up
CREATE TABLE artifact_owners (
    named_slug  TEXT PRIMARY KEY,
    owner_email TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by  TEXT
);

INSERT INTO artifact_owners (named_slug, owner_email)
SELECT DISTINCT ON (named_slug) named_slug, creator
  FROM artifacts
 WHERE named_slug IS NOT NULL AND named_slug <> ''
 ORDER BY named_slug, version ASC;

-- +goose Down
DROP TABLE artifact_owners;
