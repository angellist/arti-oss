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

// A TEXT artifact must carry a textual content_type; a PDF (or any non-text
// blob) has to be an ATTACHMENT (or live in a PACKAGE). Otherwise the bytes
// render as garbage in the viewer.
func TestCreate_RejectsNonTextualTextType(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	// TEXT + application/pdf → rejected.
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		ArtifactType: "TEXT", Title: "kyc.pdf", ContentType: "application/pdf", Content: "%PDF-1.6",
	}, "tian@example.com"); err == nil {
		t.Fatal("TEXT + application/pdf should be rejected, got nil error")
	}

	// TEXT + text/markdown → allowed.
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		ArtifactType: "TEXT", Title: "notes", ContentType: "text/markdown", Content: "# hi",
	}, "tian@example.com"); err != nil {
		t.Fatalf("TEXT + text/markdown should be allowed: %v", err)
	}

	// ATTACHMENT + application/pdf → allowed (the correct home for a PDF).
	info, err := svc.Create(ctx, artifacts.CreateRequest{
		ArtifactType: "ATTACHMENT", Title: "kyc.pdf", ContentType: "application/pdf", Content: "%PDF-1.6",
	}, "tian@example.com")
	if err != nil {
		t.Fatalf("ATTACHMENT + application/pdf should be allowed: %v", err)
	}
	if info.ArtifactType != "ATTACHMENT" {
		t.Fatalf("artifact_type = %q, want ATTACHMENT", info.ArtifactType)
	}
}

// Versioning an existing slug requires the same access a reader would need —
// read and write are the same permission. Without it, a stranger could push
// a new version onto a slug they can't even see, which is worse than a
// stranger overwriting something public.
func TestCreate_VersioningRestrictedSlugRequiresAccess(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("create-restricted")
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType:  pgstore.TypeText,
		NamedSlug:     &slug,
		Title:         "secret",
		ContentType:   "text/plain",
		Content:       []byte("v1"),
		Creator:       "alice@example.com",
		AllowedAccess: []string{}, // creator-only
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Bob has no access to alice's creator-only slug — versioning it must
	// fail the same way reading it would (ErrNotFound, not a distinct
	// forbidden), so existence isn't leaked either.
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "hijack", ContentType: "text/plain", Content: "v2",
	}, "bob@example.com"); !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("bob versioning alice's creator-only slug: err = %v, want pgstore.ErrNotFound", err)
	}

	// Alice, the creator, can still version her own slug.
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v2", ContentType: "text/plain", Content: "v2",
	}, "alice@example.com"); err != nil {
		t.Fatalf("alice versioning her own slug: %v", err)
	}
}

// Once alice grants bob read access, bob can also version the slug: this
// service ties write permission to read permission rather than to a
// separate creator-only rule, so widening one widens the other.
func TestCreate_VersioningAllowedForAnyoneWithReadAccess(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("create-shared")
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType:  pgstore.TypeText,
		NamedSlug:     &slug,
		Title:         "shared",
		ContentType:   "text/plain",
		Content:       []byte("v1"),
		Creator:       "alice@example.com",
		AllowedAccess: []string{"bob@example.com"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	info, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v2 by bob", ContentType: "text/plain", Content: "v2",
	}, "bob@example.com")
	if err != nil {
		t.Fatalf("bob versioning a slug he has read access to: %v", err)
	}
	if info.Version == nil || *info.Version != 2 {
		t.Errorf("version = %v, want 2", info.Version)
	}
}
