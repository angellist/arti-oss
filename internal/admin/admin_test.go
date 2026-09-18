//go:build integration

package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("ARTI_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://postgres:postgres@localhost:5436/arti_test?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// router attributes each request to the X-Test-Email header, standing in for
// the real auth middleware (which sets the identity in context).
func newRouter(svc *Service) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithIdentity(req.Context(), req.Header.Get("X-Test-Email"))))
		})
	})
	svc.Mount(r)
	return r
}

func get(t *testing.T, h http.Handler, email string) (int, Stats) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/stats", nil)
	if email != "" {
		req.Header.Set("X-Test-Email", email)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var st Stats
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
			t.Fatalf("decode stats: %v", err)
		}
	}
	return rec.Code, st
}

// seedComment inserts one active TEXT artifact, one open thread, and one live
// comment so the stats counts have a known non-zero floor. Rows are cleaned up
// after the test to keep the shared DB tidy.
func seedComment(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	aid, tid, cid := uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO artifacts (artifact_id, artifact_type, title, content_type, creator, inline_content)
		 VALUES ($1,'TEXT','admin-stats-test','text/plain','admin-stats@test.local','hi'::bytea)`, aid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO comment_threads (thread_id, artifact_id, anchor, status, created_by)
		 VALUES ($1,$2,'{"type":"doc"}','open','admin-stats@test.local')`, tid, aid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO comments (comment_id, thread_id, author, body)
		 VALUES ($1,$2,'admin-stats@test.local','hi')`, cid, tid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// CASCADE from artifacts removes the thread + comment.
		_, _ = pool.Exec(context.Background(), `DELETE FROM artifacts WHERE artifact_id=$1`, aid)
	})
}

func TestAdminStats(t *testing.T) {
	pool := newPool(t)
	store := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	// Stats now gates on MANAGE_ARTIFACTS — grant it to the test admin via the
	// ADMIN role (rather than the old email allowlist).
	const admin = "admin@example.com"
	if err := store.AssignRole(context.Background(), rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.UnassignRole(context.Background(), rbac.PrincipalUser, admin, rbac.RoleAdmin) })
	h := newRouter(NewService(pool, store))

	// Non-admins (and the unauthenticated) must not discover the endpoint.
	if code, _ := get(t, h, "user@example.com"); code != http.StatusNotFound {
		t.Fatalf("non-admin: want 404, got %d", code)
	}
	if code, _ := get(t, h, ""); code != http.StatusNotFound {
		t.Fatalf("unauthenticated: want 404, got %d", code)
	}

	seedComment(t, pool)

	code, st := get(t, h, "admin@example.com")
	if code != http.StatusOK {
		t.Fatalf("admin: want 200, got %d", code)
	}
	// After seeding, every count we touched must reflect at least our row.
	if st.Artifacts.Active < 1 || st.Artifacts.ByType["TEXT"] < 1 {
		t.Fatalf("artifact counts not reflected: %+v", st.Artifacts)
	}
	if st.Comments.Threads < 1 || st.Comments.ThreadsOpen < 1 {
		t.Fatalf("thread counts not reflected: %+v", st.Comments)
	}
	if st.Comments.Live < 1 || st.Comments.Last7Days < 1 {
		t.Fatalf("comment counts not reflected: %+v", st.Comments)
	}
}

// The block list is the lever an admin reaches for in a hurry, so the gate on
// it matters as much as what it does: nobody but an admin may see it, add to
// it, or lift from it.
func TestAdminBlocks(t *testing.T) {
	pool := newPool(t)
	store := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	const admin = "block-admin@example.com"
	ctx := context.Background()
	if err := store.AssignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.UnassignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin) })
	h := newRouter(NewService(pool, store))

	pattern := "admin-block-test-" + uuid.NewString()
	t.Cleanup(func() { _, _ = store.RemoveBlock(ctx, pattern) })

	do := func(method, path, email string, body io.Reader) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, body)
		if email != "" {
			req.Header.Set("X-Test-Email", email)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/blocks"},
		{http.MethodPost, "/api/admin/blocks"},
		{http.MethodDelete, "/api/admin/blocks?pattern=x"},
	} {
		if rec := do(c.method, c.path, "user@example.com", strings.NewReader(`{"pattern":"x"}`)); rec.Code != http.StatusNotFound {
			t.Fatalf("non-admin %s %s: want 404, got %d", c.method, c.path, rec.Code)
		}
	}

	if rec := do(http.MethodPost, "/api/admin/blocks", admin,
		strings.NewReader(`{"pattern":"`+pattern+`","reason":"test"}`)); rec.Code != http.StatusOK {
		t.Fatalf("add block: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	rec := do(http.MethodGet, "/api/admin/blocks", admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list blocks: want 200, got %d", rec.Code)
	}
	var listed struct {
		Blocks []pgstore.Block `json:"blocks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	var found *pgstore.Block
	for i := range listed.Blocks {
		if listed.Blocks[i].Pattern == pattern {
			found = &listed.Blocks[i]
		}
	}
	if found == nil {
		t.Fatalf("added pattern missing from the list")
	}
	if found.Reason != "test" || found.CreatedBy != admin {
		t.Errorf("block attribution = %q by %q, want %q by %q", found.Reason, found.CreatedBy, "test", admin)
	}

	if rec := do(http.MethodDelete, "/api/admin/blocks?pattern="+url.QueryEscape(pattern), admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("remove block: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodDelete, "/api/admin/blocks?pattern="+url.QueryEscape(pattern), admin, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("remove of a lifted block: want 404, got %d", rec.Code)
	}
}
