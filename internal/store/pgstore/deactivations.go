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
	emails     map[string]struct{}
	loadedAt   time.Time
	generation uint64
}

func (c *deactivationCache) invalidate() {
	c.mu.Lock()
	c.loadedAt = time.Time{}
	c.generation++
	c.mu.Unlock()
}

func (s *Store) deactivationSnap(ctx context.Context) (map[string]struct{}, error) {
	for {
		s.deactivations.mu.Lock()
		if s.authzCacheHealthy() && !s.deactivations.loadedAt.IsZero() && time.Since(s.deactivations.loadedAt) < deactivationCacheTTL {
			set := s.deactivations.emails
			s.deactivations.mu.Unlock()
			return set, nil
		}
		generation := s.deactivations.generation
		s.deactivations.mu.Unlock()

		rows, err := s.pool.Query(ctx, `SELECT lower(email) FROM users WHERE deactivated_at IS NOT NULL`)
		if err != nil {
			return nil, fmt.Errorf("pgstore: deactivations: %w", err)
		}
		set := map[string]struct{}{}
		for rows.Next() {
			var e string
			if err := rows.Scan(&e); err != nil {
				rows.Close()
				return nil, fmt.Errorf("pgstore: deactivations scan: %w", err)
			}
			set[e] = struct{}{}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("pgstore: deactivations iter: %w", err)
		}

		s.deactivations.mu.Lock()
		if generation != s.deactivations.generation {
			s.deactivations.mu.Unlock()
			continue
		}
		if s.authzCacheHealthy() {
			s.deactivations.emails, s.deactivations.loadedAt = set, time.Now()
		}
		s.deactivations.mu.Unlock()
		return set, nil
	}
}

// IsDeactivated is the hook auth.IsAllowed calls. It fails closed: if the
// table cannot be read, the email is treated as deactivated.
func (s *Store) IsDeactivated(email string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	set, err := s.deactivationSnap(ctx)
	if err != nil {
		slog.Default().Warn("pgstore: deactivation check failed; denying", "err", err)
		return true
	}
	_, ok := set[strings.ToLower(strings.TrimSpace(email))]
	return ok
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
