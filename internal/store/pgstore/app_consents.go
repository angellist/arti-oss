package pgstore

import (
	"context"
	"fmt"
	"time"
)

// GrantAppConsent records that email agreed to run subject until until,
// refreshing the expiry of an earlier record.
func (s *Store) GrantAppConsent(ctx context.Context, email, subject string, until time.Time) error {
	const q = `
INSERT INTO app_consents (email, subject, expires_at) VALUES (lower($1), $2, $3)
ON CONFLICT (email, subject) DO UPDATE SET expires_at = EXCLUDED.expires_at`
	if _, err := s.pool.Exec(ctx, q, email, subject, until); err != nil {
		return fmt.Errorf("pgstore: grant app consent: %w", err)
	}
	return nil
}

// HasAppConsent reports whether email holds an unexpired record for any of
// subjects.
func (s *Store) HasAppConsent(ctx context.Context, email string, subjects ...string) (bool, error) {
	const q = `
SELECT EXISTS (SELECT 1 FROM app_consents
               WHERE email = lower($1) AND subject = ANY($2) AND expires_at > now())`
	var ok bool
	if err := s.pool.QueryRow(ctx, q, email, subjects).Scan(&ok); err != nil {
		return false, fmt.Errorf("pgstore: has app consent: %w", err)
	}
	return ok, nil
}
