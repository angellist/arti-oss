//go:build integration

package artifacts_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// unique appends a random suffix so cohort values in the shared test DB
// can't collide with other tests' data.
func unique(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

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

func TestAggregatesHTTP(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	_, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		Title:        "agg-http-fixture",
		ContentType:  "text/plain",
		Content:      []byte("x"),
		Creator:      "alice@example.com",
		Scopes:       []string{"user:alice@example.com"},
		Labels:       []string{"memory"},
	})
	if err != nil {
		t.Fatal(err)
	}

	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/aggregates", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var body artifacts.AggregatesResponse
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.ScopeTypes) == 0 {
		t.Fatalf("expected scope_types in response, got %+v", body)
	}
	if len(body.Labels) == 0 {
		t.Fatalf("expected labels in response, got %+v", body)
	}
	if len(body.Scopes) == 0 {
		t.Fatalf("expected scopes (full values) in response, got %+v", body)
	}
	if len(body.ContentTypes) == 0 {
		t.Fatalf("expected content_types in response, got %+v", body)
	}
}

// The Browse page hits /api/artifacts/aggregates/browse for one facet at a
// time, paged and sorted, distinct from the sidebar's fixed top-N
// /api/artifacts/aggregates. Verified end-to-end via unique content_type
// values so counts are exact regardless of the shared test DB's other rows.
func TestBrowseAggregatesHTTP(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	multi := "application/x-" + unique("browsehttp-multi")
	single := "application/x-" + unique("browsehttp-single")
	mk := func(contentType string, n int) {
		for i := 0; i < n; i++ {
			_, err := st.Put(ctx, pgstore.PutInput{
				ArtifactType: pgstore.TypeText, Title: unique("browsehttp"), ContentType: contentType,
				Content: []byte("x"), Creator: "alice@example.com",
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	mk(multi, 3)
	mk(single, 1)

	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	req := httptest.NewRequest(http.MethodGet,
		"/api/artifacts/aggregates/browse?facet=content_type&sort=count&dir=desc&page=1&page_size=1000", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var body artifacts.BrowseAggregatesResponse
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byValue := map[string]int64{}
	for _, v := range body.Values {
		byValue[v.Value] = v.Count
	}
	if byValue[multi] != 3 {
		t.Fatalf("expected %s count = 3, got %d", multi, byValue[multi])
	}
	if byValue[single] != 1 {
		t.Fatalf("expected %s count = 1, got %d", single, byValue[single])
	}
	if body.Total < 2 {
		t.Fatalf("expected total >= 2, got %d", body.Total)
	}
}

// An unknown facet is a 400, not a 500 or a silent empty result.
func TestBrowseAggregatesHTTP_BadFacet(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/aggregates/browse?facet=bogus", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rr.Code, rr.Body.String())
	}
}

func TestPatchScopes(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	row, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		Title:        "patch-scopes",
		ContentType:  "text/plain",
		Content:      []byte("x"),
		Creator:      "alice@example.com",
		Scopes:       []string{"a:one"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := pgstore.UUIDFromPG(row.ArtifactID).String()

	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	// Inject the creator as the authed caller so the creator-gate passes.
	// auth.WithIdentity(ctx, email) sets ctxEmail, which EmailFromContext
	// (used by httpPatch's creator gate) reads. Verified in middleware.go.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
			next.ServeHTTP(w, rq.WithContext(auth.WithIdentity(rq.Context(), "alice@example.com")))
		})
	})
	artifacts.Mount(r, svc)

	body := `{"scopes":["a:one"," a:two ","a:one",""]}` // dupes + blanks + whitespace
	req := httptest.NewRequest(http.MethodPatch, "/api/artifacts/"+id, strings.NewReader(body))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got artifacts.ArtifactInfo
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	// trimmed, de-duped, blanks dropped, order preserved
	if len(got.Scopes) != 2 || got.Scopes[0] != "a:one" || got.Scopes[1] != "a:two" {
		t.Fatalf("scopes = %v, want [a:one a:two]", got.Scopes)
	}
}
