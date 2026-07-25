//go:build integration

package artifacts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// With allowed_write=[] (creator-only writes) but allowed_access=['*'] (world
// readable), a non-creator can READ but must NOT be able to push a new version
// — the split that this feature introduces. Before allowed_write, read==write
// meant any reader could version it.
func TestWriteGate_ReaderCannotVersionCreatorOnlyWrite(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("write-gate")
	world := []string{"*"}
	creatorOnly := []string{}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug:     &slug,
		Title:         "shared-read",
		ContentType:   "text/markdown",
		Content:       "v1",
		AllowedAccess: &world,       // anyone may read
		AllowedWrite:  &creatorOnly, // only the creator may write
	}, "alice@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Bob can read (world-readable) but must be denied versioning.
	_, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug:   &slug,
		Title:       "bob-hijack",
		ContentType: "text/markdown",
		Content:     "v2",
	}, "bob@example.com")
	if !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("bob versioning a creator-only-write slug: err = %v, want ErrNotFound", err)
	}

	// The creator can still version it.
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug:   &slug,
		Title:       "alice-v2",
		ContentType: "text/markdown",
		Content:     "v2",
	}, "alice@example.com"); err != nil {
		t.Fatalf("creator re-version: %v", err)
	}

	// GetBySlug stamps can_write from the same gate: true for the creator,
	// false for a read-only grantee. This is what the viewer's Edit affordance
	// keys on.
	aliceInfo, err := svc.GetBySlug(ctx, slug, nil, "alice@example.com")
	if err != nil {
		t.Fatalf("get as alice: %v", err)
	}
	if aliceInfo.CanWrite == nil || !*aliceInfo.CanWrite {
		t.Errorf("creator can_write = %v, want true", aliceInfo.CanWrite)
	}
	bobInfo, err := svc.GetBySlug(ctx, slug, nil, "bob@example.com")
	if err != nil {
		t.Fatalf("get as bob: %v", err)
	}
	if bobInfo.CanWrite == nil || *bobInfo.CanWrite {
		t.Errorf("read-only grantee can_write = %v, want false", bobInfo.CanWrite)
	}
}

// A delegated writer (on allowed_write but not the creator) may push content
// versions but must NOT change the ACL on a new version — that would let them
// widen readership or grant write to arbitrary principals. ACL changes on a
// version are creator/admin-only, matching the PATCH gate. Omitting the ACL
// fields (inherit) stays allowed.
func TestWriteGate_DelegatedWriterCannotChangeAclOnVersion(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("acl-esc")
	world := []string{"*"}
	writers := []string{"bob@example.com"} // bob is a delegated writer
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world, AllowedWrite: &writers,
	}, "alice@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Bob tries to version AND widen the ACL (grant write to mallory) — rejected.
	escalate := []string{"bob@example.com", "mallory@example.com"}
	_, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v2",
		AllowedWrite: &escalate,
	}, "bob@example.com")
	if err == nil {
		t.Fatal("delegated writer changing allowed_write on a version must be rejected")
	}

	// Bob CAN push a content-only version (no ACL fields) — inherits alice's ACL.
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v2",
	}, "bob@example.com"); err != nil {
		t.Fatalf("delegated writer content-only version should be allowed: %v", err)
	}

	// Creator-flip escalation: bob's content-only version above made HIM the
	// latest version's creator. He must STILL be unable to change the ACL —
	// the gate must not trust the (mutable) latest-version creator.
	_, err = svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v3",
		AllowedWrite: &escalate,
	}, "bob@example.com")
	if err == nil {
		t.Fatal("creator-flip: delegated writer must not escalate ACL after becoming latest-version creator")
	}

	// A no-op ACL (re-sending the current values) is allowed for a delegated
	// writer — covers the CLI/MCP re-sending a configured default_access.
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v3",
		AllowedAccess: &world, AllowedWrite: &writers, // == current ACL
	}, "bob@example.com"); err != nil {
		t.Fatalf("no-op ACL re-send by a delegated writer should be allowed: %v", err)
	}
}

// The creator-flip also must not work through PATCH: a delegated writer who
// became the latest version's creator (by pushing a content version) must not
// be able to change the ACL via UpdateMetadata. Authority is the immutable slug
// owner, so only the owner (alice) or an admin may. The slug's latest version
// at this point was pushed by bob (from the test above's content-only version).
func TestWriteGate_PatchAclAuthorityIsSlugOwner(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("patch-owner")
	world := []string{"*"}
	writers := []string{"bob@example.com"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world, AllowedWrite: &writers,
	}, "alice@example.com")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// bob (delegated writer) pushes a content-only version → becomes latest
	// version's creator.
	bobVer, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v2",
	}, "bob@example.com")
	if err != nil {
		t.Fatalf("bob content version: %v", err)
	}

	// bob PATCHes his version's ACL to escalate → rejected (not the owner).
	escalate := []string{"*"}
	bobVerID, perr := uuid.Parse(bobVer.ArtifactID)
	if perr != nil {
		t.Fatal(perr)
	}
	if _, err := svc.UpdateMetadata(ctx, bobVerID, artifacts.UpdateMetadataRequest{
		AllowedWrite: &escalate,
	}, "bob@example.com"); err == nil {
		t.Fatal("PATCH creator-flip: non-owner writer must not change ACL")
	}

	// alice (the owner) can change the ACL on her own version.
	aliceID, perr := uuid.Parse(v1.ArtifactID)
	if perr != nil {
		t.Fatal(perr)
	}
	readOnly := []string{}
	if _, err := svc.UpdateMetadata(ctx, aliceID, artifacts.UpdateMetadataRequest{
		AllowedWrite: &readOnly,
	}, "alice@example.com"); err != nil {
		t.Fatalf("owner changing ACL via PATCH should be allowed: %v", err)
	}
}
