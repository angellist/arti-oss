-- 0042_read_org_shared.sql
-- READ_ORG_SHARED lets a service principal read what is shared org-wide
-- (`*@<allowlisted domain>`), which its .invalid address never matches.
-- It joins the built-in ADMIN role so "ADMIN holds every permission" stays true.

-- +goose Up
UPDATE roles
SET permissions = array_append(permissions, 'READ_ORG_SHARED'),
    modified_at = now()
WHERE name = 'ADMIN'
  AND NOT ('READ_ORG_SHARED' = ANY (permissions));

-- +goose Down
UPDATE roles
SET permissions = array_remove(permissions, 'READ_ORG_SHARED'),
    modified_at = now()
WHERE name = 'ADMIN';
