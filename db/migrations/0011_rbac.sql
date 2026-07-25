-- Role-based permissions. A role is a named set of opaque permission keys; a
-- person (email) or a user-group can be assigned one or more roles. A caller's
-- effective permissions are the union of the permissions of every role they
-- hold, plus the baseline USER role that everyone has implicitly (see
-- pgstore.EffectivePermissions).
--
-- The two built-in roles are seeded here with their permission SETS. The ADMIN
-- *assignment* to specific emails is NOT seeded — it's re-asserted at startup
-- from ARTI_ADMIN_EMAILS (idempotent), so config stays the lockout-proof floor.
--
-- +goose Up
CREATE TABLE roles (
    name        TEXT PRIMARY KEY,
    description TEXT        NOT NULL DEFAULT '',
    permissions TEXT[]      NOT NULL DEFAULT '{}', -- opaque permission keys
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    modified_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE role_assignments (
    principal_type TEXT NOT NULL CHECK (principal_type IN ('user', 'group')),
    principal_id   TEXT NOT NULL,  -- lowercased email, or user-group name
    role_name      TEXT NOT NULL REFERENCES roles(name) ON DELETE CASCADE,
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (principal_type, principal_id, role_name)
);
CREATE INDEX role_assignments_principal_idx ON role_assignments (principal_type, principal_id);
CREATE INDEX role_assignments_role_idx ON role_assignments (role_name);

INSERT INTO roles (name, description, permissions) VALUES
  ('ADMIN', 'Full administrative access', '{MANAGE_ROLES,MANAGE_USER_GROUPS,MANAGE_ARTIFACTS,USE_ARTIFACTS}'),
  ('USER',  'Default access for every authenticated user', '{USE_ARTIFACTS}');

-- +goose Down
DROP TABLE role_assignments;
DROP TABLE roles;
