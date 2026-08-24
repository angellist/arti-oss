//go:build integration

package artifacts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func tokenSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]struct{}{}
	for _, x := range a {
		m[x] = struct{}{}
	}
	for _, x := range b {
		if _, ok := m[x]; !ok {
			return false
		}
	}
	return true
}

// The slug is the unit of access: the owner narrowing the ACL via PATCH on any
// version must revoke on EVERY version — v1 included — or the revocation is
// cosmetic (old versions carry mostly the same content).
func TestSlugACL_OwnerPatchConvergesAllVersions(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("slug-acl-patch")
	world := []string{"*"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world,
	}, "alice@example.com")
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	v2, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v2", ContentType: "text/markdown", Content: "v2",
	}, "alice@example.com")
	if err != nil {
		t.Fatalf("v2: %v", err)
	}

	// Bob can read v1 while it is world-readable.
	if _, err := svc.Get(ctx, mustUUID(t, v1.ArtifactID), "bob@example.com"); err != nil {
		t.Fatalf("bob pre-revoke read: %v", err)
	}

	creatorOnly := []string{}
	if _, err := svc.UpdateMetadata(ctx, mustUUID(t, v2.ArtifactID), artifacts.UpdateMetadataRequest{
		AllowedAccess: &creatorOnly,
	}, "alice@example.com"); err != nil {
		t.Fatalf("owner narrow: %v", err)
	}

	// Revocation propagated: bob is denied on the OLD version too.
	if _, err := svc.Get(ctx, mustUUID(t, v1.ArtifactID), "bob@example.com"); !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("bob post-revoke read of v1: err=%v, want ErrNotFound", err)
	}
}

// A delegated writer (on allowed_write, creator of a later version) re-sending
// that version's own ACL is a legal no-op today and must stay one: it must NOT
// converge a diverged sibling — that would be an ACL change on rows the writer
// has no authority over.
func TestSlugACL_DelegatedWriterNoopResendDoesNotConverge(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("slug-acl-noop")
	world := []string{"*"}
	writers := []string{"bob@example.com"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world, AllowedWrite: &writers,
	}, "alice@example.com")
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	v2, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v2", ContentType: "text/markdown", Content: "v2",
	}, "bob@example.com")
	if err != nil {
		t.Fatalf("bob v2: %v", err)
	}

	// Diverge v1 by hand (simulates pre-convergence data).
	divergent := []string{"*", "bob@example.com", "stray@example.com"}
	if _, err := st.UpdateAccess(ctx, mustUUID(t, v1.ArtifactID), divergent, writers); err != nil {
		t.Fatalf("diverge v1: %v", err)
	}

	// Bob resends v2's exact current pair — allowed, but converges nothing.
	v2row, err := st.GetByID(ctx, mustUUID(t, v2.ArtifactID))
	if err != nil {
		t.Fatalf("v2 row: %v", err)
	}
	resendAccess := append([]string{}, v2row.AllowedAccess...)
	resendWrite := append([]string{}, v2row.AllowedWrite...)
	if _, err := svc.UpdateMetadata(ctx, mustUUID(t, v2.ArtifactID), artifacts.UpdateMetadataRequest{
		AllowedAccess: &resendAccess, AllowedWrite: &resendWrite,
	}, "bob@example.com"); err != nil {
		t.Fatalf("bob no-op resend must stay legal: %v", err)
	}

	v1row, err := st.GetByID(ctx, mustUUID(t, v1.ArtifactID))
	if err != nil {
		t.Fatalf("v1 row: %v", err)
	}
	if !tokenSetEqual(v1row.AllowedAccess, divergent) {
		t.Fatalf("v1 allowed_access = %v; a delegated writer's no-op resend converged a sibling it has no authority over", v1row.AllowedAccess)
	}
}

