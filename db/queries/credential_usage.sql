-- name: UpsertCredentialUsage :exec
-- Adds a flush's worth of counted requests to one (credential, day, source)
-- row. The aggregator batches in memory, so this runs on a timer rather than
-- per request.
INSERT INTO credential_usage (
    cred, day, ip, user_agent, owner_email, reads, writes
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (cred, owner_email, day, ip, user_agent) DO UPDATE
SET reads     = credential_usage.reads + EXCLUDED.reads,
    writes    = credential_usage.writes + EXCLUDED.writes,
    last_seen = now();

-- name: ListCredentialSourcesByOwner :many
-- One row per credential per source for this owner, over the given window.
SELECT cred, ip, user_agent,
       SUM(reads)::BIGINT  AS reads,
       SUM(writes)::BIGINT AS writes,
       MIN(first_seen)::TIMESTAMPTZ AS first_seen,
       MAX(last_seen)::TIMESTAMPTZ  AS last_seen
FROM credential_usage
WHERE LOWER(owner_email) = LOWER(sqlc.arg(owner_email)) AND day >= sqlc.arg(day)
GROUP BY cred, ip, user_agent
ORDER BY MAX(last_seen) DESC;

-- name: ListCredentialSources :many
-- The same, across every owner. Admin view (MANAGE_API_KEYS).
SELECT cred, owner_email, ip, user_agent,
       SUM(reads)::BIGINT  AS reads,
       SUM(writes)::BIGINT AS writes,
       MIN(first_seen)::TIMESTAMPTZ AS first_seen,
       MAX(last_seen)::TIMESTAMPTZ  AS last_seen
FROM credential_usage
WHERE day >= $1
GROUP BY cred, owner_email, ip, user_agent
ORDER BY MAX(last_seen) DESC;

-- name: CountArtifactsByCredential :many
-- How many live documents each of an owner's credentials has written. Powers
-- the "documents" column next to each key.
-- Counted per DOCUMENT, not per row: a slug appended fifty times is one
-- document, and the number is a link to a latest-per-slug search that would
-- otherwise disagree with it.
SELECT written_via, COUNT(DISTINCT COALESCE(named_slug, artifact_id::TEXT))::BIGINT AS docs
FROM artifacts
WHERE LOWER(creator) = LOWER(sqlc.arg(creator)) AND written_via IS NOT NULL AND deleted_at IS NULL
GROUP BY written_via;

-- name: CountArtifactsByCredentialAll :many
-- The same count for every credential, regardless of owner. Admin view.
SELECT written_via, COUNT(DISTINCT COALESCE(named_slug, artifact_id::TEXT))::BIGINT AS docs
FROM artifacts
WHERE written_via IS NOT NULL AND deleted_at IS NULL
GROUP BY written_via;

-- name: DeleteCredentialUsageBefore :execrows
-- Retention. Usage rows answer "where is this credential used lately"; a row
-- older than the window nobody can query is only cost, and an attacker who
-- forges source rows must not leave them behind for good.
DELETE FROM credential_usage WHERE day < sqlc.arg(day);

-- name: ClaimCredentialAlert :one
-- Claims the right to send one new-network alert, returning a row only to the
-- caller that won. Claiming and sending are the same decision: nothing marks an
-- alert delivered unless it is about to be delivered, so a burst held back by a
-- rate limit is simply retried on the next flush. Two pods racing the same
-- network: one claims, the other gets nothing.
INSERT INTO credential_alert (cred, owner_email, network)
VALUES (sqlc.arg(cred), LOWER(sqlc.arg(owner_email)), sqlc.arg(network))
ON CONFLICT (cred, owner_email, network) DO NOTHING
RETURNING first_alerted_at;
