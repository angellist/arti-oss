//go:build integration

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// The review routes are the only way to read a blocked document, so the gate
// on them and the shape of what they serve are the whole security story.
func TestAdminBlockedReview(t *testing.T) {
	pool := newPool(t)
	store := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	ctx := context.Background()
	const admin = "blocked-review-admin@example.com"
	if err := store.AssignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.UnassignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin) })
	h := newRouter(NewService(pool, store))

	// An HTML document, because serving one at its own content type is the
	// mistake this route must not make.
	slug := "blocked-review-" + uuid.NewString()
	if _, err := store.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, NamedSlug: &slug, Title: "Review me",
		ContentType: "text/html", Content: []byte("<script>alert(1)</script>"),
		Creator: "someone@example.com",
	}); err != nil {
		t.Fatal(err)
	}

	get := func(path, email string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if email != "" {
			req.Header.Set("X-Test-Email", email)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	docPath := "/api/admin/blocked/" + url.PathEscape(slug)
	matchPath := "/api/admin/blocks/matches?pattern=" + url.QueryEscape(slug)

	// Unblocked: the review routes refuse it, so they cannot stand in for an
	// ordinary read.
	if rec := get(docPath, admin); rec.Code != http.StatusNotFound {
		t.Fatalf("review of an unblocked document: want 404, got %d", rec.Code)
	}

	if err := store.AddBlock(ctx, slug, "test", admin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = store.RemoveBlock(ctx, slug) })

	// Non-admins never see any of it.
	for _, p := range []string{docPath, docPath + "/raw", matchPath} {
		if rec := get(p, "user@example.com"); rec.Code != http.StatusNotFound {
			t.Errorf("non-admin GET %s: want 404, got %d", p, rec.Code)
		}
	}

	if rec := get(docPath, admin); rec.Code != http.StatusOK {
		t.Fatalf("admin review: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := get(matchPath, admin); rec.Code != http.StatusOK {
		t.Fatalf("admin matches: want 200, got %d", rec.Code)
	}

	rec := get(docPath+"/raw", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin body: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	// The body comes back as text, never as the document's own type, so a
	// blocked page cannot run in the session of the admin reviewing it.
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("body Content-Type = %q, want text/plain", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("body served without nosniff")
	}
	if rec.Body.String() != "<script>alert(1)</script>" {
		t.Errorf("body = %q, want the document's bytes verbatim", rec.Body.String())
	}

	// Lifting closes the review path again.
	if _, err := store.RemoveBlock(ctx, slug); err != nil {
		t.Fatal(err)
	}
	if rec := get(docPath, admin); rec.Code != http.StatusNotFound {
		t.Errorf("review after the lift: want 404, got %d", rec.Code)
	}
}

// The review route takes an artifact id as well as a slug, because a slugless
// document has no other name to reach it by.
func TestAdminBlockedReview_SluglessByID(t *testing.T) {
	pool := newPool(t)
	store := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	ctx := context.Background()
	const admin = "blocked-slugless-admin@example.com"
	if err := store.AssignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.UnassignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin) })
	h := newRouter(NewService(pool, store))

	row, err := store.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "Slugless", ContentType: "text/plain",
		Content: []byte("no slug here"), Creator: "someone@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := pgstore.UUIDFromPG(row.ArtifactID).String()
	if err := store.AddBlock(ctx, id, "test", admin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = store.RemoveBlock(ctx, id) })

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Test-Email", admin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := get("/api/admin/blocked/" + id); rec.Code != http.StatusOK {
		t.Fatalf("review by id: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	rec := get("/api/admin/blocked/" + id + "/raw")
	if rec.Code != http.StatusOK {
		t.Fatalf("body by id: want 200, got %d", rec.Code)
	}
	if rec.Body.String() != "no slug here" {
		t.Errorf("body = %q", rec.Body.String())
	}
}
