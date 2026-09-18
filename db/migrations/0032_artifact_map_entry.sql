-- 0032_artifact_map_entry.sql
-- Mutable head for MAP artifacts: one row per key, keyed by slug.
-- Versions stay immutable NDJSON snapshots in the artifacts table; this is
-- the head those snapshots are taken from.

-- +goose Up
CREATE TABLE artifact_map_entry (
    named_slug TEXT        NOT NULL,
    -- COLLATE "C" is load-bearing, not a preference. Prefix listing is a
    -- half-open range (key >= prefix AND key < successor) and the cursor is
    -- key > after_key; both assume byte order. Under a locale collation
    -- punctuation is weighted differently, so a ':'-namespaced prefix could
    -- match the wrong rows and paging could skip or repeat. Nothing else in
    -- arti sets a collation, so key would otherwise inherit the RDS default.
    key        TEXT COLLATE "C" NOT NULL,
    value      JSONB       NOT NULL,
    rev        BIGINT      NOT NULL DEFAULT 1,
    size_bytes INTEGER     NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT        NOT NULL,
    PRIMARY KEY (named_slug, key),
    CONSTRAINT ck_map_entry_value_size CHECK (size_bytes > 0 AND size_bytes <= 65536),
    CONSTRAINT ck_map_entry_key_len CHECK (length(key) BETWEEN 1 AND 256)
);

-- +goose Down
DROP TABLE IF EXISTS artifact_map_entry;
