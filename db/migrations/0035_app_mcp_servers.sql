-- The MCP upstreams an APP may reach through the apps proxy, managed by
-- admins at runtime. This table is the only source of truth: the
-- ARTI_APP_MCP_SERVERS env var seeds it once, when it is empty, and is
-- otherwise ignored. The in-process built-ins (arti-self, llm) are not rows.
--
-- `auth` is constrained in code, not by a CHECK: 'service' is built-in only
-- and must never be settable from the admin API.
-- An empty tool_allowlist admits every tool the app's own manifest allows.

-- +goose Up
CREATE TABLE app_mcp_servers (
    name           TEXT PRIMARY KEY,
    resource_url   TEXT        NOT NULL,
    auth           TEXT        NOT NULL,
    scope          TEXT        NOT NULL DEFAULT '',
    enabled        BOOLEAN     NOT NULL DEFAULT TRUE,
    tool_allowlist TEXT[]      NOT NULL DEFAULT '{}',
    notes          TEXT        NOT NULL DEFAULT '',
    created_by     TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by     TEXT        NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS app_mcp_servers;
