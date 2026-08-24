//go:build integration

package pgstore_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// rosterStore returns a store plus the raw pool, so a fixture can seed a table
// the store exposes no writer for (comments) or age a row past a bound.
func rosterStore(t *testing.T, cfg pgstore.Config) (*pgstore.Store, *pgxpool.Pool) {
	t.Helper()
	pool := newPool(t)
	return pgstore.New(pool, blob.NewInMemory(), cfg), pool
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %s: %v", sql, err)
	}
}

// rosterFor finds one email's row. The test DB is shared, so every test seeds
// unique emails and asserts on its own rows only — never on roster size.
func rosterFor(t *testing.T, st *pgstore.Store, email string) pgstore.RosterUser {
	t.Helper()
	all, err := st.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	for _, u := range all {
		if u.Email == email {
			return u
		}
	}
	t.Fatalf("roster has no row for %q", email)
	return pgstore.RosterUser{}
}

func rosterHas(t *testing.T, st *pgstore.Store, email string) bool {
	t.Helper()
	all, err := st.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	for _, u := range all {
		if u.Email == email {
			return true
		}
	}
	return false
}

// TestUsers_APIKeyOnlyPrincipalAppears pins criterion 2: a principal whose only
// trace is an API key must be on the roster. This is the population the roster
// exists to surface: a service account can own hundreds of artifacts and never
// sign in interactively, so a login-only roster would hide exactly the
// principal most worth auditing.
func TestUsers_APIKeyOnlyPrincipalAppears(t *testing.T) {
	ctx := context.Background()
	st, _ := rosterStore(t, pgstore.Config{})
	suffix := unique("svc")
	email := strings.ToLower(suffix) + "@example.com"

	if _, err := st.InsertAPIKey(ctx, sqlc.InsertAPIKeyParams{
		KeyHash:    hashKey("arti_upload_" + suffix),
		KeyPrefix:  "arti_upload_" + suffix[:5],
		OwnerEmail: email,
		Name:       "roster probe " + suffix,
		Scopes:     []string{"upload"},
		ExpiresAt:  pgTimestamptz(time.Now().Add(24 * time.Hour)),
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	u := rosterFor(t, st, email)
	if u.LastSeenAt != nil {
		t.Error("a key-only principal has no login, so LastSeenAt must be nil")
	}
	if u.Registered {
		t.Error("derived principals are not registered")
	}
	if u.Kind != pgstore.UserKindHuman {
		t.Errorf("kind for a derived principal = %q, want %q", u.Kind, pgstore.UserKindHuman)
	}
}

// TestUsers_CommentOnlyPrincipalAppears pins the arm added after review: a user
// whose only trace is a comment. Before migration 0020 (2026-07-27) logins left
// no row at all, so someone who commented and never came back is invisible
// unless the comment tables are read.
func TestUsers_CommentOnlyPrincipalAppears(t *testing.T) {
	ctx := context.Background()
	st, pool := rosterStore(t, pgstore.Config{})
	author := strings.ToLower(unique("commenter")) + "@example.com"
	opener := strings.ToLower(unique("opener")) + "@example.com"
	resolver := strings.ToLower(unique("resolver")) + "@example.com"

	row, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: "TEXT",
		Title:        "roster comment fixture", ContentType: "text/plain",
		Content: []byte("body"), Creator: strings.ToLower(unique("owner")) + "@example.com",
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	threadID := uuid.New()
	mustExec(t, pool,
		`INSERT INTO comment_threads (thread_id, artifact_id, anchor, created_by, status, resolved_by, resolved_at)
		 VALUES ($1, $2, '{"type":"doc"}'::jsonb, $3, 'resolved', $4, now())`,
		threadID, row.ArtifactID, opener, resolver)
	mustExec(t, pool,
		`INSERT INTO comments (comment_id, thread_id, author, body) VALUES ($1, $2, $3, 'hello')`,
		uuid.New(), threadID, author)

	if !rosterHas(t, st, author) {
		t.Errorf("roster is missing %q, whose only trace is writing a comment", author)
	}
	if !rosterHas(t, st, opener) {
		t.Errorf("roster is missing %q, whose only trace is opening a thread", opener)
	}
	if !rosterHas(t, st, resolver) {
		t.Errorf("roster is missing %q, whose only trace is resolving a thread", resolver)
	}

	// The reported source list must name every arm the union actually reads, or
	// a caller reading it to explain a missing principal is misled.
	for _, want := range []string{"comment_threads.created_by", "comment_threads.resolved_by"} {
		var named bool
		for _, s := range pgstore.KnownPrincipalSources {
			if s == want {
				named = true
			}
		}
		if !named {
			t.Errorf("KnownPrincipalSources omits %q, which the CTE reads", want)
		}
	}
}

// TestUsers_StaleIdPSnapshotIsMarked pins T2a. A snapshot older than the bound
// must still be LISTED (the group exists and a fresh login restores it) but
// marked not-fresh, because callerIdPTokens ignores it — a row rendered as live
// would claim access the checks would deny.
func TestUsers_StaleIdPSnapshotIsMarked(t *testing.T) {
	ctx := context.Background()
	st, pool := rosterStore(t, pgstore.Config{IdPGroupsMaxAge: time.Hour})
	fresh := strings.ToLower(unique("fresh")) + "@example.com"
	stale := strings.ToLower(unique("stale")) + "@example.com"

	if err := st.UpsertIdPGroups(ctx, fresh, []string{"eng"}); err != nil {
		t.Fatalf("UpsertIdPGroups: %v", err)
	}
	if err := st.UpsertIdPGroups(ctx, stale, []string{"eng"}); err != nil {
		t.Fatalf("UpsertIdPGroups: %v", err)
	}
	// Age the snapshot past the bound.
	mustExec(t, pool,
		`UPDATE user_idp_groups SET captured_at = now() - interval '48 hours' WHERE email = $1`, stale)

	if got := rosterFor(t, st, fresh); !got.IdPFresh {
		t.Error("a snapshot inside the bound must be fresh")
	}
	got := rosterFor(t, st, stale)
	if got.IdPFresh {
		t.Error("a snapshot past IdPGroupsMaxAge must NOT be fresh")
	}
	if len(got.IdPGroups) != 1 || got.IdPGroups[0] != "eng" {
		t.Errorf("stale row must still LIST its groups, got %v", got.IdPGroups)
	}
	if got.LastSeenAt == nil {
		t.Error("a stale snapshot still records when the login happened")
	}
}

// TestUsers_IdPBoundDisabledMarksEverythingStale pins T2b. With the bound at
// zero callerIdPTokens grants NO idp access at all (groups.go fails closed), so
// every snapshot must read stale however recent it is. A naive implementation
// that only compares timestamps renders these live.
func TestUsers_IdPBoundDisabledMarksEverythingStale(t *testing.T) {
	ctx := context.Background()
	st, _ := rosterStore(t, pgstore.Config{IdPGroupsMaxAge: 0})
	email := strings.ToLower(unique("nobound")) + "@example.com"
	if err := st.UpsertIdPGroups(ctx, email, []string{"eng"}); err != nil {
		t.Fatalf("UpsertIdPGroups: %v", err)
	}
	if rosterFor(t, st, email).IdPFresh {
		t.Error("with IdPGroupsMaxAge <= 0 no idp group grants anything, so none may read fresh")
	}
}

// TestUsers_RoleSourcesIncludeBothDirectAndGroup pins T2c + criterion 3 — the
// composite case, which is the only one where a reimplementation could diverge
// from /api/role-lookup. One principal holds ADMIN directly AND via a group, so
// the row must carry two entries for it, one per source, plus the baseline.
func TestUsers_RoleSourcesIncludeBothDirectAndGroup(t *testing.T) {
	ctx := context.Background()
	st, _ := rosterStore(t, pgstore.Config{})
	email := strings.ToLower(unique("both")) + "@example.com"
	group := strings.ToLower(unique("grp"))

	if _, err := st.CreateGroup(ctx, group, "roster fixture", []string{email}, "seed@example.com"); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if err := st.AssignRole(ctx, rbac.PrincipalUser, email, rbac.RoleAdmin, "seed@example.com"); err != nil {
		t.Fatalf("AssignRole user: %v", err)
	}
	if err := st.AssignRole(ctx, rbac.PrincipalGroup, group, rbac.RoleAdmin, "seed@example.com"); err != nil {
		t.Fatalf("AssignRole group: %v", err)
	}

	u := rosterFor(t, st, email)
	var sawDirect, sawGroup, sawBaseline bool
	for _, r := range u.Roles {
		switch {
		case r.Name == rbac.RoleUser && r.Source == "baseline":
			sawBaseline = true
		case r.Name == rbac.RoleAdmin && r.Source == "direct":
			sawDirect = true
		case r.Name == rbac.RoleAdmin && r.Source == "group:"+group:
			sawGroup = true
		}
	}
	if !sawBaseline {
		t.Error("every principal holds the USER baseline")
	}
	if !sawDirect || !sawGroup {
		t.Errorf("ADMIN must appear once per source; got %+v", u.Roles)
	}

	// The store's single-email path must agree with the bulk path, or the roster
	// and /api/role-lookup can report different sources for the same person.
	held, err := st.HeldRoles(ctx, email)
	if err != nil {
		t.Fatalf("HeldRoles: %v", err)
	}
	if len(held) != len(u.Roles) {
		t.Errorf("HeldRoles and ListUsers disagree: %d vs %d entries", len(held), len(u.Roles))
	}
}

// TestUsers_GlobMemberIsMembershipNotARow pins T2d + criterion 4. A `*@domain`
// member makes matching principals members of the group, but the pattern itself
// is not a person and must never become a roster row — offering it as one would
// invite granting a whole domain by accident.
//
// The group's member list holds ONLY the glob. An earlier version of this test
// listed the subject's exact address alongside it, which made the assertion pass
// with glob matching removed entirely — it proved nothing about globs. The
// subject therefore has to reach the roster by some other arm (a login here), so
// that the group on its row can only have come from the glob.
func TestUsers_GlobMemberIsMembershipNotARow(t *testing.T) {
	ctx := context.Background()
	st, _ := rosterStore(t, pgstore.Config{IdPGroupsMaxAge: time.Hour})
	domain := strings.ToLower(unique("d")) + ".example"
	email := "person@" + domain
	group := strings.ToLower(unique("globgrp"))
	glob := "*@" + domain

	// The subject exists because it signed in, NOT because it is named in the
	// group. Without this there would be no roster row to inspect — which is
	// itself correct: a glob match alone does not make somebody known to arti.
	if err := st.UpsertIdPGroups(ctx, email, nil); err != nil {
		t.Fatalf("UpsertIdPGroups: %v", err)
	}
	if _, err := st.CreateGroup(ctx, group, "glob fixture", []string{glob}, "seed@example.com"); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	u := rosterFor(t, st, email)
	var inGroup bool
	for _, g := range u.Groups {
		if g == group {
			inGroup = true
		}
	}
	if !inGroup {
		t.Errorf("the glob member %q must put %q in group %q; got groups %v",
			glob, email, group, u.Groups)
	}
	if rosterHas(t, st, glob) {
		t.Errorf("the glob %q is a pattern, not a principal, and must not be a roster row", glob)
	}

	// A non-matching address must NOT pick the group up, or the assertion above
	// would also pass for a matcher that returns true for everything.
	other := "person@" + strings.ToLower(unique("other")) + ".example"
	if err := st.UpsertIdPGroups(ctx, other, nil); err != nil {
		t.Fatalf("UpsertIdPGroups: %v", err)
	}
	for _, g := range rosterFor(t, st, other).Groups {
		if g == group {
			t.Errorf("%q does not match %q and must not be in group %q", other, glob, group)
		}
	}
}

// TestUsers_AddUserRecordsAndIsIdempotent pins the write half. AddUser is the
// ONLY way to record a principal at baseline privilege: AssignRole rejects the
// USER role, so a role assignment cannot express it.
func TestUsers_AddUserRecordsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st, _ := rosterStore(t, pgstore.Config{})
	email := strings.ToLower(unique("devin")) + "@example.com"

	u, err := st.AddUser(ctx, strings.ToUpper(email), pgstore.UserKindService, "platform team", "admin@example.com")
	if err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if u.Email != email {
		t.Errorf("email must be lowercased on save: got %q", u.Email)
	}
	if u.Kind != pgstore.UserKindService || u.Note != "platform team" || !u.Registered {
		t.Errorf("registration not recorded: %+v", u)
	}
	if u.AddedBy != "admin@example.com" {
		t.Errorf("added_by = %q", u.AddedBy)
	}
	// A principal recorded this way holds only the baseline.
	if len(u.Roles) != 1 || u.Roles[0].Source != "baseline" {
		t.Errorf("AddUser must grant nothing beyond the baseline; got %+v", u.Roles)
	}

	again, err := st.AddUser(ctx, email, pgstore.UserKindService, "corrected note", "admin@example.com")
	if err != nil {
		t.Fatalf("re-adding must update rather than fail: %v", err)
	}
	if again.Note != "corrected note" {
		t.Errorf("note not updated: %q", again.Note)
	}
}

// TestUsers_AddUserRejectsPatternsAndJunk keeps a pattern out of the table: the
// roster filters globs on read, but letting one in would put a row in `users`
// that can never render.
func TestUsers_AddUserRejectsPatternsAndJunk(t *testing.T) {
	ctx := context.Background()
	st, _ := rosterStore(t, pgstore.Config{})
	for _, bad := range []string{"", "   ", "*@example.com", "*", "not-an-email", "a@b"} {
		if _, err := st.AddUser(ctx, bad, "", "", "admin@example.com"); err == nil {
			t.Errorf("AddUser(%q) must be rejected", bad)
		}
	}
	if _, err := st.AddUser(ctx, "ok@example.com", "robot", "", "admin@example.com"); err == nil {
		t.Error("an unknown kind must be rejected")
	}
}
