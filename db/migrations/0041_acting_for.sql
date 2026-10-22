-- 0041_acting_for.sql
-- A service principal may write a version on behalf of a named person (DD-0094):
-- the person is the version's creator, and asserted_by names the principal that
-- made the claim. NULL on every version written directly.
-- ASSERT_ACTOR is the permission a service principal needs to make that claim;
-- it joins the built-in ADMIN role so "ADMIN holds every permission" stays true.

-- +goose Up
ALTER TABLE artifacts ADD COLUMN asserted_by TEXT;

CREATE INDEX ix_artifacts_asserted_by ON artifacts (asserted_by)
    WHERE asserted_by IS NOT NULL AND deleted_at IS NULL;

UPDATE roles
SET permissions = array_append(permissions, 'ASSERT_ACTOR'),
    modified_at = now()
WHERE name = 'ADMIN'
  AND NOT ('ASSERT_ACTOR' = ANY (permissions));

-- +goose Down
UPDATE roles
SET permissions = array_remove(permissions, 'ASSERT_ACTOR'),
    modified_at = now()
WHERE name = 'ADMIN';

DROP INDEX IF EXISTS ix_artifacts_asserted_by;
ALTER TABLE artifacts DROP COLUMN IF EXISTS asserted_by;
