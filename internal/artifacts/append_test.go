//go:build integration

package artifacts_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// withAuth returns a Request whose context carries `email` as the
// authenticated user — what auth.RequireAuth would normally inject.
// The append flow reads creator from EmailFromContext so we have to
// fake it here in the test.
func withAuth(req *http.Request, email string) *http.Request {
	ctx := auth.WithIdentity(req.Context(), email)
	return req.WithContext(ctx)
}

func uniqueSlug(prefix string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

func TestAppendCreatesV1WhenSlugAbsent(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	slug := uniqueSlug("append-create")
	body := artifacts.AppendRequest{
		Content:     "hello world",
		Title:       strPtr("Append Test " + slug),
		ContentType: "text/markdown",
	}
	raw, _ := json.Marshal(body)
	req := withAuth(
		httptest.NewRequest(http.MethodPost, "/api/artifacts/by-slug/"+slug+"/append", bytes.NewReader(raw)),
		"alice@example.com",
	)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var info artifacts.ArtifactInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.NamedSlug == nil || *info.NamedSlug != slug {
		t.Fatalf("slug roundtrip: got %v", info.NamedSlug)
	}
	if info.Version == nil || *info.Version != 1 {
		t.Fatalf("expected v1 on auto-create, got %v", info.Version)
	}

	// Verify content via direct store read.
	row, err := st.GetBySlug(ctx, slug, nil)
	if err != nil {
		t.Fatalf("get back: %v", err)
	}
	if got := string(row.InlineContent); got != "hello world" {
		t.Errorf("body = %q, want %q", got, "hello world")
	}
}

func TestAppendBumpsVersionAndConcatsWithSeparator(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	slug := uniqueSlug("append-bump")
	// Seed v1.
	_, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		NamedSlug:    &slug,
		Title:        "seed",
		ContentType:  "text/markdown",
		Content:      []byte("first"),
		Creator:      "alice@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Append (default separator "\n\n").
	body := artifacts.AppendRequest{Content: "second"}
	raw, _ := json.Marshal(body)
	req := withAuth(
		httptest.NewRequest(http.MethodPost, "/api/artifacts/by-slug/"+slug+"/append", bytes.NewReader(raw)),
		"alice@example.com",
	)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("append status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var info artifacts.ArtifactInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if info.Version == nil || *info.Version != 2 {
		t.Fatalf("expected v2, got %v", info.Version)
	}

	row, _ := st.GetBySlug(ctx, slug, nil)
	got := string(row.InlineContent)
	want := "first\n\nsecond"
	if got != want {
		t.Errorf("concat body = %q, want %q", got, want)
	}

	// Custom separator.
	sep := " | "
	body2 := artifacts.AppendRequest{Content: "third", Separator: &sep}
	raw2, _ := json.Marshal(body2)
	req2 := withAuth(
		httptest.NewRequest(http.MethodPost, "/api/artifacts/by-slug/"+slug+"/append", bytes.NewReader(raw2)),
		"alice@example.com",
	)
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("status2 = %d, body = %s", rr2.Code, rr2.Body.String())
	}
	row2, _ := st.GetBySlug(ctx, slug, nil)
	got2 := string(row2.InlineContent)
	want2 := "first\n\nsecond | third"
	if got2 != want2 {
		t.Errorf("custom-sep body = %q, want %q", got2, want2)
	}
}

// Appending to an existing slug requires the same access a reader would
// need — read and write are the same permission. Bob has no access to
// alice's creator-only slug, so his append is rejected with a 404 (not a
// 403), matching the read-side "don't leak existence" behavior.
func TestAppendRestrictedSlugRequiresAccess(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	slug := uniqueSlug("append-restricted")
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType:  pgstore.TypeText,
		NamedSlug:     &slug,
		Title:         "secret",
		ContentType:   "text/markdown",
		Content:       []byte("first"),
		Creator:       "alice@example.com",
		AllowedAccess: []string{}, // creator-only
	}); err != nil {
		t.Fatal(err)
	}

	body := artifacts.AppendRequest{Content: "hijack"}
	raw, _ := json.Marshal(body)
	req := withAuth(
		httptest.NewRequest(http.MethodPost, "/api/artifacts/by-slug/"+slug+"/append", bytes.NewReader(raw)),
		"bob@example.com",
	)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("bob appending to alice's creator-only slug: status = %d, body = %s, want 404", rr.Code, rr.Body.String())
	}

	row, _ := st.GetBySlug(ctx, slug, nil)
	if got := string(row.InlineContent); got != "first" {
		t.Errorf("content changed despite rejected append: got %q", got)
	}
}

