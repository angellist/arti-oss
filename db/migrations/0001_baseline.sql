-- +goose Up

CREATE TABLE artifacts (
    artifact_id    UUID PRIMARY KEY,
    artifact_type  TEXT NOT NULL,
    named_slug     TEXT,
    version        INT,
    title          TEXT NOT NULL,
    description    TEXT,
    content_type   TEXT NOT NULL,
    inline_content BYTEA,
    blob_ref       TEXT,
    sha256         TEXT,
    size_bytes     BIGINT,
    creator        TEXT NOT NULL,
    scope          TEXT,
    labels         TEXT[] NOT NULL DEFAULT '{}',
    metadata       JSONB NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    modified_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ,
    CONSTRAINT ck_content_location
        CHECK ((inline_content IS NOT NULL)::INT + (blob_ref IS NOT NULL)::INT = 1),
    CONSTRAINT ck_blob_ref_complete
        CHECK (blob_ref IS NULL OR (sha256 IS NOT NULL AND size_bytes IS NOT NULL)),
    CONSTRAINT ck_inline_size
        CHECK (inline_content IS NULL OR length(inline_content) <= 65536)
);

CREATE UNIQUE INDEX uix_artifacts_slug_ver
    ON artifacts (named_slug, version)
    WHERE named_slug IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX ix_artifacts_creator  ON artifacts (creator);
CREATE INDEX ix_artifacts_type     ON artifacts (artifact_type);
CREATE INDEX ix_artifacts_sha256   ON artifacts (sha256);
CREATE INDEX ix_artifacts_scope    ON artifacts (scope) WHERE deleted_at IS NULL;
CREATE INDEX ix_artifacts_labels   ON artifacts USING GIN (labels);
CREATE INDEX ix_artifacts_created  ON artifacts (created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX ix_artifacts_meta_gin ON artifacts USING GIN (metadata);

-- +goose Down

DROP TABLE IF EXISTS artifacts;
