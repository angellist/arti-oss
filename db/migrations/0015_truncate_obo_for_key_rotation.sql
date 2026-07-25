-- 0015_truncate_obo_for_key_rotation.sql
-- One-shot data reset paired with provisioning a dedicated ARTI_OBO_ENC_KEY
-- (decoupling the OBO broker's envelope encryption from JWT_SIGNING_KEY).
--
-- Rotating the OBO master key makes every existing OBO ciphertext (the
-- envelope-encrypted upstream access/refresh tokens in oauth_obo_tokens and the
-- DCR client secrets in oauth_client_registrations) undecryptable under the new
-- key. Those rows must therefore be cleared in the SAME deploy: goose runs this
-- in the init container BEFORE arti-server boots with the new key, so there is
-- no window where the new server reads stale ciphertext. Without it,
-- GetToken/GetClient error on the old rows and surface as a sticky HTTP 500 on
-- the apps OBO proxy (internal/apps/apps.go) instead of a clean re-consent.
--
-- User impact: the (small, recent) set of users who authorized an OBO MCP
-- server from an arti APP artifact re-consent once (a 401 authorize_url prompt);
-- everyone else is unaffected. Safe + idempotent: truncating empty tables is a
-- no-op, and neither table is referenced by a foreign key (see 0008_oauth_obo).

-- +goose Up
TRUNCATE oauth_obo_tokens, oauth_client_registrations;

-- +goose Down
-- Irreversible by design: the deleted OBO ciphertext cannot be restored (and
-- could not be decrypted under the new key anyway). Down is an explicit no-op.
SELECT 1;