// A delegated writer actually CHANGING the ACL via PATCH stays forbidden.
func TestSlugACL_DelegatedWriterAclChangeForbidden(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("slug-acl-403")
	world := []string{"*"}
	writers := []string{"bob@example.com"}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world, AllowedWrite: &writers,
	}, "alice@example.com"); err != nil {
		t.Fatalf("v1: %v", err)
	}
	v2, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v2", ContentType: "text/markdown", Content: "v2",
	}, "bob@example.com")
	if err != nil {
		t.Fatalf("bob v2: %v", err)
	}

	bobOnly := []string{"bob@example.com"}
	if _, err := svc.UpdateMetadata(ctx, mustUUID(t, v2.ArtifactID), artifacts.UpdateMetadataRequest{
		AllowedAccess: &bobOnly,
	}, "bob@example.com"); err == nil {
		t.Fatal("delegated writer changing the ACL via PATCH must be rejected")
	}
}

// The owner publishing a new version with a different ACL changes the SLUG's
// ACL: siblings converge (Key Decision D3 — fan-out, not 409).
func TestSlugACL_OwnerCreateWithChangedACLFansOut(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("slug-acl-create")
	world := []string{"*"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world,
	}, "alice@example.com")
	if err != nil {
		t.Fatalf("v1: %v", err)
	}

	narrow := []string{"crew@example.com"}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v2", ContentType: "text/markdown", Content: "v2",
		AllowedAccess: &narrow,
	}, "alice@example.com"); err != nil {
		t.Fatalf("v2: %v", err)
	}

	v1row, err := st.GetByID(ctx, mustUUID(t, v1.ArtifactID))
	if err != nil {
		t.Fatalf("v1 row: %v", err)
	}
	if !tokenSetEqual(v1row.AllowedAccess, narrow) {
		t.Fatalf("v1 allowed_access = %v, want converged to %v on owner publish", v1row.AllowedAccess, narrow)
	}
}

// The owner re-sending the current ACL heals drifted siblings — the exact
// operation the one-time convergence pass performs through the API.
func TestSlugACL_OwnerNoopResendHealsDrift(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("slug-acl-heal")
	world := []string{"*"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world,
	}, "alice@example.com")
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	v2, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v2", ContentType: "text/markdown", Content: "v2",
	}, "alice@example.com")
	if err != nil {
		t.Fatalf("v2: %v", err)
	}

	// Drift v1 by hand (pre-convergence data).
	if _, err := st.UpdateAccess(ctx, mustUUID(t, v1.ArtifactID), []string{"*", "stray@example.com"}, nil); err != nil {
		t.Fatalf("drift v1: %v", err)
	}

	// Owner resends v2's current pair (a no-op vs the targeted row).
	resend := append([]string{}, world...)
	if _, err := svc.UpdateMetadata(ctx, mustUUID(t, v2.ArtifactID), artifacts.UpdateMetadataRequest{
		AllowedAccess: &resend,
	}, "alice@example.com"); err != nil {
		t.Fatalf("owner resend: %v", err)
	}

	v1row, err := st.GetByID(ctx, mustUUID(t, v1.ArtifactID))
	if err != nil {
		t.Fatalf("v1 row: %v", err)
	}
	if !tokenSetEqual(v1row.AllowedAccess, world) {
		t.Fatalf("v1 allowed_access = %v, want healed to %v by the owner's no-op resend", v1row.AllowedAccess, world)
	}
}

