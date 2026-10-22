-- 0040_api_keys_minted_by.sql
-- Records who minted a key when it is not its owner. Self-serve keys are minted
-- by their owner; an admin mints a service principal's key, and the key's
-- record must still name the person who created it.

-- +goose Up
ALTER TABLE api_keys ADD COLUMN minted_by TEXT;

-- +goose Down
ALTER TABLE api_keys DROP COLUMN IF EXISTS minted_by;
