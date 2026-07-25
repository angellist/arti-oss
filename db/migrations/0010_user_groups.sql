-- Named user groups for access control. A group is a collection of member
-- emails identified by a lowercase slug `name`; it is granted read-access to
-- an artifact by adding the token `group:<name>` to artifacts.allowed_access.
--
-- Resolution is "live": access is decided by the group's CURRENT membership
-- at request time (see pgstore.CallerGroups + CanAccess), so editing a group
-- immediately changes who can read everything granted to it. A deleted group
-- leaves dangling `group:<name>` tokens that resolve to no members — i.e.
-- they grant nobody (fail-closed), so no cascade is needed.
--
-- +goose Up
CREATE TABLE user_groups (
    name         TEXT PRIMARY KEY,             -- lowercase slug; token is 'group:'||name
    display_name TEXT        NOT NULL DEFAULT '',
    members      TEXT[]      NOT NULL DEFAULT '{}', -- lowercased member emails
    created_by   TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    modified_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE user_groups;
