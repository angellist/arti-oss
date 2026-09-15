-- 0028_notification_settings.sql
-- Who hears about what.
--
-- A notification is a message to one person, so the person decides. Each row
-- here is one recipient's choice about one category; an absent row means that
-- category's default (comment mentions on, credential notifications off — see
-- internal/notifysettings). Nothing is seeded, so the defaults live in code and
-- a deployment nobody has configured behaves the same as a fresh one.
--
-- app_settings holds the one deployment-wide switch, which an admin can use to
-- stop everything at once. It exists because on 2026-09-04 a notification
-- misfired to fifteen people and the only way to stop it was to roll the
-- service back.

-- +goose Up
CREATE TABLE app_settings (
    key        TEXT        PRIMARY KEY,
    enabled    BOOLEAN     NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT        NOT NULL
);

CREATE TABLE user_notification_settings (
    user_email TEXT        NOT NULL,
    key        TEXT        NOT NULL,
    enabled    BOOLEAN     NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_email, key)
);

-- +goose Down
DROP TABLE IF EXISTS user_notification_settings;
DROP TABLE IF EXISTS app_settings;
