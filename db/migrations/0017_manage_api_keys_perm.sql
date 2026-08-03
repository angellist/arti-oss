-- 0017_manage_api_keys_perm.sql
-- Add the MANAGE_API_KEYS permission to the built-in ADMIN role so the
-- "ADMIN holds every permission" invariant stays true now that MANAGE_API_KEYS
-- exists (rbac.AllPermissions). MANAGE_API_KEYS gates the cross-owner admin
-- view of API keys (list all / revoke any). Idempotent.

-- +goose Up
UPDATE roles
SET permissions = array_append(permissions, 'MANAGE_API_KEYS'),
    modified_at = now()
WHERE name = 'ADMIN'
  AND NOT ('MANAGE_API_KEYS' = ANY (permissions));

-- +goose Down
UPDATE roles
SET permissions = array_remove(permissions, 'MANAGE_API_KEYS'),
    modified_at = now()
WHERE name = 'ADMIN';
