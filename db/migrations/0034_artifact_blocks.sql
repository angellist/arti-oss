-- An admin kill switch for a document, or for a family of them.
--
-- The existing controls all answer "who may read this": an ACL narrows the
-- audience, archiving moves a document out of the catalog but keeps it
-- readable. Neither takes a document away from everyone, and neither can be
-- aimed at a slug that does not exist yet. A block does both: rows matched by
-- a pattern here stop existing for every caller, admins included, and nothing
-- about the document changes. Deleting the row restores it exactly.
--
-- A pattern is matched against a document's slug, or against its artifact_id
-- when it has no slug, with `*` as the only wildcard and case ignored.

-- +goose Up
CREATE TABLE artifact_blocks (
    pattern    TEXT PRIMARY KEY,
    reason     TEXT        NOT NULL DEFAULT '',
    created_by TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS artifact_blocks;
