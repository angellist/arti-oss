//go:build integration

package artifacts_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// listArchived GETs /api/artifacts with the given extra query string as
// alice and decodes the response. The label scopes every query to the
// calling test's rows (the test DB is shared).
func listArchived(t *testing.T, r http.Handler, query string) artifacts.ListResponse {
	t.Helper()
	req := withAuth(
		httptest.NewRequest(http.MethodGet, "/api/artifacts?"+query, nil),
		"alice@example.com",
	)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var out artifacts.ListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// The /archived page lists archived=only. Archiving is per-version in
// the UI and versions of a slug archive independently, so every
// archived version must appear as its own row — the latest-per-slug
// catalog collapse must NOT apply (an archived row can never be its
// slug's latest live version, so the collapse would hide all of them).
func TestListArchivedOnly_IncludesSluggedArchivedVersions(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	label := uniqueSlug("arch-list")
	put := func(title string, slug *string) {
		t.Helper()
		if _, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText, Title: title, ContentType: "text/plain",
			Content: []byte(title), Creator: "alice@example.com",
			NamedSlug: slug, Labels: []string{label},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Slug fully archived (both versions).
	slugAll := uniqueSlug("arch-all")
	put("all-v1", &slugAll)
	put("all-v2", &slugAll)
	if _, err := st.ArchiveBySlug(ctx, slugAll); err != nil {
		t.Fatal(err)
	}

	// Slug with v1 archived, v2 live (the per-version UI flow).
	slugPart := uniqueSlug("arch-part")
	put("part-v1", &slugPart)
	v1, err := st.GetBySlug(ctx, slugPart, nil)
	if err != nil {
		t.Fatal(err)
	}
	put("part-v2", &slugPart)
	if _, err := st.ArchiveByID(ctx, pgstore.UUIDFromPG(v1.ArtifactID)); err != nil {
		t.Fatal(err)
	}

	// Slug-less archived artifact.
	put("loner", nil)
	loner, err := st.List(ctx, pgstore.ListInput{Limit: 10, Labels: []string{label}, Q: "loner"})
	if err != nil || len(loner.Rows) != 1 {
		t.Fatalf("seed loner lookup: rows=%d err=%v", len(loner.Rows), err)
	}
	if _, err := st.ArchiveByID(ctx, pgstore.UUIDFromPG(loner.Rows[0].ArtifactID)); err != nil {
		t.Fatal(err)
	}

	got := listArchived(t, r, "archived=only&label="+label)
	if got.Total != 4 {
		t.Fatalf("archived=only total = %d, want 4 (all-v1, all-v2, part-v1, loner)", got.Total)
	}
	want := map[string]bool{"all-v1": false, "all-v2": false, "part-v1": false, "loner": false}
	for _, a := range got.Artifacts {
		if _, ok := want[a.Title]; ok {
			want[a.Title] = true
		}
	}
	for title, seen := range want {
		if !seen {
			t.Errorf("archived row %q missing from archived=only listing", title)
		}
	}
}

// The /archived page shows what you archived most recently first, which
// is archive time (deleted_at), not creation time — an old artifact
// archived today must top the list.
func TestListArchivedOnly_DefaultOrderIsArchiveTimeDesc(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	label := uniqueSlug("arch-order")
	ids := make(map[string]uuid.UUID, 3)
	for _, title := range []string{"first", "second", "third"} {
		row, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText, Title: title, ContentType: "text/plain",
			Content: []byte(title), Creator: "alice@example.com",
			Labels: []string{label},
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[title] = pgstore.UUIDFromPG(row.ArtifactID)
	}

	// Archive out of creation order: second, third, first. Sleeps keep
	// deleted_at strictly ordered (now() has microsecond resolution).
	for _, title := range []string{"second", "third", "first"} {
		if _, err := st.ArchiveByID(ctx, ids[title]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	got := listArchived(t, r, "archived=only&label="+label)
	if len(got.Artifacts) != 3 {
		t.Fatalf("got %d rows, want 3", len(got.Artifacts))
	}
	wantOrder := []string{"first", "third", "second"} // most recently archived first
	for i, title := range wantOrder {
		if got.Artifacts[i].Title != title {
			t.Fatalf("row %d = %q, want %q (default order must be deleted_at DESC; created order was first,second,third)",
				i, got.Artifacts[i].Title, title)
		}
	}
}

// order_by=archived is an explicit sort key so the FE can flip the
// direction; asc must be oldest-archived first.
func TestListArchived_OrderByArchivedAsc(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	label := uniqueSlug("arch-key")
	ids := make(map[string]uuid.UUID, 2)
	for _, title := range []string{"b", "a"} {
		row, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText, Title: title, ContentType: "text/plain",
			Content: []byte(title), Creator: "alice@example.com",
			Labels: []string{label},
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[title] = pgstore.UUIDFromPG(row.ArtifactID)
	}
	// Archive in the opposite of creation order so archive-time order is
	// distinguishable from any created_at fallback.
	archiveOrder := []string{"a", "b"}
	for _, title := range archiveOrder {
		if _, err := st.ArchiveByID(ctx, ids[title]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	got := listArchived(t, r, "archived=only&label="+label+"&order_by=archived&order_dir=asc")
	if len(got.Artifacts) != 2 {
		t.Fatalf("got %d rows, want 2", len(got.Artifacts))
	}
	for i, title := range archiveOrder {
		if got.Artifacts[i].Title != title {
			t.Fatalf("asc row %d = %q, want %q", i, got.Artifacts[i].Title, title)
		}
	}
}
