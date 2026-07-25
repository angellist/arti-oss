//go:build integration

package pgstore_test

import (
	"context"
	"errors"
	"testing"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// Put always re-derives the next version number live (nextVersion queries
// current state, not anything cached from an earlier read), so a caller-side
// access check taken before calling Put can be stale: the slug can come into
// existence as someone else's restricted artifact between that check and
// this call, and Put would otherwise insert the next version under it with
// no unique-violation to force a retry (unlike Append). CheckAccess re-reads
// fresh right here and rejects if the caller can't see what's actually
// there now (Cursor Bugbot finding on PR #113 round 2, mirroring the
// Append fix for the same class of gap).
func TestPut_CheckAccessRejectsWhenPriorVersionExists(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	slug := unique("put-checkaccess-deny")
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

	sentinel := errors.New("no access")
	_, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		NamedSlug:    &slug,
		Title:        "hijack",
		ContentType:  "text/plain",
		Content:      []byte("v2"),
		Creator:      "bob@example.com",
		CheckAccess: func(_ context.Context, _ sqlc.Artifact) error {
			return sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel from CheckAccess", err)
	}

	row, gerr := st.GetBySlug(ctx, slug, nil)
	if gerr != nil {
		t.Fatalf("get back: %v", gerr)
	}
	if row.Version == nil || *row.Version != 1 {
		t.Errorf("version = %v, want 1 (rejected put must not create v2)", row.Version)
	}
}

// A passing CheckAccess lets the version through as normal.
func TestPut_CheckAccessAllowsWhenGranted(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	slug := unique("put-checkaccess-allow")
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		NamedSlug:    &slug,
		Title:        "shared",
		ContentType:  "text/plain",
		Content:      []byte("v1"),
		Creator:      "alice@example.com",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	called := false
	row, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		NamedSlug:    &slug,
		Title:        "v2",
		ContentType:  "text/plain",
		Content:      []byte("v2"),
		Creator:      "bob@example.com",
		CheckAccess: func(_ context.Context, _ sqlc.Artifact) error {
			called = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !called {
		t.Error("CheckAccess was not invoked despite a prior version existing")
	}
	if row.Version == nil || *row.Version != 2 {
		t.Errorf("version = %v, want 2", row.Version)
	}
}

// A brand-new slug (or no slug at all) has no prior version to check access
// against, so CheckAccess must not be invoked.
func TestPut_CheckAccessNotCalledOnFreshSlug(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	slug := unique("put-checkaccess-fresh")
	called := false
	_, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		NamedSlug:    &slug,
		Title:        "brand new",
		ContentType:  "text/plain",
		Content:      []byte("v1"),
		Creator:      "alice@example.com",
		CheckAccess: func(_ context.Context, _ sqlc.Artifact) error {
			called = true
			return errors.New("must not be called")
		},
	})
	if err != nil {
		t.Fatalf("fresh put: %v", err)
	}
	if called {
		t.Error("CheckAccess was invoked on a fresh slug with no prior version")
	}
}
