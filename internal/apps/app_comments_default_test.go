//go:build integration

package apps_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func TestAppCommentsDefaultOffAndOptInIsInherited(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	man, _ := json.Marshal(map[string]any{"name": "demo", "entry": "index.html"})
	slug := uniqueSlug("app-comments")
	put := func() bool {
		t.Helper()
		row, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeApp,
			NamedSlug:    &slug,
			Title:        "demo app",
			ContentType:  "application/zip",
			Content:      buildAppZip(t, string(man)),
			Creator:      "alice@example.com",
		})
		if err != nil {
			t.Fatalf("put app: %v", err)
		}
		return row.CommentsEnabled
	}

	if put() {
		t.Fatal("a new APP must start with comments off")
	}
	if _, err := st.SetCommentsEnabledBySlug(ctx, slug, true); err != nil {
		t.Fatalf("turn comments on: %v", err)
	}
	if !put() {
		t.Fatal("a new APP version must inherit the owner's opt-in")
	}

	textSlug := uniqueSlug("text-to-app")
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, NamedSlug: &textSlug, Title: "doc",
		ContentType: "text/markdown", Content: []byte("hello"), Creator: "alice@example.com",
	}); err != nil {
		t.Fatalf("put text: %v", err)
	}
	row, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeApp, NamedSlug: &textSlug, Title: "doc as app",
		ContentType: "application/zip", Content: buildAppZip(t, string(man)), Creator: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("republish as app: %v", err)
	}
	if row.CommentsEnabled {
		t.Fatal("a slug republished as an APP must start with comments off")
	}
}
