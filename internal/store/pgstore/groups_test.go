//go:build integration

package pgstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestGroups_LiveResolution is the heart of the feature: a group reference is
// resolved against CURRENT membership, so editing a group changes who has
// access immediately — no re-stamping of artifacts. It also pins the input
// normalization (lowercase + trim + dedupe) that lets membership compare by ==.
func TestGroups_LiveResolution(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	name := unique("eng")
	token := pgstore.GroupToken(name)

	g, err := st.CreateGroup(ctx, name, "Engineering", []string{"Dev@A.com", "dev@a.com", "  "}, "admin@a.com")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(g.Members) != 1 || g.Members[0] != "dev@a.com" {
		t.Fatalf("members must be lowercased/trimmed/deduped, got %v", g.Members)
	}

	// A member carries the token (case-insensitively); a stranger does not.
	if tk, err := st.CallerGroups(ctx, "DEV@a.com"); err != nil || !contains(tk, token) {
		t.Fatalf("member must carry the group token, got %v (err %v)", tk, err)
	}
	if tk, _ := st.CallerGroups(ctx, "stranger@a.com"); contains(tk, token) {
		t.Fatal("a non-member must not carry the group token")
	}

	// LIVE: drop the member — the very next resolution reflects it (the cache
	// is invalidated on write). This is what "reference, resolved live" means.
	if _, err := st.UpdateGroup(ctx, name, "Engineering", []string{"other@a.com"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if tk, _ := st.CallerGroups(ctx, "dev@a.com"); contains(tk, token) {
		t.Fatal("a removed member must lose access immediately")
	}

	// Deleting the group leaves any `group:<name>` tokens dangling — they must
	// resolve to nobody (fail-closed).
	if err := st.DeleteGroup(ctx, name); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if tk, _ := st.CallerGroups(ctx, "other@a.com"); contains(tk, token) {
		t.Fatal("a deleted group must grant no one")
	}
}

// TestGroups_GlobMembers pins that a group member may be a glob (`*@domain`),
// not just an exact email — so a whole domain can be a member, matched the
// same way allowed_access domain globs are.
func TestGroups_GlobMembers(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	name := unique("dom")
	token := pgstore.GroupToken(name)
	if _, err := st.CreateGroup(ctx, name, "", []string{"*@example.org", "alice@example.com"}, "admin@a.com"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Anyone at the glob domain is a member...
	if tk, _ := st.CallerGroups(ctx, "Gabe@example.org"); !contains(tk, token) {
		t.Error("a *@example.org member glob must match any belltower caller")
	}
	// ...as is the explicitly-listed email...
	if tk, _ := st.CallerGroups(ctx, "alice@example.com"); !contains(tk, token) {
		t.Error("an exact-email member must still match")
	}
	// ...but an unrelated domain is not.
	if tk, _ := st.CallerGroups(ctx, "bob@other.com"); contains(tk, token) {
		t.Error("a caller outside the member glob/list must not match")
	}
}

// TestGroups_NameValidationAndConflict pins the slug rule (so tokens stay
// unambiguous) and the duplicate-name guard.
func TestGroups_NameValidationAndConflict(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	for _, bad := range []string{"", "has space", "bad_underscore", "-leading", "UPPER CASE WITH SPACE"} {
		if _, err := st.CreateGroup(ctx, bad, "", nil, "a@a.com"); !errors.Is(err, pgstore.ErrInvalidInput) {
			t.Errorf("name %q must be rejected as invalid, got %v", bad, err)
		}
	}

	name := unique("dup")
	if _, err := st.CreateGroup(ctx, name, "", nil, "a@a.com"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := st.CreateGroup(ctx, name, "", nil, "a@a.com"); !errors.Is(err, pgstore.ErrConflict) {
		t.Fatalf("duplicate name must conflict, got %v", err)
	}
}

// TestGetLatestBySlugForCaller_GroupAccess covers the THIRD enforcement path:
// the `/s/<slug>` (no version pinned) lookup for a non-admin runs its own SQL
// access clause, which must honor group grants just like List and CanAccess —
// otherwise a member can list a group artifact but 404s opening it.
func TestGetLatestBySlugForCaller_GroupAccess(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	gname := unique("bs")
	if _, err := st.CreateGroup(ctx, gname, "", []string{"member@a.com"}, "admin@a.com"); err != nil {
		t.Fatalf("create group: %v", err)
	}
	slug := unique("bs-slug")
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType:  pgstore.TypeText,
		Title:         "g",
		ContentType:   "text/plain",
		Content:       []byte("x"),
		Creator:       "owner@a.com",
		NamedSlug:     &slug,
		AllowedAccess: []string{pgstore.GroupToken(gname)},
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	if _, err := st.GetLatestBySlugForCaller(ctx, slug, "member@a.com"); err != nil {
		t.Fatalf("member must resolve a group-granted slug, got %v", err)
	}
	if _, err := st.GetLatestBySlugForCaller(ctx, slug, "stranger@a.com"); !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("non-member must get ErrNotFound, got %v", err)
	}
}

// TestAggregates_GroupAccess covers the FOURTH access path: the sidebar
// scope/label histograms build their own access clause, which must honor group
// grants too — otherwise a member's histograms undercount artifacts they can
// actually see (flagged by review). Uses scopes (deterministic: well under the
// top-N cap) with a count delta: a public + a group-only artifact share one
// scope, so a member counts 2 and a non-member counts 1.
func TestAggregates_GroupAccess(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	gname := unique("agg")
	if _, err := st.CreateGroup(ctx, gname, "", []string{"member@a.com"}, "admin@a.com"); err != nil {
		t.Fatalf("create group: %v", err)
	}
	scope := unique("aggscope")
	put := func(access []string) {
		slug := unique("agg-slug")
		if _, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText, Title: "x", ContentType: "text/plain", Content: []byte("x"),
			Creator: "owner@a.com", NamedSlug: &slug, Scopes: []string{scope}, AllowedAccess: access,
		}); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	put([]string{"*"})                       // public
	put([]string{pgstore.GroupToken(gname)}) // group-only

	scopeCount := func(caller string) int64 {
		res, err := st.Aggregates(ctx, pgstore.AggregatesInput{Limit: 100, CallerEmail: caller})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range res.Scopes {
			if s.Scope == scope {
				return s.Count
			}
		}
		return 0
	}
	if c := scopeCount("stranger@a.com"); c != 1 {
		t.Fatalf("non-member must count only the public artifact: got %d, want 1", c)
	}
	if c := scopeCount("member@a.com"); c != 2 {
		t.Fatalf("group member must also count the group-granted artifact: got %d, want 2", c)
	}
}

// TestList_GroupAccessFilter covers the OTHER enforcement path — the SQL
// catalog filter must agree with CanAccess: an artifact granted only to a
// group shows up for a member and is hidden from a non-member.
func TestList_GroupAccessFilter(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	gname := unique("team")
	if _, err := st.CreateGroup(ctx, gname, "", []string{"member@a.com"}, "admin@a.com"); err != nil {
		t.Fatalf("create group: %v", err)
	}

	label := unique("grpdoc") // scope List to just this test's row
	row, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType:  pgstore.TypeText,
		Title:         "secret",
		ContentType:   "text/plain",
		Content:       []byte("x"),
		Creator:       "owner@a.com",
		Labels:        []string{label},
		AllowedAccess: []string{pgstore.GroupToken(gname)},
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	memberTokens, err := st.CallerGroups(ctx, "member@a.com")
	if err != nil {
		t.Fatal(err)
	}
	memberView, err := st.List(ctx, pgstore.ListInput{
		Limit: 10, Labels: []string{label},
		CallerEmail: "member@a.com", CallerGroups: memberTokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	if memberView.Total != 1 || len(memberView.Rows) != 1 || memberView.Rows[0].ArtifactID != row.ArtifactID {
		t.Fatalf("group member must see the group-granted artifact; total=%d", memberView.Total)
	}

	// Non-member: no group tokens → the array-overlap clause can't match.
	strangerView, err := st.List(ctx, pgstore.ListInput{
		Limit: 10, Labels: []string{label}, CallerEmail: "stranger@a.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strangerView.Total != 0 {
		t.Fatalf("non-member must NOT see the group-granted artifact; saw %d", strangerView.Total)
	}
}
