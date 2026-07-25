-- name: InsertDeviceCode :exec
INSERT INTO device_auth (device_code, user_code, duration, expires_at)
VALUES ($1, $2, $3, $4);

-- name: GetDeviceByUserCode :one
SELECT * FROM device_auth WHERE user_code = $1 AND expires_at > now();

-- name: GetDeviceCode :one
SELECT * FROM device_auth WHERE device_code = $1;

-- name: ApproveDeviceCode :exec
UPDATE device_auth SET status = 'approved', email = $2
WHERE user_code = $1 AND status = 'pending' AND expires_at > now();

-- name: TakeApprovedDeviceCode :one
-- single-use pickup: flips approved -> consumed atomically.
UPDATE device_auth SET status = 'consumed', consumed = TRUE
WHERE device_code = $1 AND status = 'approved' AND expires_at > now()
RETURNING *;

-- name: InsertDeviceTokenFamily :exec
INSERT INTO device_token (family_id, email, current_refresh_jti, expires_at)
VALUES ($1, $2, $3, $4);

-- name: GetDeviceTokenFamily :one
SELECT * FROM device_token WHERE family_id = $1;

-- name: RotateDeviceTokenRefresh :execrows
-- Compare-and-swap on the presented (old) jti so two concurrent refreshes
-- using the same refresh token can't both rotate — only the one matching the
-- current jti wins; the loser gets 0 rows affected and is rejected.
UPDATE device_token SET current_refresh_jti = @new_jti, rotated_at = now()
WHERE family_id = @family_id AND current_refresh_jti = @prev_jti
  AND NOT revoked AND expires_at > now();

-- name: RevokeDeviceTokensForEmail :exec
-- Case-insensitive: approver emails arrive from SSO headers and revoke callers
-- may pass different casing; match how the rest of auth treats email identity.
UPDATE device_token SET revoked = TRUE WHERE LOWER(email) = LOWER($1) AND NOT revoked;

-- name: RevokeDeviceTokenFamily :exec
UPDATE device_token SET revoked = TRUE WHERE family_id = $1;
