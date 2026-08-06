//go:build integration

package artifacts_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// seedArtifact persists a minimal TEXT artifact owned by creator and returns
// its UUID + slug. Shared by the UpdateMetadata cases below.
func seedArtifact(t *testing.T, st *pgstore.Store, creator string, labels []string) (uuid.UUID, string) {
	t.Helper()
	slug := uniqueSlug("update-meta")
	row, err := st.Put(context.Background(), pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		NamedSlug:    &slug,
		Title:        "Original Title",
		ContentType:  "text/markdown",
		Content:      []byte("body"),
		Creator:      creator,
		Labels:       labels,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return pgstore.UUIDFromPG(row.ArtifactID), slug
}

// The creator can edit title + labels in place; labels are trimmed, deduped,
// and emptied-out, and the version is NOT bumped (metadata edits don't version).
func TestUpdateMetadataEditsFieldsInPlace(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	id, _ := seedArtifact(t, st, "alice@example.com", []string{"old"})

	newTitle := "Edited Title"
	info, err := svc.UpdateMetadata(ctx, id, artifacts.UpdateMetadataRequest{
		Title:  &newTitle,
		Labels: &[]string{"  kept ", "kept", "", "added"},
	}, "alice@example.com")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if info.Title != "Edited Title" {
		t.Errorf("title = %q, want %q", info.Title, "Edited Title")
	}
	if got := info.Labels; len(got) != 2 || got[0] != "kept" || got[1] != "added" {
		t.Errorf("labels = %v, want [kept added] (trimmed + deduped + empties dropped)", got)
	}
	// A metadata edit must NOT create a new version.
	if info.Version == nil || *info.Version != 1 {
		t.Errorf("version = %v, want 1 (metadata edit must not version)", info.Version)
	}
}

// Fields whose pointer is nil are left untouched; only the named field changes.
func TestUpdateMetadataLeavesOmittedFieldsUnchanged(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	id, _ := seedArtifact(t, st, "alice@example.com", []string{"keepme"})

	newTitle := "Only Title Changed"
	info, err := svc.UpdateMetadata(ctx, id, artifacts.UpdateMetadataRequest{Title: &newTitle}, "alice@example.com")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(info.Labels) != 1 || info.Labels[0] != "keepme" {
		t.Errorf("labels = %v, want [keepme] unchanged (nil pointer must not clear)", info.Labels)
	}
}

// Only the creator (or MANAGE_ARTIFACTS) may edit; a different non-admin caller
// is rejected so editing can't be used to seize someone else's artifact.
func TestUpdateMetadataRejectsNonCreator(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	id, _ := seedArtifact(t, st, "alice@example.com", nil)

	newTitle := "Hijacked"
	_, err := svc.UpdateMetadata(ctx, id, artifacts.UpdateMetadataRequest{Title: &newTitle}, "bob@example.com")
	if err == nil {
		t.Fatal("expected a forbidden error editing another user's artifact, got nil")
	}
	if !strings.Contains(err.Error(), "only the creator or an admin may edit") {
		t.Errorf("error = %v, want creator/admin forbidden", err)
	}
}

// An explicit empty title is a client mistake (400-class), not a clear.
func TestUpdateMetadataEmptyTitleRejected(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	id, _ := seedArtifact(t, st, "alice@example.com", nil)

	blank := "   "
	_, err := svc.UpdateMetadata(ctx, id, artifacts.UpdateMetadataRequest{Title: &blank}, "alice@example.com")
	if err == nil || !strings.Contains(err.Error(), "title cannot be empty") {
		t.Fatalf("error = %v, want 'title cannot be empty'", err)
	}
}

// Editing a missing artifact surfaces ErrNotFound (the HTTP layer maps 404, and
// non-creators can't probe existence — both paths funnel through this error).
func TestUpdateMetadataNotFound(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	newTitle := "x"
	_, err := svc.UpdateMetadata(ctx, uuid.New(), artifacts.UpdateMetadataRequest{Title: &newTitle}, "alice@example.com")
	if !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("error = %v, want pgstore.ErrNotFound", err)
	}
}

// Description is editable in place for the same reason labels are: the two are
// what browse/search surface, so a bad description is a findability defect. It
// used to be immutable, which meant the only way to fix one was minting a
// content version — impossible for a PACKAGE/ATTACHMENT whose original bytes
// are gone. An explicit "" clears it; a nil pointer leaves it alone.
func TestUpdateMetadataEditsDescription(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	id, _ := seedArtifact(t, st, "alice@example.com", []string{"keepme"})

	desc := "  what this doc is and when to read it  "
	info, err := svc.UpdateMetadata(ctx, id, artifacts.UpdateMetadataRequest{Description: &desc}, "alice@example.com")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if info.Description == nil || *info.Description != "what this doc is and when to read it" {
		t.Errorf("description = %v, want the trimmed string", info.Description)
	}
	// Editing only the description must not disturb labels or the version.
	if len(info.Labels) != 1 || info.Labels[0] != "keepme" {
		t.Errorf("labels = %v, want [keepme] untouched", info.Labels)
	}
	if info.Version == nil || *info.Version != 1 {
		t.Errorf("version = %v, want 1 (metadata edit must not version)", info.Version)
	}

	// Explicit empty clears it.
	empty := ""
	info, err = svc.UpdateMetadata(ctx, id, artifacts.UpdateMetadataRequest{Description: &empty}, "alice@example.com")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if info.Description != nil && *info.Description != "" {
		t.Errorf("description = %v, want cleared", *info.Description)
	}

	// A nil pointer leaves the (now empty) value alone rather than erroring.
	newTitle := "Title Only"
	if _, err := svc.UpdateMetadata(ctx, id, artifacts.UpdateMetadataRequest{Title: &newTitle}, "alice@example.com"); err != nil {
		t.Fatalf("title-only after clear: %v", err)
	}
}
