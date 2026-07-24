package pgstore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Group is a named collection of member emails. The token `group:<Name>` in an
// artifact's allowed_access grants read-access to the group's CURRENT members
// (resolved live — see CallerGroups + CanAccess).
type Group struct {
	Name        string
	DisplayName string
	Members     []string
	CreatedBy   string
	CreatedAt   time.Time
	ModifiedAt  time.Time
}

const groupTokenPrefix = "group:"

// GroupToken returns the allowed_access token for a group name.
func GroupToken(name string) string { return groupTokenPrefix + name }

// IsGroupToken reports whether an allowed_access pattern is a group reference,
// and returns the bare group name.
func IsGroupToken(pattern string) (string, bool) {
	if strings.HasPrefix(pattern, groupTokenPrefix) {
		return pattern[len(groupTokenPrefix):], true
	}
	return "", false
}

// groupNameRe constrains a group name to a lowercase slug so the
// `group:<name>` token is unambiguous and URL-safe.
var groupNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func normalizeGroupName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// normalizeMembers lowercases + trims members, drops blanks, and de-dupes.
// A member may be an exact email or a glob (`*@domain`, `*`); CallerGroups
// matches them with the same glob matcher used for allowed_access patterns.
func normalizeMembers(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, m := range in {
		e := strings.ToLower(strings.TrimSpace(m))
		if e == "" {
			continue
		}
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		out = append(out, e)
	}
	return out
}

// ─── cache ───────────────────────────────────────────────────────────
//
// The user_groups table is tiny (a handful of groups), so per-request access
// checks read an in-memory snapshot rather than hitting the DB. Writes
// invalidate immediately; the TTL only bounds staleness on OTHER server
// instances that didn't perform the write.

const groupCacheTTL = 15 * time.Second

type groupCache struct {
	mu       sync.Mutex
	snapshot []Group
	loadedAt time.Time
}

func (c *groupCache) invalidate() {
	c.mu.Lock()
	c.loadedAt = time.Time{}
	c.mu.Unlock()
}

// groupSnapshot returns the cached group list, reloading from the DB if the
// cache is empty or past its TTL.
func (s *Store) groupSnapshot(ctx context.Context) ([]Group, error) {
	s.groups.mu.Lock()
	if !s.groups.loadedAt.IsZero() && time.Since(s.groups.loadedAt) < groupCacheTTL {
		snap := s.groups.snapshot
		s.groups.mu.Unlock()
		return snap, nil
	}
	s.groups.mu.Unlock()

	// Reload outside the lock so a slow DB round-trip doesn't serialize every
	// concurrent access check. A redundant double-load under contention is
	// harmless (the result is identical).
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	s.groups.mu.Lock()
	s.groups.snapshot = groups
	s.groups.loadedAt = time.Now()
	s.groups.mu.Unlock()
	return groups, nil
}

// CallerGroups returns the set of group tokens ("group:<name>") whose current
// membership includes `caller` (case-insensitive). This is the inverse of
// expanding a row's tokens to members: computing it once per caller lets both
// CanAccess and the List SQL filter decide group access with a cheap set/array
// check. Empty caller → no groups.
func (s *Store) CallerGroups(ctx context.Context, caller string) ([]string, error) {
	caller = strings.ToLower(strings.TrimSpace(caller))
	if caller == "" {
		return nil, nil
	}
	snap, err := s.groupSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	var tokens []string
	for _, g := range snap {
		for _, m := range g.Members {
			// Members may be exact emails OR globs (`*@domain`, `*`), matched
			// with the same matcher as allowed_access patterns. Both sides are
			// already lowercased (normalizeMembers + the caller lowercasing
			// above), which is what matchGlob expects.
			if matchGlob(m, caller) {
				tokens = append(tokens, GroupToken(g.Name))
				break
			}
		}
	}
	return tokens, nil
}

// ─── CRUD ────────────────────────────────────────────────────────────

// ListGroups returns all groups ordered by name.
func (s *Store) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name, display_name, members, created_by, created_at, modified_at
		   FROM user_groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.Name, &g.DisplayName, &g.Members, &g.CreatedBy, &g.CreatedAt, &g.ModifiedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetGroup returns one group by name, or ErrNotFound.
func (s *Store) GetGroup(ctx context.Context, name string) (Group, error) {
	var g Group
	err := s.pool.QueryRow(ctx,
		`SELECT name, display_name, members, created_by, created_at, modified_at
		   FROM user_groups WHERE name = $1`, normalizeGroupName(name)).
		Scan(&g.Name, &g.DisplayName, &g.Members, &g.CreatedBy, &g.CreatedAt, &g.ModifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	return g, err
}

// CreateGroup inserts a new group. Returns ErrInvalidInput for a bad name and
// ErrConflict if the name is taken.
func (s *Store) CreateGroup(ctx context.Context, name, displayName string, members []string, createdBy string) (Group, error) {
	n := normalizeGroupName(name)
	if !groupNameRe.MatchString(n) {
		return Group{}, fmt.Errorf("%w: group name must be a lowercase slug (a-z, 0-9, -), 1-63 chars", ErrInvalidInput)
	}
	var g Group
	err := s.pool.QueryRow(ctx,
		`INSERT INTO user_groups (name, display_name, members, created_by)
		 VALUES ($1, $2, $3, $4)
		 RETURNING name, display_name, members, created_by, created_at, modified_at`,
		n, strings.TrimSpace(displayName), normalizeMembers(members), createdBy).
		Scan(&g.Name, &g.DisplayName, &g.Members, &g.CreatedBy, &g.CreatedAt, &g.ModifiedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return Group{}, fmt.Errorf("%w: group %q already exists", ErrConflict, n)
		}
		return Group{}, err
	}
	s.groups.invalidate()
	return g, nil
}

// UpdateGroup replaces a group's display name and members (the name is
// immutable). Returns ErrNotFound if the group doesn't exist.
func (s *Store) UpdateGroup(ctx context.Context, name, displayName string, members []string) (Group, error) {
	var g Group
	err := s.pool.QueryRow(ctx,
		`UPDATE user_groups
		    SET display_name = $2, members = $3, modified_at = now()
		  WHERE name = $1
		  RETURNING name, display_name, members, created_by, created_at, modified_at`,
		normalizeGroupName(name), strings.TrimSpace(displayName), normalizeMembers(members)).
		Scan(&g.Name, &g.DisplayName, &g.Members, &g.CreatedBy, &g.CreatedAt, &g.ModifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	if err != nil {
		return Group{}, err
	}
	s.groups.invalidate()
	return g, nil
}

// DeleteGroup removes a group. Dangling `group:<name>` tokens left in any
// artifact's allowed_access resolve to no members (fail-closed), so no
// cascade is needed. Returns ErrNotFound if the group doesn't exist.
func (s *Store) DeleteGroup(ctx context.Context, name string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM user_groups WHERE name = $1`, normalizeGroupName(name))
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.groups.invalidate()
	return nil
}
