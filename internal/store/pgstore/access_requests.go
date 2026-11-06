package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Values of access_requests.status (migration 0045).
const (
	AccessRequestPending  = "PENDING"
	AccessRequestApproved = "APPROVED"
	AccessRequestDenied   = "DENIED"
)

type AccessRequest struct {
	Email       string
	Name        string
	Status      string
	RequestedAt time.Time
	DecidedBy   string
	DecidedAt   *time.Time
}

// CreateAccessRequest records a request and reports whether it is new. A
// second request from the same email changes nothing, whatever the first one's
// status, so a resubmit never re-notifies and a decline is final.
func (s *Store) CreateAccessRequest(ctx context.Context, email, name string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if !emailRe.MatchString(email) {
		return false, fmt.Errorf("%w: %q is not a single email address", ErrInvalidInput, email)
	}
	if name == "" {
		return false, fmt.Errorf("%w: name required", ErrInvalidInput)
	}
	ct, err := s.pool.Exec(ctx,
		`INSERT INTO access_requests (email, name) VALUES ($1, $2) ON CONFLICT (email) DO NOTHING`,
		email, name)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// AccessRequestStatus returns the email's request status, or "" when it has
// never asked.
func (s *Store) AccessRequestStatus(ctx context.Context, email string) (string, error) {
	var status string
	err := s.pool.QueryRow(ctx, `SELECT status FROM access_requests WHERE email = $1`,
		strings.ToLower(strings.TrimSpace(email))).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return status, err
}

// ListPendingAccessRequests returns the open requests, oldest first.
func (s *Store) ListPendingAccessRequests(ctx context.Context) ([]AccessRequest, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT email, name, status, requested_at, COALESCE(decided_by, ''), decided_at
		  FROM access_requests WHERE status = $1 ORDER BY requested_at`, AccessRequestPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccessRequest{}
	for rows.Next() {
		var a AccessRequest
		if err := rows.Scan(&a.Email, &a.Name, &a.Status, &a.RequestedAt, &a.DecidedBy, &a.DecidedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DecideAccessRequest closes a pending request. Approving adds the account in
// the same transaction, so a request is never marked approved without one.
// Returns ErrNotFound when the email has no pending request.
func (s *Store) DecideAccessRequest(ctx context.Context, email string, approve bool, by string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	by = strings.ToLower(strings.TrimSpace(by))
	status := AccessRequestDenied
	if approve {
		status = AccessRequestApproved
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ct, err := tx.Exec(ctx, `
		UPDATE access_requests SET status = $2, decided_by = $3, decided_at = now()
		 WHERE email = $1 AND status = $4`, email, status, by, AccessRequestPending)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if approve {
		if err := upsertAdminUser(ctx, tx, email, UserKindHuman, "approved access request", by); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if approve {
		s.invalidateDeactivations(ctx)
	}
	return nil
}
