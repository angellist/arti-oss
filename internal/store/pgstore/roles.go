package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/angellist/arti-oss/internal/rbac"
)

// Role is a named set of opaque permission keys.
type Role struct {
	Name        string
	Description string
	Permissions []string
	CreatedAt   time.Time
	ModifiedAt  time.Time
}

// RoleAssignment binds a principal (a user email or a group name) to a role.
type RoleAssignment struct {
	PrincipalType string // rbac.PrincipalUser | rbac.PrincipalGroup
	PrincipalID   string
	RoleName      string
	CreatedBy     string
	CreatedAt     time.Time
}

// ─── cache ───────────────────────────────────────────────────────────
//
// roles + role_assignments are tiny, so per-request permission resolution
// reads an in-memory snapshot while the cross-replica invalidation listener is
// healthy. Writes invalidate locally and publish a notification; the TTL
// remains a backstop for notifications missed while another instance restarts.

const rbacCacheTTL = 15 * time.Second

type rbacCache struct {
	mu         sync.Mutex
	roles      map[string][]string // role name → permissions
	byUser     map[string][]string // lowercased email → role names
	byGroup    map[string][]string // group name → role names
	loadedAt   time.Time
	generation uint64
}

func (c *rbacCache) invalidate() {
	c.mu.Lock()
	c.loadedAt = time.Time{}
	c.generation++
	c.mu.Unlock()
}

type rbacSnapshot struct {
	roles   map[string][]string
	byUser  map[string][]string
	byGroup map[string][]string
}

func (s *Store) rbacSnap(ctx context.Context) (rbacSnapshot, error) {
	for {
		s.rbac.mu.Lock()
		if s.authzCacheHealthy() && !s.rbac.loadedAt.IsZero() && time.Since(s.rbac.loadedAt) < rbacCacheTTL {
			snap := rbacSnapshot{roles: s.rbac.roles, byUser: s.rbac.byUser, byGroup: s.rbac.byGroup}
			s.rbac.mu.Unlock()
			return snap, nil
		}
		generation := s.rbac.generation
		s.rbac.mu.Unlock()

		// Reload outside the lock so a slow DB round-trip doesn't serialize
		// every concurrent permission check.
		roles, err := s.listRolesMap(ctx)
		if err != nil {
			return rbacSnapshot{}, err
		}
		byUser, byGroup, err := s.loadAssignments(ctx)
		if err != nil {
			return rbacSnapshot{}, err
		}
		s.rbac.mu.Lock()
		if generation != s.rbac.generation {
			s.rbac.mu.Unlock()
			continue
		}
		if s.authzCacheHealthy() {
			s.rbac.roles, s.rbac.byUser, s.rbac.byGroup, s.rbac.loadedAt = roles, byUser, byGroup, time.Now()
		}
		s.rbac.mu.Unlock()
		return rbacSnapshot{roles: roles, byUser: byUser, byGroup: byGroup}, nil
	}
}

func (s *Store) listRolesMap(ctx context.Context) (map[string][]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT name, permissions FROM roles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var name string
		var perms []string
		if err := rows.Scan(&name, &perms); err != nil {
			return nil, err
		}
		out[name] = perms
	}
	return out, rows.Err()
}

