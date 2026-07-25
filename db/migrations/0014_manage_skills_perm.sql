-- 0014_manage_skills_perm.sql
-- Add the MANAGE_SKILLS permission to the built-in ADMIN role so the
-- "ADMIN holds every permission" invariant stays true now that MANAGE_SKILLS
-- exists (rbac.AllPermissions). MANAGE_SKILLS gates writes to kind:skill
-- artifacts and exempts holders from the app:couch List/Search hide; it is
-- granted to app service identities (e.g. couch's service@) via a role
-- assignment done out-of-band. Idempotent.

-- +goose Up
UPDATE roles
SET permissions = array_append(permissions, 'MANAGE_SKILLS'),
    modified_at = now()
WHERE name = 'ADMIN'
  AND NOT ('MANAGE_SKILLS' = ANY (permissions));

-- +goose Down
UPDATE roles
SET permissions = array_remove(permissions, 'MANAGE_SKILLS'),
    modified_at = now()
WHERE name = 'ADMIN';
