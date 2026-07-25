-- name: GetIdempotencyKey :one
-- Returns the cached response for (key, creator) if a write happened
-- within the TTL window. Stale rows are filtered out at SELECT time
-- so cleanup can be lazy.
SELECT response_artifact_id, response_version, created_at
FROM idempotency_keys
WHERE idempotency_key = $1
  AND creator         = $2
  AND created_at      > now() - interval '24 hours';

-- name: InsertIdempotencyKey :execrows
-- Records (key, creator) → (artifact_id, version). ON CONFLICT DO
-- NOTHING + :execrows lets the caller distinguish "I won the race"
-- (rows=1) from "someone else got there first" (rows=0). On loss the
-- service layer re-fetches the winner via GetIdempotencyKey and
-- returns the winner's response, so concurrent writers with the same
-- key converge on a single canonical artifact_id even though both
-- writes physically happened (the loser's append survives as an
-- orphan version on the slug — acceptable cost for a rare race; the
-- alternative is a 2-phase reserve/finalize pattern that's not worth
-- the complexity for the actual collision rate).
INSERT INTO idempotency_keys (
    idempotency_key, creator, response_artifact_id, response_version
) VALUES ($1, $2, $3, $4)
ON CONFLICT (idempotency_key, creator) DO NOTHING;

-- name: DeleteExpiredIdempotencyKeys :execrows
-- Periodic cleanup. Run from a cron or admin endpoint; GetIdempotencyKey
-- already ignores stale rows so this is just for table-size hygiene.
DELETE FROM idempotency_keys
WHERE created_at <= now() - interval '24 hours';
