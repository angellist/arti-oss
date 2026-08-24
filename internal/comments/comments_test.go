//go:build integration

package comments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/auth"
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

// router that attributes each request to the X-Test-Email header, standing
// in for the real auth middleware.
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

// The embed token lets a sandboxed served-HTML page comment via /api/embed/*
// without the session cookie — but only on the ONE artifact it's scoped to.
func TestComments_EmbedToken(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	art := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	svc := NewService(pool, art, auth.NewJWTSigner([]byte("test-secret")))
	r := chi.NewRouter()
	svc.MountEmbed(r)

	const owner = "owner@example.com"
	mk := func() string {
		row, err := art.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText, Title: "p", ContentType: "text/html",
			Content: []byte("<h1>x</h1>"), Creator: owner, AllowedAccess: []string{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return uuidStr(row.ArtifactID)
	}
	aID, bID := mk(), mk()
	tok, err := svc.SignEmbedToken(owner, aID, "", "")
	if err != nil {
		t.Fatal(err)
	}

	do := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	// valid token → can comment on its artifact
	if rec := do("POST", "/api/embed/artifacts/"+aID+"/comments", tok, `{"anchor":{"type":"doc"},"body":"hi from the page"}`); rec.Code != http.StatusCreated {
		t.Fatalf("embed create on scoped artifact: got %d (%s)", rec.Code, rec.Body)
	}
	if rec := do("GET", "/api/embed/artifacts/"+aID+"/comments", tok, ""); rec.Code != http.StatusOK {
		t.Fatalf("embed list: got %d", rec.Code)
	}
	// same token on a DIFFERENT artifact → refused (scope mismatch)
	if rec := do("POST", "/api/embed/artifacts/"+bID+"/comments", tok, `{"anchor":{"type":"doc"},"body":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("embed cross-artifact: want 404, got %d", rec.Code)
	}
	// no / bad token → 401
	if rec := do("GET", "/api/embed/artifacts/"+aID+"/comments", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: want 401, got %d", rec.Code)
	}
	if rec := do("GET", "/api/embed/artifacts/"+aID+"/comments", "garbage", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: want 401, got %d", rec.Code)
	}
	// CORS preflight
	if rec := do("OPTIONS", "/api/embed/artifacts/"+aID+"/comments", "", ""); rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight: code %d acao %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

// ListForCaller is the read-only surface MCP uses: it must enforce the same
// access as the artifact and honor the include-resolved toggle.
func TestComments_ListForCaller(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	art := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	svc := NewService(pool, art, auth.NewJWTSigner([]byte("test-secret")))
	router := newRouter(svc)

	const owner = "owner@example.com"
	const stranger = "stranger@example.com"

	row, err := art.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "doc", ContentType: "text/plain",
		Content: []byte("alpha beta gamma"), Creator: owner, AllowedAccess: []string{}, // creator-only
	})
	if err != nil {
		t.Fatal(err)
	}
	idStr := uuidStr(row.ArtifactID)
	id := uuid.MustParse(idStr)

	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("X-Test-Email", owner)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	mkThread := func(quote string) string {
		rec := do("POST", "/api/artifacts/"+idStr+"/comments", `{"anchor":{"type":"text","quote":"`+quote+`","start":0},"body":"c"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: %d (%s)", rec.Code, rec.Body)
		}
		var th Thread
		json.Unmarshal(rec.Body.Bytes(), &th)
		return th.ID
	}
	mkThread("alpha")
	resolved := mkThread("gamma")
	if rec := do("POST", "/api/comments/"+resolved+"/resolve", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("resolve: %d", rec.Code)
	}

	// default (include resolved) → both threads
	all, err := svc.ListForCaller(ctx, id, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Threads) != 2 {
		t.Fatalf("include-resolved: want 2 threads, got %d", len(all.Threads))
	}
	// exclude resolved → only the open one
	open, err := svc.ListForCaller(ctx, id, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(open.Threads) != 1 || open.Threads[0].Status != "open" {
		t.Fatalf("exclude-resolved: want 1 open thread, got %+v", open.Threads)
	}
	// a caller without read access can't read comments either
	if _, err := svc.ListForCaller(ctx, id, stranger, true); !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("stranger: want ErrNotFound, got %v", err)
	}
}

func TestComments_FlowAndAccess(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	art := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	svc := NewService(pool, art, auth.NewJWTSigner([]byte("test-secret")))
	router := newRouter(svc)

	const owner = "owner@example.com"
	const stranger = "stranger@example.com"

	// A private artifact (empty allowed_access → creator-only).
	row, err := art.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "doc", ContentType: "text/plain",
		Content: []byte("body"), Creator: owner, AllowedAccess: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := uuidStr(row.ArtifactID)

	do := func(method, path, email, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if email != "" {
			req.Header.Set("X-Test-Email", email)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// owner creates a doc-level thread
	rec := do("POST", "/api/artifacts/"+id+"/comments", owner, `{"anchor":{"type":"doc"},"body":"first"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d (%s)", rec.Code, rec.Body)
	}
	var created Thread
	json.Unmarshal(rec.Body.Bytes(), &created)
	if len(created.Comments) != 1 || created.Comments[0].Body != "first" {
		t.Fatalf("created thread bad: %+v", created)
	}
	tid := created.ID

	// owner lists → 1 thread
	rec = do("GET", "/api/artifacts/"+id+"/comments", owner, "")
	var list ListResponse
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Threads) != 1 {
		t.Fatalf("owner list: want 1 thread, got %d", len(list.Threads))
	}

	// stranger is blocked from both read and write (404, not 403)
	if rec := do("GET", "/api/artifacts/"+id+"/comments", stranger, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("stranger list: want 404, got %d", rec.Code)
	}
	if rec := do("POST", "/api/artifacts/"+id+"/comments", stranger, `{"anchor":{"type":"doc"},"body":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("stranger create: want 404, got %d", rec.Code)
	}

	// reply
	if rec := do("POST", "/api/comments/"+tid+"/replies", owner, `{"body":"second"}`); rec.Code != http.StatusCreated {
		t.Fatalf("reply: got %d (%s)", rec.Code, rec.Body)
	}

	// a second doc comment folds into the SAME single doc thread
	do("POST", "/api/artifacts/"+id+"/comments", owner, `{"anchor":{"type":"doc"},"body":"third"}`)
	rec = do("GET", "/api/artifacts/"+id+"/comments", owner, "")
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Threads) != 1 {
		t.Fatalf("doc thread must stay singular, got %d threads", len(list.Threads))
	}
	if len(list.Threads[0].Comments) != 3 {
		t.Fatalf("want 3 comments in doc thread, got %d", len(list.Threads[0].Comments))
	}

	// a text-anchored comment makes a distinct thread
	do("POST", "/api/artifacts/"+id+"/comments", owner, `{"anchor":{"type":"text","quote":"body"},"body":"on text"}`)
	rec = do("GET", "/api/artifacts/"+id+"/comments", owner, "")
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Threads) != 2 {
		t.Fatalf("want 2 threads after text comment, got %d", len(list.Threads))
	}

	// delete a comment — you can only delete your OWN. The doc thread (tid)
	// has several comments all authored by owner; deleting all of them
	// removes the now-empty thread.
	rec = do("GET", "/api/artifacts/"+id+"/comments", owner, "")
	json.Unmarshal(rec.Body.Bytes(), &list)
	var docCmtIDs []string
	for _, th := range list.Threads {
		if th.ID == tid {
			for _, c := range th.Comments {
				docCmtIDs = append(docCmtIDs, c.ID)
			}
		}
	}
	if len(docCmtIDs) == 0 {
		t.Fatal("expected comments on the doc thread")
	}
	// a stranger (no artifact access) is blocked → 404
	if rec := do("DELETE", "/api/comments/"+tid+"/comments/"+docCmtIDs[0], stranger, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("stranger delete comment: want 404, got %d", rec.Code)
	}
	// owner deletes each of their own comments → 204
	for _, cid := range docCmtIDs {
		if rec := do("DELETE", "/api/comments/"+tid+"/comments/"+cid, owner, ""); rec.Code != http.StatusNoContent {
			t.Fatalf("owner delete own comment: got %d", rec.Code)
		}
	}
	// thread auto-removed once empty
	rec = do("GET", "/api/artifacts/"+id+"/comments", owner, "")
	json.Unmarshal(rec.Body.Bytes(), &list)
	for _, th := range list.Threads {
		if th.ID == tid {
			t.Fatalf("emptied thread should be gone")
		}
	}

	// recreate a doc thread to exercise resolve below
	rec = do("POST", "/api/artifacts/"+id+"/comments", owner, `{"anchor":{"type":"doc"},"body":"again"}`)
	json.Unmarshal(rec.Body.Bytes(), &created)
	tid = created.ID

	// resolve the doc thread
	if rec := do("POST", "/api/comments/"+tid+"/resolve", owner, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("resolve: got %d", rec.Code)
	}
	rec = do("GET", "/api/artifacts/"+id+"/comments", owner, "")
	json.Unmarshal(rec.Body.Bytes(), &list)
	for _, th := range list.Threads {
		if th.ID == tid && th.Status != "resolved" {
			t.Fatalf("thread should be resolved, got %q", th.Status)
		}
	}
}

// A document whose owner turned commenting off must behave as if it has no
// comments at all: existing threads are not reported, and every mutating
// endpoint refuses — 403, not 404, because the artifact itself is readable and
// the client needs to be able to say WHY.
func TestComments_DisabledDocument(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	art := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	svc := NewService(pool, art, auth.NewJWTSigner([]byte("test-secret")))
	router := newRouter(svc)

	const owner = "owner@example.com"

	row, err := art.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "doc", ContentType: "text/plain",
		Content: []byte("body"), Creator: owner, AllowedAccess: []string{"*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := uuidStr(row.ArtifactID)

	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("X-Test-Email", owner)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// Seed a thread while commenting is still on.
	rec := do("POST", "/api/artifacts/"+id+"/comments", `{"anchor":{"type":"doc"},"body":"before"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed comment: got %d (%s)", rec.Code, rec.Body)
	}
	var seeded Thread
	json.Unmarshal(rec.Body.Bytes(), &seeded)

	if _, err := art.SetCommentsEnabled(ctx, uuidFrom(row.ArtifactID), false); err != nil {
		t.Fatal(err)
	}

	// Reads report nothing — the threads still exist in the table, they are
	// just not part of the document while the switch is off.
	rec = do("GET", "/api/artifacts/"+id+"/comments", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d", rec.Code)
	}
	var list ListResponse
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Threads) != 0 {
		t.Fatalf("comments off: want 0 threads, got %d", len(list.Threads))
	}
	mcp, err := svc.ListForCaller(ctx, uuidFrom(row.ArtifactID), owner, true)
	if err != nil {
		t.Fatalf("ListForCaller: %v", err)
	}
	if len(mcp.Threads) != 0 {
		t.Fatalf("comments off (MCP): want 0 threads, got %d", len(mcp.Threads))
	}

	// Every write is refused — including by the owner, who must flip the
	// switch back rather than write around it.
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/artifacts/" + id + "/comments", `{"anchor":{"type":"doc"},"body":"after"}`},
		{"POST", "/api/comments/" + seeded.ID + "/replies", `{"body":"after"}`},
		{"POST", "/api/comments/" + seeded.ID + "/resolve", ""},
		{"POST", "/api/comments/" + seeded.ID + "/reopen", ""},
		{"PUT", "/api/comments/" + seeded.ID + "/comments/" + seeded.Comments[0].ID, `{"body":"edited"}`},
		{"DELETE", "/api/comments/" + seeded.ID + "/comments/" + seeded.Comments[0].ID, ""},
	} {
		if rec := do(c.method, c.path, c.body); rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s with comments off: want 403, got %d (%s)", c.method, c.path, rec.Code, rec.Body)
		}
	}

	// Flipping it back restores the seeded thread untouched.
	if _, err := art.SetCommentsEnabled(ctx, uuidFrom(row.ArtifactID), true); err != nil {
		t.Fatal(err)
	}
	rec = do("GET", "/api/artifacts/"+id+"/comments", "")
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Threads) != 1 || len(list.Threads[0].Comments) != 1 {
		t.Fatalf("re-enabled: want the seeded thread back, got %+v", list.Threads)
	}
}
