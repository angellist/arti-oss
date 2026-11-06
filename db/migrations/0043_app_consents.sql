-- 0043_app_consents.sql
-- A viewer's yes to running an APP, keyed by subject: an artifact id (one
-- version) or a slug subject (every version by the same publisher with the
-- same tools). Replaces the per-app arti_app_ok_* cookies.

-- +goose Up
CREATE TABLE IF NOT EXISTS app_consents (
    email      TEXT        NOT NULL,
    subject    TEXT        NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (email, subject)
);

-- +goose Down
DROP TABLE IF EXISTS app_consents;
