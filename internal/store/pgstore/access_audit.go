package pgstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// access_audit.go — every stored credential one email holds, for the admin
// "Audit access" dialog. Browser sessions and CLI/MCP bearer tokens are signed
// and never stored, so they appear only in the usage rows.

type AuditAPIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

type AuditDeviceToken struct {
	FamilyID  string    `json:"family_id"`
	Scope     string    `json:"scope"`
	CreatedAt time.Time `json:"created_at"`
	RotatedAt time.Time `json:"rotated_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Revoked   bool      `json:"revoked"`
}

type AuditShareLink struct {
	ID           string     `json:"id"`
	TokenPrefix  string     `json:"token_prefix"`
	Target       string     `json:"target"` // slug, or artifact id for a version-pinned link
	Note         string     `json:"note"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	RevokedAt    *time.Time `json:"revoked_at"`
	LastOpenedAt *time.Time `json:"last_opened_at"`
	OpenCount    int64      `json:"open_count"`
}

// AuditConnection is an upstream MCP token arti holds for the user. Never
// select the encrypted token columns here.
type AuditConnection struct {
	Resource  string    `json:"resource"`
	Expiry    time.Time `json:"expiry"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AuditUsage struct {
	Cred      string    `json:"cred"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	Reads     int64     `json:"reads"`
	Writes    int64     `json:"writes"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

type UserAccess struct {
	Email        string             `json:"email"`
	APIKeys      []AuditAPIKey      `json:"api_keys"`
	DeviceTokens []AuditDeviceToken `json:"device_tokens"`
	ShareLinks   []AuditShareLink   `json:"share_links"`
	Connections  []AuditConnection  `json:"connections"`
	Usage        []AuditUsage       `json:"usage"`
	UsageDays    int                `json:"usage_days"`
}

// Credential kinds RevokeUserCredential accepts.
const (
	AuditKindAPIKey     = "apikey"
	AuditKindDevice     = "device"
	AuditKindShare      = "share"
	AuditKindConnection = "connection"
)

func (s *Store) UserAccess(ctx context.Context, email string, usageDays int) (UserAccess, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return UserAccess{}, fmt.Errorf("%w: email required", ErrInvalidInput)
	}
	out := UserAccess{Email: email, UsageDays: usageDays}
	var err error

	if out.APIKeys, err = collect(ctx, s, `
		SELECT id::text, name, key_prefix, scopes, created_at, expires_at, last_used_at, revoked_at
		  FROM api_keys WHERE lower(owner_email) = $1 ORDER BY created_at DESC`,
		func(r pgx.Rows, k *AuditAPIKey) error {
			return r.Scan(&k.ID, &k.Name, &k.KeyPrefix, &k.Scopes, &k.CreatedAt, &k.ExpiresAt, &k.LastUsedAt, &k.RevokedAt)
		}, email); err != nil {
		return UserAccess{}, err
	}
	if out.DeviceTokens, err = collect(ctx, s, `
		SELECT family_id, scope, created_at, rotated_at, expires_at, revoked
		  FROM device_token WHERE lower(email) = $1 ORDER BY created_at DESC`,
		func(r pgx.Rows, d *AuditDeviceToken) error {
			return r.Scan(&d.FamilyID, &d.Scope, &d.CreatedAt, &d.RotatedAt, &d.ExpiresAt, &d.Revoked)
		}, email); err != nil {
		return UserAccess{}, err
	}
	if out.ShareLinks, err = collect(ctx, s, `
		SELECT id::text, token_prefix, COALESCE(slug, artifact_id::text), note, created_at, expires_at,
		       revoked_at, last_opened_at, open_count
		  FROM share_links WHERE lower(created_by) = $1 ORDER BY created_at DESC`,
		func(r pgx.Rows, l *AuditShareLink) error {
			return r.Scan(&l.ID, &l.TokenPrefix, &l.Target, &l.Note, &l.CreatedAt, &l.ExpiresAt,
				&l.RevokedAt, &l.LastOpenedAt, &l.OpenCount)
		}, email); err != nil {
		return UserAccess{}, err
	}
	if out.Connections, err = collect(ctx, s, `
		SELECT resource, expiry, updated_at
		  FROM oauth_obo_tokens WHERE lower(email) = $1 ORDER BY updated_at DESC`,
		func(r pgx.Rows, c *AuditConnection) error {
			return r.Scan(&c.Resource, &c.Expiry, &c.UpdatedAt)
		}, email); err != nil {
		return UserAccess{}, err
	}
	if out.Usage, err = collect(ctx, s, `
		SELECT cred, ip, user_agent, SUM(reads)::bigint, SUM(writes)::bigint, MIN(first_seen), MAX(last_seen)
		  FROM credential_usage
		 WHERE lower(owner_email) = $1 AND day >= (now() AT TIME ZONE 'UTC')::date - $2::int
		 GROUP BY cred, ip, user_agent
		 ORDER BY MAX(last_seen) DESC`,
		func(r pgx.Rows, u *AuditUsage) error {
			return r.Scan(&u.Cred, &u.IP, &u.UserAgent, &u.Reads, &u.Writes, &u.FirstSeen, &u.LastSeen)
		}, email, usageDays); err != nil {
		return UserAccess{}, err
	}
	return out, nil
}

// RevokeUserCredential revokes one stored credential, scoped to the owner's
// email so an id that belongs to someone else matches nothing (ErrNotFound).
func (s *Store) RevokeUserCredential(ctx context.Context, email, kind, id, by string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || id == "" {
		return fmt.Errorf("%w: email and id required", ErrInvalidInput)
	}
	var q string
	args := []any{email, id}
	switch kind {
	case AuditKindAPIKey:
		q = `UPDATE api_keys SET revoked_at = now()
		      WHERE lower(owner_email) = $1 AND id::text = $2 AND revoked_at IS NULL`
	case AuditKindDevice:
		q = `UPDATE device_token SET revoked = TRUE
		      WHERE lower(email) = $1 AND family_id = $2 AND NOT revoked`
	case AuditKindShare:
		q = `UPDATE share_links SET revoked_at = now(), revoked_by = $3
		      WHERE lower(created_by) = $1 AND id::text = $2 AND revoked_at IS NULL`
		args = append(args, by)
	case AuditKindConnection:
		q = `DELETE FROM oauth_obo_tokens WHERE lower(email) = $1 AND resource = $2`
	default:
		return fmt.Errorf("%w: unknown credential kind %q", ErrInvalidInput, kind)
	}
	tag, err := s.pool.Exec(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("pgstore: revoke %s: %w", kind, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func collect[T any](ctx context.Context, s *Store, q string, scan func(pgx.Rows, *T) error, args ...any) ([]T, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pgstore: user access: %w", err)
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var v T
		if err := scan(rows, &v); err != nil {
			return nil, fmt.Errorf("pgstore: user access scan: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
