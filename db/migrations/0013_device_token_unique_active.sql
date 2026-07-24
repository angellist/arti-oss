-- 0013_device_token_unique_active.sql
-- Enforce one active (non-revoked) device-token family per user at the DB
-- level, case-insensitively. Closes a concurrency race where two simultaneous
-- `long` pickups for the same user could each insert a fresh active family
-- (revoke-then-insert is not atomic across requests). Replaces the non-unique
-- idx_device_token_email_active from 0012; the lookup it served still works.

-- +goose Up
DROP INDEX IF EXISTS idx_device_token_email_active;
CREATE UNIQUE INDEX idx_device_token_email_active ON device_token (LOWER(email)) WHERE NOT revoked;

-- +goose Down
DROP INDEX IF EXISTS idx_device_token_email_active;
CREATE INDEX idx_device_token_email_active ON device_token (email) WHERE NOT revoked;
