-- 0033_artifact_map_id.sql
-- Key a MAP's head by an id minted when the map is created, not by its slug.
-- A slug is a name the platform hands back: archiving frees it, version
-- numbering ignores archival, and anyone may republish there. A head keyed by
-- the name therefore outlives the document it belonged to, which is what made
-- archive, reclaim and retype each need their own rule. An id is minted once
-- and inherited by every later version of the same map, so a map that is gone
-- stays gone and a new map at the same name is empty.

-- +goose Up
ALTER TABLE artifacts ADD COLUMN map_id UUID;

-- One id per MAP slug. A slug that was archived and reclaimed becomes a single
-- map here, because one shared head is what those rows mean today.
UPDATE artifacts a
SET map_id = ids.map_id
FROM (
    SELECT named_slug, gen_random_uuid() AS map_id
    FROM artifacts
    WHERE artifact_type = 'MAP' AND named_slug IS NOT NULL
    GROUP BY named_slug
) ids
WHERE a.artifact_type = 'MAP' AND a.named_slug = ids.named_slug;

ALTER TABLE artifacts ADD CONSTRAINT ck_artifacts_map_id_iff_map
    CHECK ((map_id IS NOT NULL) = (artifact_type = 'MAP'));

ALTER TABLE artifact_map_entry ADD COLUMN map_id UUID;

UPDATE artifact_map_entry e
SET map_id = a.map_id
FROM (SELECT DISTINCT named_slug, map_id FROM artifacts WHERE artifact_type = 'MAP') a
WHERE a.named_slug = e.named_slug;

-- A head whose slug holds no MAP artifact is already unreachable: every route
-- resolves the slug to an artifact row first.
DELETE FROM artifact_map_entry WHERE map_id IS NULL;

ALTER TABLE artifact_map_entry
    DROP CONSTRAINT artifact_map_entry_pkey,
    DROP COLUMN named_slug,
    ALTER COLUMN map_id SET NOT NULL,
    ADD PRIMARY KEY (map_id, key);

-- +goose Down
ALTER TABLE artifact_map_entry ADD COLUMN named_slug TEXT;

-- Only a map with a live version can be named by a slug again. Reclaimed
-- slugs are the reason this direction loses rows: two ids can share one name,
-- and the old primary key cannot hold both.
DELETE FROM artifact_map_entry e
WHERE NOT EXISTS (
    SELECT 1 FROM artifacts a
    WHERE a.map_id = e.map_id AND a.artifact_type = 'MAP' AND a.deleted_at IS NULL
);

UPDATE artifact_map_entry e
SET named_slug = a.named_slug
FROM (SELECT DISTINCT map_id, named_slug FROM artifacts WHERE artifact_type = 'MAP') a
WHERE a.map_id = e.map_id;

ALTER TABLE artifact_map_entry
    DROP CONSTRAINT artifact_map_entry_pkey,
    DROP COLUMN map_id,
    ALTER COLUMN named_slug SET NOT NULL,
    ADD PRIMARY KEY (named_slug, key);

ALTER TABLE artifacts DROP CONSTRAINT ck_artifacts_map_id_iff_map;
ALTER TABLE artifacts DROP COLUMN map_id;
