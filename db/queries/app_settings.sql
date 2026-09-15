-- name: ListAppSettings :many
-- The deployment-wide switches. Absent keys take their default, so the caller
-- fills those in rather than the database seeding them.
SELECT key, enabled, updated_at, updated_by FROM app_settings ORDER BY key;

-- name: UpsertAppSetting :exec
INSERT INTO app_settings (key, enabled, updated_by)
VALUES (sqlc.arg(key), sqlc.arg(enabled), sqlc.arg(updated_by))
ON CONFLICT (key) DO UPDATE
SET enabled    = EXCLUDED.enabled,
    updated_by = EXCLUDED.updated_by,
    updated_at = now();

-- name: ListUserNotificationSettings :many
-- Every person's choices, in one read. The table holds a row only for someone
-- who changed something from the default, so it stays small enough to cache
-- whole rather than querying per recipient while fanning a notification out.
SELECT user_email, key, enabled FROM user_notification_settings;

-- name: UpsertUserNotificationSetting :exec
INSERT INTO user_notification_settings (user_email, key, enabled)
VALUES (LOWER(sqlc.arg(user_email)), sqlc.arg(key), sqlc.arg(enabled))
ON CONFLICT (user_email, key) DO UPDATE
SET enabled = EXCLUDED.enabled, updated_at = now();
