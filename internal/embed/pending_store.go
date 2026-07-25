package embed

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PendingTokenStore hands a freshly-minted embed token from the mint route
// (which may run on a different replica) to the embedded app's poll request,
// keyed by the app's own (surface, state) nonce. Single-use + short-lived.
type PendingTokenStore interface {
	// Put stores token for (surface, state), overwriting any prior pending
	// token for the same key, with the given TTL.
	Put(ctx context.Context, surface, state, token, email string, ttl time.Duration) error
	// Take atomically returns and deletes the token for (surface, state) if one
	// exists and hasn't expired. Returns ("", nil) on a miss.
	Take(ctx context.Context, surface, state string) (token string, err error)
}

// PGPendingTokenStore is the Postgres-backed PendingTokenStore (raw pgx, no
// sqlc — a tiny two-query surface). Correct across the HPA's replicas.
type PGPendingTokenStore struct{ pool *pgxpool.Pool }

func NewPGPendingTokenStore(pool *pgxpool.Pool) *PGPendingTokenStore {
	return &PGPendingTokenStore{pool: pool}
}

func (s *PGPendingTokenStore) Put(ctx context.Context, surface, state, token, email string, ttl time.Duration) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO embed_pending_tokens (surface, state, token, email, expires_at)
		 VALUES ($1, $2, $3, $4, now() + $5::interval)
		 ON CONFLICT (surface, state) DO UPDATE
		   SET token = EXCLUDED.token, email = EXCLUDED.email,
		       created_at = now(), expires_at = EXCLUDED.expires_at`,
		surface, state, token, email, ttl.String())
	return err
}

func (s *PGPendingTokenStore) Take(ctx context.Context, surface, state string) (string, error) {
	// DELETE ... RETURNING makes the take atomic and single-use even if two
	// polls race: only one deletes the row and gets the token.
	var token string
	err := s.pool.QueryRow(ctx,
		`DELETE FROM embed_pending_tokens
		 WHERE surface = $1 AND state = $2 AND expires_at > now()
		 RETURNING token`,
		surface, state).Scan(&token)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return token, nil
}
