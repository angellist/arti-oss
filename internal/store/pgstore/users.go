package pgstore

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/angellist/arti-oss/internal/rbac"
)

// users.go — the Users roster behind GET /api/users, plus the deliberate
// registration record from migration 0023.
//
// The roster is a projection, never an authority. It reads the same
// knownPrincipalsCTE the access-editor typeahead reads (people.go), so the two
// surfaces cannot disagree about who exists, and it resolves roles and group
// membership through the same code the access checks use. Nothing here is
// consulted when deciding access.

// HeldRole is one role a principal holds and where it came from: "baseline"
// (the implicit USER role), "direct" (assigned to their email), or
// "group:<name>" (assigned to a group they belong to).
//
// A role held both directly and via a group is TWO entries, one per source —
// callers render source, so collapsing them would hide how someone got it.
type HeldRole struct {
	Name   string
	Source string
}

// UserKind distinguishes a person from a non-human principal. Only a row in
// `users` can carry it; a principal known only by derivation reports human.
const (
	UserKindHuman   = "human"
	UserKindService = "service"
)

// RosterUser is one principal on the Users page.
type RosterUser struct {
	Email string
	Kind  string
	Note  string

	Roles     []HeldRole
	Groups    []string // arti group names (bare, no `group:` prefix)
	IdPGroups []string // captured IdP group names (bare, no `idp:` prefix)

	// IdPFresh reports whether the IdP snapshot is inside
	// Config.IdPGroupsMaxAge. When false, the `idp:` groups listed above grant
	// nothing (callerIdPTokens ignores a stale snapshot, and ignores every
	// snapshot when the bound is <= 0), so a caller must not render them as
	// live access.
	IdPFresh bool

	// LastSeenAt is the IdP snapshot time, which is set at every interactive
	// login. Nil means no login was ever recorded for this principal — which is
	// NOT the same as "never signed in": user_idp_groups only starts at
	// migration 0020 (2026-07-27), so an older login left no row.
	LastSeenAt *time.Time

	// Registered reports whether a `users` row exists, i.e. somebody recorded
	// this principal deliberately rather than it being derived from activity.
	Registered bool
	AddedBy    string
	AddedAt    *time.Time
}

// composeHeldRoles is the single definition of "which roles does this principal
// hold, and from where". Pure, so the bulk roster and the single-email lookup
// cannot drift apart.
//
// live is the set of role names that currently exist: an assignment naming a
// since-deleted role resolves to nothing rather than a phantom entry.
func composeHeldRoles(direct []string, groups []string, byGroup map[string][]string, live map[string]struct{}) []HeldRole {
	out := make([]HeldRole, 0, len(direct)+len(groups)+1)
	seen := make(map[string]struct{}, len(direct)+len(groups)+1)
	add := func(name, source string) {
		if _, ok := live[name]; !ok {
			return
		}
		key := name + "\x00" + source
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, HeldRole{Name: name, Source: source})
	}
	add(rbac.RoleUser, "baseline")
	for _, r := range direct {
		add(r, "direct")
	}
	for _, g := range groups {
		for _, r := range byGroup[g] {
			add(r, GroupToken(g))
		}
	}
	return out
}

// HeldRoles returns one principal's roles with their sources. Shares
// composeHeldRoles with the roster so /api/role-lookup and /api/users can never
// report different sources for the same person.
func (s *Store) HeldRoles(ctx context.Context, email string) ([]HeldRole, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	live, err := s.liveRoleNames(ctx)
	if err != nil {
		return nil, err
	}
	direct, err := s.RolesAssignedTo(ctx, rbac.PrincipalUser, email)
	if err != nil {
		return nil, err
	}
	groups, err := s.GroupsForCaller(ctx, email)
	if err != nil {
		return nil, err
	}
	byGroup := make(map[string][]string, len(groups))
	for _, g := range groups {
		rs, err := s.RolesAssignedTo(ctx, rbac.PrincipalGroup, g)
		if err != nil {
			return nil, err
		}
		byGroup[g] = rs
	}
	return composeHeldRoles(direct, groups, byGroup, live), nil
}

func (s *Store) liveRoleNames(ctx context.Context) (map[string]struct{}, error) {
	roles, err := s.ListRoles(ctx)
	if err != nil {
		return nil, err
	}
	live := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		live[r.Name] = struct{}{}
	}
	return live, nil
}

// idpSnapshot is one row of user_idp_groups plus whether it is fresh enough to
// grant anything.
type idpSnapshot struct {
	Groups     []string
	CapturedAt time.Time
	Fresh      bool
}

// idpSnapshots reads every captured IdP snapshot and marks each fresh or stale
// against Config.IdPGroupsMaxAge — the SAME bound callerIdPTokens enforces,
// including its fail-closed case: a bound of zero or less disables IdP
// resolution entirely, so every snapshot is stale.
//
// Unlike callerIdPTokens this RETURNS stale rows rather than dropping them: the
// roster has to show a group that exists but no longer grants, and hiding it
// would understate what a login would restore.
func (s *Store) idpSnapshots(ctx context.Context) (map[string]idpSnapshot, error) {
	rows, err := s.pool.Query(ctx, `SELECT lower(email), groups, captured_at FROM user_idp_groups`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]idpSnapshot{}
	bound := s.cfg.IdPGroupsMaxAge
	now := time.Now()
	for rows.Next() {
		var email string
		var snap idpSnapshot
		if err := rows.Scan(&email, &snap.Groups, &snap.CapturedAt); err != nil {
			return nil, err
		}
		snap.Fresh = bound > 0 && !snap.CapturedAt.Before(now.Add(-bound))
		out[email] = snap
	}
	return out, rows.Err()
}

