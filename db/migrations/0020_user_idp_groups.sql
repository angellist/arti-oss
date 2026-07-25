-- Snapshot of each user's IdP (SSO) group memberships, captured at every
-- interactive login (both the built-in `oidc` mode and the `proxy` mode).
-- Grants reference these via the `idp:<name>` token — a namespace DISTINCT
-- from manual `group:` tokens, so a manually-created arti group can never
-- impersonate an IdP group.
--
-- Resolution is freshness-bounded (auth.idp_groups_max_age /
-- ARTI_IDP_GROUPS_MAX_AGE): a snapshot older than the bound is ignored (fail
-- closed), so a user removed from an IdP group loses `idp:`-granted access
-- once their snapshot ages out or on their next login (whichever is first).

-- +goose Up
CREATE TABLE user_idp_groups (
    email       TEXT PRIMARY KEY,                 -- lowercased
    groups      TEXT[]      NOT NULL DEFAULT '{}', -- normalized names, no idp: prefix
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE user_idp_groups;