// Once alice grants bob read access, he can also append a new version:
// write follows read, so widening allowed_access widens both.
func TestAppendAllowedForAnyoneWithReadAccess(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	slug := uniqueSlug("append-shared")
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType:  pgstore.TypeText,
		NamedSlug:     &slug,
		Title:         "shared",
		ContentType:   "text/markdown",
		Content:       []byte("first"),
		Creator:       "alice@example.com",
		AllowedAccess: []string{"bob@example.com"},
	}); err != nil {
		t.Fatal(err)
	}

	body := artifacts.AppendRequest{Content: "second"}
	raw, _ := json.Marshal(body)
	req := withAuth(
		httptest.NewRequest(http.MethodPost, "/api/artifacts/by-slug/"+slug+"/append", bytes.NewReader(raw)),
		"bob@example.com",
	)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("bob appending to a slug he has read access to: status = %d, body = %s", rr.Code, rr.Body.String())
	}

	row, _ := st.GetBySlug(ctx, slug, nil)
	if got, want := string(row.InlineContent), "first\n\nsecond"; got != want {
		t.Errorf("concat body = %q, want %q", got, want)
	}
}

func TestAppendIdempotencyReplay(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	slug := uniqueSlug("append-idem")
	body := artifacts.AppendRequest{
		Content:        "ONCE",
		Title:          strPtr("idem"),
		ContentType:    "text/plain",
		IdempotencyKey: "test-key-" + slug,
	}
	raw, _ := json.Marshal(body)

	// First write.
	req1 := withAuth(
		httptest.NewRequest(http.MethodPost, "/api/artifacts/by-slug/"+slug+"/append", bytes.NewReader(raw)),
		"alice@example.com",
	)
	rr1 := httptest.NewRecorder()
	r.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first write status = %d, body = %s", rr1.Code, rr1.Body.String())
	}
	var info1 artifacts.ArtifactInfo
	_ = json.Unmarshal(rr1.Body.Bytes(), &info1)
	if info1.Version == nil || *info1.Version != 1 {
		t.Fatalf("first write should be v1, got %v", info1.Version)
	}
	if rr1.Header().Get("X-Arti-Idempotent-Replay") == "true" {
		t.Errorf("first write incorrectly flagged as replay")
	}

	// Second write with same key — should return cached v1, no v2.
	req2 := withAuth(
		httptest.NewRequest(http.MethodPost, "/api/artifacts/by-slug/"+slug+"/append", bytes.NewReader(raw)),
		"alice@example.com",
	)
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second write status = %d, body = %s", rr2.Code, rr2.Body.String())
	}
	if rr2.Header().Get("X-Arti-Idempotent-Replay") != "true" {
		t.Errorf("expected X-Arti-Idempotent-Replay=true on retry, got %q",
			rr2.Header().Get("X-Arti-Idempotent-Replay"))
	}
	var info2 artifacts.ArtifactInfo
	_ = json.Unmarshal(rr2.Body.Bytes(), &info2)
	if info2.ArtifactID != info1.ArtifactID {
		t.Errorf("replay should return same artifact_id; got %s vs %s",
			info2.ArtifactID, info1.ArtifactID)
	}

	// Body should still be just "ONCE" (no second copy).
	row, _ := st.GetBySlug(ctx, slug, nil)
	if got := string(row.InlineContent); got != "ONCE" {
		t.Errorf("idempotent replay should not double-write; body = %q", got)
	}
	if row.Version == nil || *row.Version != 1 {
		t.Errorf("expected to stop at v1 after replay; got %v", row.Version)
	}
}

