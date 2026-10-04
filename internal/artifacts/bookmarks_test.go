//go:build integration

package artifacts_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func TestBookmarks_SpanVersionsCountPubliclyAndFilterPerPerson(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	access := []string{"*"}
	slug := uniqueSlug("bookmarks")
	create := func() uuid.UUID {
		info, err := svc.Create(ctx, artifacts.CreateRequest{
			NamedSlug: &slug, Title: "b", ContentType: "text/plain", Content: "body",
			AllowedAccess: &access,
		}, "owner@example.com")
		if err != nil {
			t.Fatal(err)
		}
		return uuid.MustParse(info.ArtifactID)
	}
	v1 := create()
	for _, who := range []string{"a@example.com", "A@example.com", "b@example.com"} {
		if _, err := svc.SetBookmark(ctx, v1, who, true); err != nil {
			t.Fatal(err)
		}
	}
	v2 := create()

	got, err := svc.Bookmarks(ctx, v2, "c@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != (artifacts.BookmarksDTO{Count: 2, Bookmarked: false}) {
		t.Fatalf("c on v2 = %+v, want the two v1 bookmarks counted and none of them c's", got)
	}

	bookmarkedBy := func(email string) int {
		res, err := svc.List(ctx, pgstore.ListInput{BookmarkedBy: &email, Slug: &slug, LatestPerSlug: true})
		if err != nil {
			t.Fatal(err)
		}
		return len(res.Artifacts)
	}
	if n := bookmarkedBy("b@example.com"); n != 1 {
		t.Fatalf("b's bookmarked list = %d rows, want 1", n)
	}
	if n := bookmarkedBy("c@example.com"); n != 0 {
		t.Fatalf("c's bookmarked list = %d rows, want 0: bookmarks are per person", n)
	}
	if n := bookmarkedBy(""); n != 0 {
		t.Fatalf("anonymous bookmarked list = %d rows, want 0", n)
	}

	catalogRow := func(viewer string) artifacts.ArtifactInfo {
		res, err := svc.List(auth.WithIdentity(ctx, viewer), pgstore.ListInput{Slug: &slug, LatestPerSlug: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Artifacts) != 1 || res.Artifacts[0].BookmarkCount == nil || res.Artifacts[0].Bookmarked == nil {
			t.Fatalf("catalog rows = %+v, want one annotated row", res.Artifacts)
		}
		return res.Artifacts[0]
	}
	if row := catalogRow("b@example.com"); *row.BookmarkCount != 2 || !*row.Bookmarked {
		t.Fatalf("b's catalog row count=%d bookmarked=%v, want 2 and true", *row.BookmarkCount, *row.Bookmarked)
	}
	if row := catalogRow("c@example.com"); *row.Bookmarked {
		t.Fatal("c sees another person's bookmark as their own")
	}

	got, err = svc.SetBookmark(ctx, v2, "a@example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || got.Bookmarked {
		t.Fatalf("after a removes = %+v, want count 1, not bookmarked", got)
	}
	if _, err := svc.SetBookmark(ctx, v2, "", true); err == nil {
		t.Fatal("anonymous bookmark succeeded, want error")
	}
}
