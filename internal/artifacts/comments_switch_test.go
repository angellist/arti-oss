//go:build integration

package artifacts_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// The per-doc comment switch: default ON, owner-only to change, applied to the
// WHOLE slug, and inherited by new versions. Each property is a way the
// feature could quietly fail open (comments reappearing on a doc whose owner
// closed it) or fail closed for the wrong person.
func TestCommentsSwitch_OwnerOnly_SlugWide_Inherited(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	const owner = "alice@example.com"
	const writer = "bob@example.com"
	slug := uniqueSlug("comment-switch")
	world := []string{"*"}

	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug:     &slug,
		Title:         "doc v1",
		ContentType:   "text/markdown",
		Content:       "v1",
		AllowedAccess: &world, // world-readable AND world-writable (mirror mode)
	}, owner)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !v1.CommentsEnabled {
		t.Fatal("comments must default to ON for a new artifact")
	}
	// CanManageComments is caller-aware, so it is stamped only on the
	// single-artifact viewer paths (Get / GetBySlug) — Create returns the bare
	// DTO with it nil. Read it back the way the viewer actually does.
	v1info, err := svc.Get(ctx, mustUUID(t, v1.ArtifactID), owner)
	if err != nil {
		t.Fatalf("owner get v1: %v", err)
	}
	if v1info.CanManageComments == nil || !*v1info.CanManageComments {
		t.Fatal("the owner must be told they can manage comments")
	}

	// A delegated writer can push a version — which makes THEM that version's
	// creator — but must still not own the switch.
	v2, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc v2", ContentType: "text/markdown", Content: "v2",
	}, writer)
	if err != nil {
		t.Fatalf("writer versioning a mirror-mode slug: %v", err)
	}
	v2info, err := svc.GetBySlug(ctx, slug, nil, writer)
	if err != nil {
		t.Fatalf("writer get: %v", err)
	}
	if v2info.CanManageComments == nil || *v2info.CanManageComments {
		t.Fatal("a delegated writer who created the latest version must NOT manage comments")
	}
	off := false
	if _, err := svc.UpdateMetadata(ctx, mustUUID(t, v2.ArtifactID),
		artifacts.UpdateMetadataRequest{CommentsEnabled: &off}, writer); err == nil {
		t.Fatal("a delegated writer must not be able to turn comments off")
	}

	// The owner can — even though `writer` created the version being patched.
	if _, err := svc.UpdateMetadata(ctx, mustUUID(t, v2.ArtifactID),
		artifacts.UpdateMetadataRequest{CommentsEnabled: &off}, owner); err != nil {
		t.Fatalf("owner turning comments off: %v", err)
	}

	// Slug-wide: the OLDER version must be off too, or a reader on /s/slug/1
	// still gets comment controls the owner thought they closed.
	got1, err := svc.Get(ctx, mustUUID(t, v1.ArtifactID), owner)
	if err != nil {
		t.Fatalf("get v1: %v", err)
	}
	if got1.CommentsEnabled {
		t.Fatal("turning comments off must apply to every version of the slug")
	}

	// Inherited: publishing v3 must not silently re-open commenting.
	v3, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc v3", ContentType: "text/markdown", Content: "v3",
	}, owner)
	if err != nil {
		t.Fatalf("owner v3: %v", err)
	}
	if v3.CommentsEnabled {
		t.Fatal("a new version must inherit the previous version's comment switch")
	}

	// A delegated writer's version inherits it too. This is the case that
	// makes the switch trustworthy: the writer never reads or sends the flag,
	// so it can only come from the store's own fresh read of the prior version
	// at insert time — not from anything the caller looked up earlier and that
	// a concurrent owner toggle could have made stale.
	v4, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc v4", ContentType: "text/markdown", Content: "v4",
	}, writer)
	if err != nil {
		t.Fatalf("writer v4: %v", err)
	}
	if v4.CommentsEnabled {
		t.Fatal("a delegated writer's version must inherit the owner's comment switch")
	}

	// And back on again — the switch is not one-way.
	on := true
	if _, err := svc.UpdateMetadata(ctx, mustUUID(t, v4.ArtifactID),
		artifacts.UpdateMetadataRequest{CommentsEnabled: &on}, owner); err != nil {
		t.Fatalf("owner turning comments back on: %v", err)
	}
	got1, err = svc.Get(ctx, mustUUID(t, v1.ArtifactID), owner)
	if err != nil {
		t.Fatalf("get v1 again: %v", err)
	}
	if !got1.CommentsEnabled {
		t.Fatal("turning comments back on must apply slug-wide too")
	}
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("bad uuid %q: %v", s, err)
	}
	return id
}

// Archiving a closed document and then republishing to its slug must NOT
// re-open commenting. Every slug read except version numbering filters
// `deleted_at IS NULL`, so a slug whose versions are ALL archived looks absent
// to the inheritance read while still being re-versionable — the one path that
// could resurrect comments the owner turned off, which is precisely what
// writing archived rows on the slug-wide update exists to prevent.
//
// Reported by Cursor Bugbot on #223.
func TestCommentsSwitch_SurvivesArchiveAndRepublish(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	const owner = "alice@example.com"
	slug := uniqueSlug("comment-switch-archived")

	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "closed doc", ContentType: "text/markdown", Content: "v1",
	}, owner)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	off := false
	if _, err := svc.UpdateMetadata(ctx, mustUUID(t, v1.ArtifactID),
		artifacts.UpdateMetadataRequest{CommentsEnabled: &off}, owner); err != nil {
		t.Fatalf("owner turning comments off: %v", err)
	}

	// Archive the ONLY version, so no live row remains under the slug.
	if _, err := svc.ArchiveBySlug(ctx, slug); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Republish to the same slug — allowed, since version numbering ignores
	// deleted_at and the unique index only covers live rows.
	v2, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "reborn doc", ContentType: "text/markdown", Content: "v2",
	}, owner)
	if err != nil {
		t.Fatalf("republish onto an all-archived slug: %v", err)
	}
	if v2.CommentsEnabled {
		t.Fatal("republishing onto an archived slug must not re-open comments the owner turned off")
	}
}
