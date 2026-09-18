//go:build integration

package artifacts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// A blocked document is gone for everyone, including the admin who blocked it
// and the owner who wrote it. An admin with an exemption would still be able
// to hand the document out, which is the thing the block is for.
func TestBlock_NoCallerCanRead(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	const owner = "block-owner@example.com"
	const admin = "block-admin2@example.com"
	if err := st.AssignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.UnassignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin) })

	slug := uniqueSlug("blocked-doc")
	world := []string{"*"}
	created, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown", Content: "secret",
		AllowedAccess: &world,
	}, owner)
	if err != nil {
		t.Fatal(err)
	}

	if err := st.AddBlock(ctx, slug, "test", admin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.RemoveBlock(ctx, slug) })

	for _, caller := range []string{owner, admin, "stranger@example.com"} {
		if _, err := svc.GetBySlug(ctx, slug, nil, caller); !errors.Is(err, pgstore.ErrNotFound) {
			t.Errorf("GetBySlug as %s = %v, want ErrNotFound", caller, err)
		}
		if _, err := svc.Get(ctx, mustUUID(t, created.ArtifactID), caller); !errors.Is(err, pgstore.ErrNotFound) {
			t.Errorf("Get by id as %s = %v, want ErrNotFound", caller, err)
		}
		if vs, err := svc.Versions(ctx, slug, caller); err != nil || len(vs) != 0 {
			t.Errorf("Versions as %s = %d versions, %v; want none", caller, len(vs), err)
		}
	}

	// Lifting restores it unchanged — the point of blocking rather than
	// archiving or rewriting the ACL.
	if _, err := st.RemoveBlock(ctx, slug); err != nil {
		t.Fatal(err)
	}
	info, err := svc.GetBySlug(ctx, slug, nil, "stranger@example.com")
	if err != nil {
		t.Fatalf("after lift: %v", err)
	}
	if info.ArtifactID != created.ArtifactID {
		t.Errorf("after lift resolved to %s, want %s", info.ArtifactID, created.ArtifactID)
	}
}