func (s *Store) loadAssignments(ctx context.Context) (byUser, byGroup map[string][]string, err error) {
	rows, err := s.pool.Query(ctx, `SELECT principal_type, principal_id, role_name FROM role_assignments`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byUser, byGroup = map[string][]string{}, map[string][]string{}
	for rows.Next() {
		var ptype, pid, role string
		if err := rows.Scan(&ptype, &pid, &role); err != nil {
			return nil, nil, err
		}
		switch ptype {
		case rbac.PrincipalUser:
			byUser[pid] = append(byUser[pid], role)
		case rbac.PrincipalGroup:
			byGroup[pid] = append(byGroup[pid], role)
		}
	}
	return byUser, byGroup, rows.Err()
}

// ─── resolution ──────────────────────────────────────────────────────

// callerRolesFromSnap computes the role names a caller holds from a SINGLE
// snapshot: the implicit USER baseline, plus roles assigned to their email,
// plus roles assigned to any group they belong to. Deduped; assignments to a
// since-deleted role are dropped. Taking one snapshot (rather than reading it
// twice across CallerRoles + EffectivePermissions) avoids briefly mixing role
// names from one cache generation with permissions from another.
func callerRolesFromSnap(snap rbacSnapshot, caller string, groups []string) []string {
	set := map[string]struct{}{rbac.RoleUser: {}}
	for _, r := range snap.byUser[caller] {
		set[r] = struct{}{}
	}
	for _, token := range groups {
		if name, ok := IsGroupToken(token); ok {
			for _, r := range snap.byGroup[name] {
				set[r] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for r := range set {
		if _, live := snap.roles[r]; live || r == rbac.RoleUser {
			out = append(out, r)
		}
	}
	return out
}

// CallerRoles returns the role names a caller holds (USER baseline ∪ direct ∪
// group-derived). Empty caller → no roles (not even USER — an empty caller
// isn't an authenticated user).
func (s *Store) CallerRoles(ctx context.Context, caller string) ([]string, error) {
	caller = strings.ToLower(strings.TrimSpace(caller))
	if caller == "" {
		return nil, nil
	}
	snap, err := s.rbacSnap(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := s.CallerGroups(ctx, caller)
	if err != nil {
		return nil, err
	}
	return callerRolesFromSnap(snap, caller, groups), nil
}

// EffectivePermissions returns the set of permission keys a caller holds — the
// union of the permissions of every role they hold. Role names AND their
// permissions come from the SAME snapshot so a concurrent role/assignment edit
// can't briefly attach stale permissions.
func (s *Store) EffectivePermissions(ctx context.Context, caller string) (map[string]struct{}, error) {
	caller = strings.ToLower(strings.TrimSpace(caller))
	perms := map[string]struct{}{}
	if caller == "" {
		return perms, nil
	}
	snap, err := s.rbacSnap(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := s.CallerGroups(ctx, caller)
	if err != nil {
		return nil, err
	}
	for _, r := range callerRolesFromSnap(snap, caller, groups) {
		for _, p := range snap.roles[r] {
			perms[p] = struct{}{}
		}
	}
	return perms, nil
}

// CallerAccess returns the caller's role names AND effective permission set
// computed from a SINGLE rbac snapshot — for callers (e.g. /api/me) that need
// both and must not pair values from two cache generations.
func (s *Store) CallerAccess(ctx context.Context, caller string) (roleNames []string, perms map[string]struct{}, err error) {
	caller = strings.ToLower(strings.TrimSpace(caller))
	perms = map[string]struct{}{}
	if caller == "" {
		return nil, perms, nil
	}
	snap, err := s.rbacSnap(ctx)
	if err != nil {
		return nil, nil, err
	}
	groups, err := s.CallerGroups(ctx, caller)
	if err != nil {
		return nil, nil, err
	}
	roleNames = callerRolesFromSnap(snap, caller, groups)
	for _, r := range roleNames {
		for _, p := range snap.roles[r] {
			perms[p] = struct{}{}
		}
	}
	return roleNames, perms, nil
}

// HasPermission reports whether the caller holds permission p. This is the
// per-request capability check that replaces auth.IsAdmin across the codebase.
func (s *Store) HasPermission(ctx context.Context, caller string, p rbac.Permission) (bool, error) {
	perms, err := s.EffectivePermissions(ctx, caller)
	if err != nil {
		return false, err
	}
	_, ok := perms[string(p)]
	return ok, nil
}

// ─── roles CRUD ──────────────────────────────────────────────────────

func scanRole(row pgx.Row) (Role, error) {
	var r Role
	err := row.Scan(&r.Name, &r.Description, &r.Permissions, &r.CreatedAt, &r.ModifiedAt)
	return r, err
}

// ListRoles returns all roles ordered by name.
func (s *Store) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name, description, permissions, created_at, modified_at FROM roles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRole returns one role by name, or ErrNotFound.
func (s *Store) GetRole(ctx context.Context, name string) (Role, error) {
	r, err := scanRole(s.pool.QueryRow(ctx,
		`SELECT name, description, permissions, created_at, modified_at FROM roles WHERE name = $1`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return Role{}, ErrNotFound
	}
	return r, err
}

// validatePermissions rejects unknown permission keys so a role's opaque
// strings stay meaningful, and de-dupes.
func validatePermissions(perms []string) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !rbac.IsKnownPermission(p) {
			return nil, fmt.Errorf("%w: unknown permission %q", ErrInvalidInput, p)
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out, nil
}

// CreateRole inserts a custom role. Returns ErrInvalidInput (bad name/unknown
// permission) or ErrConflict (name taken).
func (s *Store) CreateRole(ctx context.Context, name, description string, perms []string) (Role, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Role{}, fmt.Errorf("%w: role name required", ErrInvalidInput)
	}
	vperms, err := validatePermissions(perms)
	if err != nil {
		return Role{}, err
	}
	r, err := scanRole(s.pool.QueryRow(ctx,
		`INSERT INTO roles (name, description, permissions) VALUES ($1, $2, $3)
		 RETURNING name, description, permissions, created_at, modified_at`,
		name, strings.TrimSpace(description), vperms))
	if err != nil {
		if isUniqueViolation(err) {
			return Role{}, fmt.Errorf("%w: role %q already exists", ErrConflict, name)
		}
		return Role{}, err
	}
	s.invalidateRBAC(ctx)
	return r, nil
}

// UpdateRole replaces a role's description + permission set (built-in roles are
// editable here; only deletion is blocked). Returns ErrNotFound if missing.
func (s *Store) UpdateRole(ctx context.Context, name, description string, perms []string) (Role, error) {
	vperms, err := validatePermissions(perms)
	if err != nil {
		return Role{}, err
	}
	// Built-in roles are fixed: USER is the baseline everyone holds and ADMIN is
	// full access by definition, so neither their permissions nor description
	// are editable (this also makes the ADMIN lockout impossible — its
	// permission set can never be narrowed). Custom roles remain editable.
	if rbac.IsBuiltinRole(name) {
		return Role{}, fmt.Errorf("%w: built-in role %q is not editable", ErrInvalidInput, name)
	}
	r, err := scanRole(s.pool.QueryRow(ctx,
		`UPDATE roles SET description = $2, permissions = $3, modified_at = now()
		 WHERE name = $1
		 RETURNING name, description, permissions, created_at, modified_at`,
		name, strings.TrimSpace(description), vperms))
	if errors.Is(err, pgx.ErrNoRows) {
		return Role{}, ErrNotFound
	}
	if err != nil {
		return Role{}, err
	}
	s.invalidateRBAC(ctx)
	return r, nil
}

// DeleteRole removes a custom role (its assignments cascade). Built-in roles
// (ADMIN/USER) are rejected with ErrInvalidInput. Returns ErrNotFound if missing.
func (s *Store) DeleteRole(ctx context.Context, name string) error {
	if rbac.IsBuiltinRole(name) {
		return fmt.Errorf("%w: built-in role %q cannot be deleted", ErrInvalidInput, name)
	}
	ct, err := s.pool.Exec(ctx, `DELETE FROM roles WHERE name = $1`, name)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.invalidateRBAC(ctx)
	return nil
}

// ─── assignments ─────────────────────────────────────────────────────

// ListAssignments returns role assignments, optionally filtered by role name.
func (s *Store) ListAssignments(ctx context.Context, roleName string) ([]RoleAssignment, error) {
	q := `SELECT principal_type, principal_id, role_name, created_by, created_at FROM role_assignments`
	args := []any{}
	if roleName != "" {
		q += ` WHERE role_name = $1`
		args = append(args, roleName)
	}
	q += ` ORDER BY role_name, principal_type, principal_id`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RoleAssignment{}
	for rows.Next() {
		var a RoleAssignment
		if err := rows.Scan(&a.PrincipalType, &a.PrincipalID, &a.RoleName, &a.CreatedBy, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// normalizePrincipalID lowercases + trims a principal id and, for a group
// principal, strips a pasted "group:<slug>" token down to the bare slug — so a
// token copied from the access editor or elsewhere still resolves to the group.
func normalizePrincipalID(principalType, principalID string) string {
	id := strings.ToLower(strings.TrimSpace(principalID))
	if principalType == rbac.PrincipalGroup {
		if name, ok := IsGroupToken(id); ok {
			id = name
		}
	}
	return id
}

// AssignRole binds a principal to a role (idempotent). The role must exist;
// principalType must be 'user' or 'group'. Emails are lowercased.
func (s *Store) AssignRole(ctx context.Context, principalType, principalID, roleName, createdBy string) error {
	if roleName == rbac.RoleUser {
		// USER is the implicit baseline everyone already holds — assigning it
		// is meaningless, so reject it rather than store dead rows.
		return fmt.Errorf("%w: the USER role applies to everyone and cannot be assigned", ErrInvalidInput)
	}
	if principalType != rbac.PrincipalUser && principalType != rbac.PrincipalGroup {
		return fmt.Errorf("%w: principal_type must be 'user' or 'group'", ErrInvalidInput)
	}
	principalID = normalizePrincipalID(principalType, principalID)
	if principalID == "" {
		return fmt.Errorf("%w: principal_id required", ErrInvalidInput)
	}
	if _, err := s.GetRole(ctx, roleName); err != nil {
		return err // ErrNotFound for an unknown role
	}
	// A group principal must reference an existing group (otherwise the
	// assignment is dead — it can never resolve to any member).
	if principalType == rbac.PrincipalGroup {
		if _, err := s.GetGroup(ctx, principalID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return fmt.Errorf("%w: group %q does not exist", ErrInvalidInput, principalID)
			}
			return err
		}
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO role_assignments (principal_type, principal_id, role_name, created_by)
		 VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
		principalType, principalID, roleName, createdBy)
	if err != nil {
		return err
	}
	s.invalidateRBAC(ctx)
	return nil
}

// UnassignRole removes a principal↔role binding. No error if it didn't exist.
func (s *Store) UnassignRole(ctx context.Context, principalType, principalID, roleName string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM role_assignments WHERE principal_type = $1 AND principal_id = $2 AND role_name = $3`,
		principalType, normalizePrincipalID(principalType, principalID), roleName)
	if err != nil {
		return err
	}
	s.invalidateRBAC(ctx)
	return nil
}

// RolesAssignedTo returns the role names assigned directly to one principal
// (no baseline, no group expansion). Used by the per-user lookup to label
// where each of a person's roles comes from.
func (s *Store) RolesAssignedTo(ctx context.Context, principalType, principalID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT role_name FROM role_assignments WHERE principal_type = $1 AND principal_id = $2 ORDER BY role_name`,
		principalType, strings.ToLower(strings.TrimSpace(principalID)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GroupsForCaller returns the bare group names a caller belongs to (not the
// `group:` tokens). Used by the per-user role lookup to attribute group-derived
// roles to their group.
func (s *Store) GroupsForCaller(ctx context.Context, caller string) ([]string, error) {
	tokens, err := s.CallerGroups(ctx, caller)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if name, ok := IsGroupToken(tok); ok {
			out = append(out, name)
		}
	}
	return out, nil
}

// EnsureAdminAssignments idempotently asserts an ADMIN role assignment for each
// of the given emails. Called at startup with ARTI_ADMIN_EMAILS so config stays
// the lockout-proof floor for admin access.
//
// This is ADDITIVE by design (config is a floor, not an authoritative sync):
// it only ensures the listed emails are admins, and never prunes assignments.
// Removing an email from ARTI_ADMIN_EMAILS does NOT revoke their ADMIN role —
// that's intentional so API/UI-granted admins persist across restarts. To
// remove an admin, unassign the ADMIN role from them via the roles UI/API.
func (s *Store) EnsureAdminAssignments(ctx context.Context, emails []string) error {
	for _, e := range emails {
		if err := s.AssignRole(ctx, rbac.PrincipalUser, e, rbac.RoleAdmin, "system:bootstrap"); err != nil {
			return err
		}
	}
	return nil
}
