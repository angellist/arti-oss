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

// Append's retry loop re-fetches the prior version on every iteration,
// including after losing the auto-create race — a caller-side access check
// run once before calling Append would miss that retry (see PR #113 review:
// a concurrent writer could create a restricted slug in the window between
// the service layer's pre-check and this loop's own lookup). CheckAccess
// closes that gap by re-running inside the loop itself, every time an
// existing row is found.
func TestAppend_CheckAccessRejectsWhenPriorVersionExists(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	slug := unique("append-checkaccess-deny")
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
	_, err := st.Append(ctx, pgstore.AppendInput{
		NamedSlug: slug,
		Content:   []byte("v2"),
		Creator:   "bob@example.com",
		CheckAccess: func(_ context.Context, _ sqlc.Artifact) error {
			return sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel from CheckAccess", err)
	}

	// The slug must still be at v1 — the rejected append must not have
	// written anything.
	row, gerr := st.GetBySlug(ctx, slug, nil)
	if gerr != nil {
		t.Fatalf("get back: %v", gerr)
	}
	if row.Version == nil || *row.Version != 1 {
		t.Errorf("version = %v, want 1 (rejected append must not create v2)", row.Version)
	}
}

// A passing CheckAccess lets the append through as normal.
func TestAppend_CheckAccessAllowsWhenGranted(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	slug := unique("append-checkaccess-allow")
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
	row, err := st.Append(ctx, pgstore.AppendInput{
		NamedSlug: slug,
		Content:   []byte("v2"),
		Creator:   "bob@example.com",
		CheckAccess: func(_ context.Context, _ sqlc.Artifact) error {
			called = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if !called {
		t.Error("CheckAccess was not invoked despite a prior version existing")
	}
	if row.Version == nil || *row.Version != 2 {
		t.Errorf("version = %v, want 2", row.Version)
	}
}

// Auto-creating a brand-new slug has no prior version to check access
// against, so CheckAccess must not be invoked on that path.
func TestAppend_CheckAccessNotCalledOnFreshSlug(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	slug := unique("append-checkaccess-fresh")
	title := "brand new"
	called := false
	_, err := st.Append(ctx, pgstore.AppendInput{
		NamedSlug:   slug,
		Content:     []byte("v1"),
		Creator:     "alice@example.com",
		Title:       &title,
		ContentType: "text/plain",
		CheckAccess: func(_ context.Context, _ sqlc.Artifact) error {
			called = true
			return errors.New("must not be called")
		},
	})
	if err != nil {
		t.Fatalf("auto-create append: %v", err)
	}
	if called {
		t.Error("CheckAccess was invoked on a fresh slug with no prior version")
	}
}
