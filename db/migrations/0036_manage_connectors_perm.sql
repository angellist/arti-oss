-- 0036_manage_connectors_perm.sql
-- Add the MANAGE_CONNECTORS permission to the built-in ADMIN role so the
-- "ADMIN holds every permission" invariant stays true now that
-- MANAGE_CONNECTORS exists (rbac.AllPermissions). MANAGE_CONNECTORS gates the
-- admin surface for the APP MCP connector list. Idempotent.

-- +goose Up
UPDATE roles
SET permissions = array_append(permissions, 'MANAGE_CONNECTORS'),
    modified_at = now()
WHERE name = 'ADMIN'
  AND NOT ('MANAGE_CONNECTORS' = ANY (permissions));

-- +goose Down
UPDATE roles
SET permissions = array_remove(permissions, 'MANAGE_CONNECTORS'),
    modified_at = now()
WHERE name = 'ADMIN';
