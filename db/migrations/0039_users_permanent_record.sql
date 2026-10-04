-- Makes `users` a permanent record of every principal: rows are backfilled
-- from every table that records an email, and each interactive login inserts
-- one. Deactivation is a column; deactivated_at IS NULL means active, and so
-- does a missing row.
--
-- source: 'admin' (added by hand, including every row that predates this
-- migration), 'login' (first recorded at a login), 'backfill' (this
-- migration). A backfilled row's added_at is the migration time, not the
-- principal's first appearance.

-- +goose Up
ALTER TABLE users
    ADD COLUMN source TEXT NOT NULL DEFAULT 'admin'
        CHECK (source IN ('admin', 'login', 'backfill')),
    ADD COLUMN deactivated_at TIMESTAMPTZ,
    ADD COLUMN deactivated_by TEXT NOT NULL DEFAULT '';

INSERT INTO users (email, source)
SELECT email, 'backfill' FROM (
    SELECT lower(email) AS email FROM user_idp_groups
    UNION
    SELECT lower(creator) FROM artifacts WHERE creator <> ''
    UNION
    SELECT lower(m) FROM user_groups, unnest(members) AS m
    UNION
    SELECT lower(principal_id) FROM role_assignments WHERE principal_type = 'user'
    UNION
    SELECT lower(owner_email) FROM api_keys WHERE owner_email <> ''
    UNION
    SELECT lower(author) FROM comments WHERE author <> ''
    UNION
    SELECT lower(created_by) FROM comment_threads WHERE created_by <> ''
    UNION
    SELECT lower(resolved_by) FROM comment_threads
     WHERE resolved_by IS NOT NULL AND resolved_by <> ''
) known
WHERE email NOT LIKE '%*%'
  AND position('@' in email) > 1
ON CONFLICT (email) DO NOTHING;

-- +goose Down
DELETE FROM users WHERE source <> 'admin';
ALTER TABLE users
    DROP COLUMN deactivated_by,
    DROP COLUMN deactivated_at,
    DROP COLUMN source;