// ListUsers returns every principal arti knows, alphabetically. Six queries
// regardless of roster size: the shared CTE, the IdP snapshots, the `users`
// rows, all role assignments, all roles, and all groups. Per-principal work
// (glob matching, role composition) happens in Go against those sets, so the
// query count does not grow with the number of users.
func (s *Store) ListUsers(ctx context.Context) ([]RosterUser, error) {
	// 1. Who exists. Same CTE and same pattern-vs-person filter as the
	// typeahead, so a principal cannot appear on one surface and not the other.
	rows, err := s.pool.Query(ctx, `
		WITH known AS (`+knownPrincipalsCTE+`
		)
		SELECT email FROM known
		WHERE email NOT LIKE '%*%'
		  AND position('@' in email) > 1
		ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	emails := []string{}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		emails = append(emails, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	snaps, err := s.idpSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	registered, err := s.listRegistered(ctx)
	if err != nil {
		return nil, err
	}
	live, err := s.liveRoleNames(ctx)
	if err != nil {
		return nil, err
	}
	assignments, err := s.ListAssignments(ctx, "")
	if err != nil {
		return nil, err
	}
	directOf := map[string][]string{}
	byGroup := map[string][]string{}
	for _, a := range assignments {
		switch a.PrincipalType {
		case rbac.PrincipalUser:
			directOf[a.PrincipalID] = append(directOf[a.PrincipalID], a.RoleName)
		case rbac.PrincipalGroup:
			byGroup[a.PrincipalID] = append(byGroup[a.PrincipalID], a.RoleName)
		}
	}
	// Read the SAME cached snapshot the access checks read, not a fresh
	// ListGroups. A fresh read would leave the roster and /api/role-lookup
	// disagreeing about group-derived roles for up to the cache TTL after a
	// membership change (and indefinitely across replicas), which defeats the
	// point of sharing composeHeldRoles.
	groups, err := s.groupSnapshot(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]RosterUser, 0, len(emails))
	for _, email := range emails {
		u := RosterUser{Email: email, Kind: UserKindHuman}

		// Same helper CallerGroups uses, over the same snapshot.
		member := groupNamesFor(groups, email)
		sort.Strings(member)
		u.Groups = member
		u.Roles = composeHeldRoles(directOf[email], member, byGroup, live)

		if snap, ok := snaps[email]; ok {
			at := snap.CapturedAt
			u.LastSeenAt = &at
			u.IdPFresh = snap.Fresh
			u.IdPGroups = append([]string{}, snap.Groups...)
			sort.Strings(u.IdPGroups)
		}
		if reg, ok := registered[email]; ok {
			u.Registered = true
			u.Kind = reg.Kind
			u.Note = reg.Note
			u.AddedBy = reg.AddedBy
			at := reg.AddedAt
			u.AddedAt = &at
		}
		out = append(out, u)
	}
	return out, nil
}

type registeredUser struct {
	Kind    string
	Note    string
	AddedBy string
	AddedAt time.Time
}

func (s *Store) listRegistered(ctx context.Context) (map[string]registeredUser, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT lower(email), kind, note, added_by, added_at FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]registeredUser{}
	for rows.Next() {
		var email string
		var r registeredUser
		if err := rows.Scan(&email, &r.Kind, &r.Note, &r.AddedBy, &r.AddedAt); err != nil {
			return nil, err
		}
		out[email] = r
	}
	return out, rows.Err()
}

// emailRe is deliberately loose — arti grants to an address, and the identity
// provider is what decides whether one is real. It rejects only what would
// break the roster's own rules: a glob (which is a pattern, not a person) and
// anything without a local part and a domain.
var emailRe = regexp.MustCompile(`^[^\s*@]+@[^\s*@]+\.[^\s*@]+$`)

// AddUser records a principal deliberately. Idempotent on email: re-adding an
// existing row updates its kind, note and attribution rather than failing, so
// the caller can correct a typo'd note without a delete.
//
// This grants nothing. It records that a principal exists; roles are assigned
// separately, and no access decision reads this table.
func (s *Store) AddUser(ctx context.Context, email, kind, note, addedBy string) (RosterUser, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return RosterUser{}, fmt.Errorf("%w: email required", ErrInvalidInput)
	}
	if strings.Contains(email, "*") {
		return RosterUser{}, fmt.Errorf("%w: %q is a pattern, not a person", ErrInvalidInput, email)
	}
	if !emailRe.MatchString(email) {
		return RosterUser{}, fmt.Errorf("%w: %q is not a single email address", ErrInvalidInput, email)
	}
	switch kind {
	case "":
		kind = UserKindHuman
	case UserKindHuman, UserKindService:
	default:
		return RosterUser{}, fmt.Errorf("%w: kind must be %q or %q", ErrInvalidInput, UserKindHuman, UserKindService)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO users (email, kind, note, added_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (email) DO UPDATE
		   SET kind = EXCLUDED.kind, note = EXCLUDED.note, added_by = EXCLUDED.added_by`,
		email, kind, strings.TrimSpace(note), strings.ToLower(strings.TrimSpace(addedBy))); err != nil {
		return RosterUser{}, err
	}
	return s.GetRosterUser(ctx, email)
}

// GetRosterUser returns one principal's roster row, composed the same way
// ListUsers composes it.
func (s *Store) GetRosterUser(ctx context.Context, email string) (RosterUser, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	all, err := s.ListUsers(ctx)
	if err != nil {
		return RosterUser{}, err
	}
	for _, u := range all {
		if u.Email == email {
			return u, nil
		}
	}
	return RosterUser{}, ErrNotFound
}