func TestAppendIdempotencyScopedByCreator(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	slug := uniqueSlug("append-idem-scope")
	// Idempotency table is global (not per-slug), so re-using a literal
	// key across test runs collides. Randomize per run.
	key := uniqueSlug("shared-key")
	body := artifacts.AppendRequest{
		Content:        "from alice",
		Title:          strPtr("scoped-idem"),
		ContentType:    "text/plain",
		IdempotencyKey: key,
	}
	rawA, _ := json.Marshal(body)
	// Alice writes.
	rA := httptest.NewRecorder()
	r.ServeHTTP(rA, withAuth(
		httptest.NewRequest(http.MethodPost, "/api/artifacts/by-slug/"+slug+"/append", bytes.NewReader(rawA)),
		"alice@example.com",
	))
	if rA.Code != http.StatusOK {
		t.Fatalf("alice write: %d %s", rA.Code, rA.Body.String())
	}

	// Bob writes with same key — should NOT collide with alice's cache.
	body.Content = "from bob"
	rawB, _ := json.Marshal(body)
	rB := httptest.NewRecorder()
	r.ServeHTTP(rB, withAuth(
		httptest.NewRequest(http.MethodPost, "/api/artifacts/by-slug/"+slug+"/append", bytes.NewReader(rawB)),
		"bob@example.com",
	))
	if rB.Code != http.StatusOK {
		t.Fatalf("bob write: %d %s", rB.Code, rB.Body.String())
	}
	if rB.Header().Get("X-Arti-Idempotent-Replay") == "true" {
		t.Errorf("bob's write should not be a replay of alice's (keys are creator-scoped)")
	}
}

func TestAppendRejectsBinaryContentType(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	slug := uniqueSlug("append-binary")
	body := artifacts.AppendRequest{
		Content:     "junk",
		Title:       strPtr("binary"),
		ContentType: "image/png",
	}
	raw, _ := json.Marshal(body)
	req := withAuth(
		httptest.NewRequest(http.MethodPost, "/api/artifacts/by-slug/"+slug+"/append", bytes.NewReader(raw)),
		"alice@example.com",
	)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for image content_type, got %d (body %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "binary") {
		t.Errorf("error body should mention 'binary'; got %s", rr.Body.String())
	}
}

// TestAppendClientMistakesReturn400 exercises the cursor-bot
// "Append validation returns HTTP 500" fix. Client-shaped mistakes
// (missing title on auto-create, append to PACKAGE/ATTACHMENT slug)
// must surface as 400 with a usable message, NOT a generic 500 that
// looks like an arti bug.
func TestAppendClientMistakesReturn400(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	// Case A: auto-create path missing the seed title.
	slugNew := uniqueSlug("append-no-title")
	req := withAuth(httptest.NewRequest(http.MethodPost,
		"/api/artifacts/by-slug/"+slugNew+"/append",
		strings.NewReader(`{"content":"x","content_type":"text/plain"}`)),
		"alice@example.com")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("auto-create without title: status = %d, want 400 (body=%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "title") {
		t.Errorf("auto-create error should mention 'title'; got %s", rr.Body.String())
	}

	// Case B: append to a PACKAGE artifact (stub a real one in via the store).
	slugPkg := uniqueSlug("append-into-pkg")
	tinyZip := []byte("PK\x05\x06" + strings.Repeat("\x00", 18)) // minimal empty zip
	_, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypePackage,
		NamedSlug:    &slugPkg,
		Title:        "pkg",
		ContentType:  "application/zip",
		Content:      tinyZip,
		Creator:      "alice@example.com",
	})
	if err != nil {
		// Empty zip may fail manifest validation — fall back to skipping if so.
		t.Skipf("could not seed PACKAGE artifact for test: %v", err)
	}
	req2 := withAuth(httptest.NewRequest(http.MethodPost,
		"/api/artifacts/by-slug/"+slugPkg+"/append",
		strings.NewReader(`{"content":"x"}`)),
		"alice@example.com")
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("append-to-PACKAGE: status = %d, want 400 (body=%s)", rr2.Code, rr2.Body.String())
	}
	if !strings.Contains(rr2.Body.String(), "PACKAGE") {
		t.Errorf("error should mention PACKAGE; got %s", rr2.Body.String())
	}
}