// The slug OWNER must be able to edit access even when the targeted version
// was created by a delegated writer: doc-level settings (ACL, comments) are
// owner-gated, and the per-version creator guard must not intercept them.
// (Cursor round-1 finding on PR #230.)
func TestSlugACL_OwnerPatchOnDelegatedWritersVersion(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("slug-acl-owner-x")
	world := []string{"*"}
	writers := []string{"bob@example.com"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world, AllowedWrite: &writers,
	}, "alice@example.com")
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	v2, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v2", ContentType: "text/markdown", Content: "v2",
	}, "bob@example.com")
	if err != nil {
		t.Fatalf("bob v2: %v", err)
	}

	// Alice (owner, NOT v2's creator) revokes the world via v2 — the version
	// a viewer lands on, since it is the latest.
	creatorOnly := []string{}
	if _, err := svc.UpdateMetadata(ctx, mustUUID(t, v2.ArtifactID), artifacts.UpdateMetadataRequest{
		AllowedAccess: &creatorOnly, AllowedWrite: &creatorOnly,
	}, "alice@example.com"); err != nil {
		t.Fatalf("owner ACL patch on delegated writer's version: %v", err)
	}

	// Converged on every version.
	for _, id := range []string{v1.ArtifactID, v2.ArtifactID} {
		row, err := st.GetByID(ctx, mustUUID(t, id))
		if err != nil {
			t.Fatalf("row %s: %v", id, err)
		}
		if len(row.AllowedAccess) != 0 {
			t.Fatalf("version %s allowed_access = %v, want creator-only", id, row.AllowedAccess)
		}
	}
}

// The doc-settings exemption from the per-version creator guard must not open
// the ACL fields to callers who are NEITHER the owner NOR the targeted
// version's creator — even for a byte-identical resend (which the authority
// helper alone would wave through as a no-op).
func TestSlugACL_StrangerAclPatchForbidden(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("slug-acl-stranger")
	world := []string{"*"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world,
	}, "alice@example.com")
	if err != nil {
		t.Fatalf("v1: %v", err)
	}

	// Mallory can read (world) but is neither creator nor owner; resending the
	// exact current ACL must still be rejected.
	resend := append([]string{}, world...)
	if _, err := svc.UpdateMetadata(ctx, mustUUID(t, v1.ArtifactID), artifacts.UpdateMetadataRequest{
		AllowedAccess: &resend,
	}, "mallory@example.com"); err == nil {
		t.Fatal("stranger ACL patch (even a no-op resend) must be rejected")
	}

	// Slugless: same rule, creator-only.
	att, err := svc.Create(ctx, artifacts.CreateRequest{
		Title: "loose", ContentType: "text/markdown", Content: "x",
		AllowedAccess: &world,
	}, "alice@example.com")
	if err != nil {
		t.Fatalf("slugless: %v", err)
	}
	if _, err := svc.UpdateMetadata(ctx, mustUUID(t, att.ArtifactID), artifacts.UpdateMetadataRequest{
		AllowedAccess: &resend,
	}, "mallory@example.com"); err == nil {
		t.Fatal("stranger ACL patch on a slugless artifact must be rejected")
	}
}

// The post-write Get fallback must be reachable ONLY when an authorized
// doc-settings write actually ran. A stranger PATCHing comments_enabled to
// its CURRENT value takes the no-op branch — no authority check fires — and
// must get the same NotFound an unauthorized read gets, not the metadata.
// (Cursor round-2 finding on PR #230.)
func TestSlugACL_StrangerNoopCommentsPatchLeaksNothing(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("slug-acl-leak")
	creatorOnly := []string{}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "secret-title", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &creatorOnly,
	}, "alice@example.com"); err != nil {
		t.Fatalf("v1: %v", err)
	}
	row, err := st.GetBySlug(ctx, slug, nil)
	if err != nil {
		t.Fatalf("row: %v", err)
	}

	// comments_enabled defaults to true; resending true is a no-op that
	// bypasses isDocOwner. Mallory must still learn nothing.
	on := true
	info, err := svc.UpdateMetadata(ctx, mustUUID(t, pgstore.UUIDFromPG(row.ArtifactID).String()), artifacts.UpdateMetadataRequest{
		CommentsEnabled: &on,
	}, "mallory@example.com")
	if !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("stranger no-op comments patch: err=%v info.Title=%q — must be ErrNotFound with no metadata", err, info.Title)
	}
}
