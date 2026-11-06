package pgstore

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Deactivation refuses every credential an email holds, enforced through
// auth.IsAllowed. It is a column on users and deletes nothing: keys, roles,
// groups, artifacts and the users row stay, so reactivating restores exactly
// the access the person had.

const deactivationCacheTTL = 15 * time.Second

type deactivationCache struct {
	mu         sync.Mutex
	snap       userGateSnap
	loadedAt   time.Time
	generation uint64
}

// userGateSnap is the users table as auth.IsAllowed reads it: who is
// deactivated, and who holds an account. An account is a login row or an
// admin row someone added; a backfilled row, or one only a deactivation
// created (no added_by), is a record, not an account.
type userGateSnap struct {
	deactivated map[string]struct{}
	accounts    map[string]bool // email → added by an admin (vs. recorded at login)
}

func (c *deactivationCache) invalidate() {
	c.mu.Lock()
	c.loadedAt = time.Time{}
	c.generation++
	c.mu.Unlock()
}

func (s *Store) userGateSnap(ctx context.Context) (userGateSnap, error) {
	for {
		s.deactivations.mu.Lock()
		if s.authzCacheHealthy() && !s.deactivations.loadedAt.IsZero() && time.Since(s.deactivations.loadedAt) < deactivationCacheTTL {
			snap := s.deactivations.snap
			s.deactivations.mu.Unlock()
			return snap, nil
		}
		generation := s.deactivations.generation
		s.deactivations.mu.Unlock()

		rows, err := s.pool.Query(ctx, `
			SELECT lower(email), deactivated_at IS NOT NULL, source = 'admin' FROM users
			 WHERE deactivated_at IS NOT NULL OR source = 'login' OR (source = 'admin' AND added_by <> '')`)
		if err != nil {
			return userGateSnap{}, fmt.Errorf("pgstore: deactivations: %w", err)
		}
		snap := userGateSnap{deactivated: map[string]struct{}{}, accounts: map[string]bool{}}
		for rows.Next() {
			var e string
			var off, added bool
			if err := rows.Scan(&e, &off, &added); err != nil {
				rows.Close()
				return userGateSnap{}, fmt.Errorf("pgstore: deactivations scan: %w", err)
			}
			if off {
				snap.deactivated[e] = struct{}{}
			} else {
				snap.accounts[e] = added
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return userGateSnap{}, fmt.Errorf("pgstore: deactivations iter: %w", err)
		}

		s.deactivations.mu.Lock()
		if generation != s.deactivations.generation {
			s.deactivations.mu.Unlock()
			continue
		}
		if s.authzCacheHealthy() {
			s.deactivations.snap, s.deactivations.loadedAt = snap, time.Now()
		}
		s.deactivations.mu.Unlock()
		return snap, nil
	}
}

// IsDeactivated is the hook auth.IsAllowed calls. It fails closed: if the
// table cannot be read, the email is treated as deactivated.
func (s *Store) IsDeactivated(email string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snap, err := s.userGateSnap(ctx)
	if err != nil {
		slog.Default().Warn("pgstore: deactivation check failed; denying", "err", err)
		return true
	}
	_, ok := snap.deactivated[strings.ToLower(strings.TrimSpace(email))]
	return ok
}

// Account is the auth.SetAccountCheck hook: whether email holds an account,
// and whether an admin added it (rather than a login recording it). It fails
// closed: if the table cannot be read, nobody holds an account.
func (s *Store) Account(email string) (has, added bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snap, err := s.userGateSnap(ctx)
	if err != nil {
		slog.Default().Warn("pgstore: account check failed; denying", "err", err)
		return false, false
	}
	added, has = snap.accounts[strings.ToLower(strings.TrimSpace(email))]
	return has, added
}

// HasAccount reports whether email holds an account of either kind.
func (s *Store) HasAccount(email string) bool {
	has, _ := s.Account(email)
	return has
}

// DeactivateUser is idempotent: deactivating an already-deactivated email
// keeps the original attribution.
func (s *Store) DeactivateUser(ctx context.Context, email, by string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if strings.Contains(email, "*") {
		return fmt.Errorf("%w: %q is a pattern, not a person", ErrInvalidInput, email)
	}
	if !emailRe.MatchString(email) {
		return fmt.Errorf("%w: %q is not a single email address", ErrInvalidInput, email)
	}
	// A principal with no row yet gets one; its added_by stays empty because
	// nobody added it, it was deactivated.
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO users (email, source, deactivated_at, deactivated_by)
		VALUES ($1, 'admin', now(), $2)
		ON CONFLICT (email) DO UPDATE
		   SET deactivated_at = now(), deactivated_by = EXCLUDED.deactivated_by
		 WHERE users.deactivated_at IS NULL`,
		email, strings.ToLower(strings.TrimSpace(by))); err != nil {
		return err
	}
	s.invalidateDeactivations(ctx)
	return nil
}

// ReactivateUser clears the deactivation and keeps the row. Returns
// ErrNotFound when arti has no row for the email.
func (s *Store) ReactivateUser(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	ct, err := s.pool.Exec(ctx,
		`UPDATE users SET deactivated_at = NULL, deactivated_by = '' WHERE email = $1`, email)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.invalidateDeactivations(ctx)
	return nil
}