// TestAppendStaleIdempotencyKeyReturnsGone exercises the cursor-bot
// "Idempotency replay re-appends loop" fix: when the cached artifact
// is no longer accessible, the next retry MUST NOT silently write a
// new version (which would loop until the 24h TTL clears). Instead,
// the service returns idempotencyStale → 410 Gone, so the caller knows
// to pick a fresh key.
func TestAppendStaleIdempotencyKeyReturnsGone(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	slug := uniqueSlug("append-stale")
	key := uniqueSlug("stale-key")
	body := artifacts.AppendRequest{
		Content:        "first",
		Title:          strPtr("stale"),
		ContentType:    "text/plain",
		IdempotencyKey: key,
	}
	raw, _ := json.Marshal(body)

	// First write — records (key, alice) → artifact v1.
	req1 := withAuth(httptest.NewRequest(http.MethodPost, "/api/artifacts/by-slug/"+slug+"/append", bytes.NewReader(raw)), "alice@example.com")
	rr1 := httptest.NewRecorder()
	r.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first write status = %d, body = %s", rr1.Code, rr1.Body.String())
	}
	var info1 artifacts.ArtifactInfo
	_ = json.Unmarshal(rr1.Body.Bytes(), &info1)

	// Hard-delete the artifact so that even the creator can't read it
	// back. (Archive doesn't trigger the staleness path here because
	// the creator can still see their own archived artifacts.)
	uid, _ := uuid.Parse(info1.ArtifactID)
	if _, err := svc.HardDeleteByID(ctx, uid); err != nil {
		t.Fatalf("hard delete: %v", err)
	}

	// Retry with the same key — should NOT fall through to a new write.
	req2 := withAuth(httptest.NewRequest(http.MethodPost, "/api/artifacts/by-slug/"+slug+"/append", bytes.NewReader(raw)), "alice@example.com")
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusGone {
		t.Errorf("expected 410 Gone for stale idempotency, got %d (body=%s)", rr2.Code, rr2.Body.String())
	}
	if !strings.Contains(rr2.Body.String(), "no longer accessible") {
		t.Errorf("expected staleness explanation in body; got %s", rr2.Body.String())
	}
}

// (Server-side auto-promote was originally part of this PR but PR #32
// landed first with a stricter rule — TEXT + non-textual content is
// rejected outright, no silent promotion. The friendly auto-promote
// now lives in `arti add` client-side; the server rejects ambiguity
// so MCP/REST callers can't accidentally write binary as TEXT either.
// `TestCreateRejectsExplicitTextBinary` below covers the surviving
// rejection path.)

func TestCreateRejectsExplicitTextBinary(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	// Caller EXPLICITLY said TEXT with a PNG — that's a programmer error,
	// reject with a helpful message so they switch to ATTACHMENT.
	body := map[string]any{
		"title":          "explicit-text.png",
		"content_type":   "image/png",
		"artifact_type":  "TEXT",
		"content_base64": "iVBORw0KGgo=",
	}
	raw, _ := json.Marshal(body)
	req := withAuth(
		httptest.NewRequest(http.MethodPost, "/api/artifacts", bytes.NewReader(raw)),
		"alice@example.com",
	)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "ATTACHMENT") {
		t.Errorf("error body should steer caller toward ATTACHMENT; got %s", rr.Body.String())
	}
}

func TestAppendConcurrentRace(t *testing.T) {
	// Best-effort race test: fire N parallel appends to the same slug.
	// Each should land as its own version (1..N+1) and no entries
	// should be lost — the retry loop absorbs (slug, version) races.
	const N = 5
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	slug := uniqueSlug("append-race")
	// Seed v1.
	_, err := st.Put(context.Background(), pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		NamedSlug:    &slug,
		Title:        "race-seed",
		ContentType:  "text/plain",
		Content:      []byte("S"),
		Creator:      "alice@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			body := artifacts.AppendRequest{Content: "x"}
			raw, _ := json.Marshal(body)
			req := withAuth(
				httptest.NewRequest(http.MethodPost, "/api/artifacts/by-slug/"+slug+"/append", bytes.NewReader(raw)),
				"alice@example.com",
			)
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				errs <- &raceErr{status: rr.Code, body: rr.Body.String()}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent append failed: %v", e)
	}

	// Final version should be N+1 (seed=1, +N appends).
	row, _ := st.GetBySlug(context.Background(), slug, nil)
	if row.Version == nil || int(*row.Version) != N+1 {
		t.Errorf("after %d concurrent appends expected v%d, got %v", N, N+1, row.Version)
	}
}

type raceErr struct {
	status int
	body   string
}

func (r *raceErr) Error() string { return "status=" + http.StatusText(r.status) + " body=" + r.body }

func strPtr(s string) *string { return &s }
