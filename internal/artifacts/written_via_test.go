//go:build integration

package artifacts_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// withCred returns a request authenticated as email holding a specific
// credential, which is what the auth middleware leaves behind.
func withCred(req *http.Request, email string, cred auth.Credential) *http.Request {
	ctx := auth.WithIdentity(req.Context(), email)
	return req.WithContext(auth.WithTestCredential(ctx, cred))
}

func newRouterAndStore(t *testing.T) (*chi.Mux, *pgstore.Store) {
	t.Helper()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	r := chi.NewRouter()
	artifacts.Mount(r, artifacts.NewService(st, "http://localhost", nil, nil))
	return r, st
}

func create(t *testing.T, r *chi.Mux, req artifacts.CreateRequest, email string, cred auth.Credential) artifacts.ArtifactInfo {
	t.Helper()
	raw, _ := json.Marshal(req)
	httpReq := withCred(httptest.NewRequest(http.MethodPost, "/api/artifacts", bytes.NewReader(raw)), email, cred)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httpReq)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", rr.Code, rr.Body.String())
	}
	var out artifacts.ArtifactInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// The incident this exists for: ~260 documents written by an agent holding one
// person's API key were indistinguishable from that person's own uploads,
// because `creator` records the key's OWNER. The document has to say which
// credential wrote it, and the name has to travel with it — a viewer cannot
// list someone else's keys to resolve an id.
func TestCreateRecordsTheWritingCredential(t *testing.T) {
	r, _ := newRouterAndStore(t)
	slug := uniqueSlug("via-key")
	keyCred := auth.Credential{
		Kind: auth.CredKindAPIKey, ID: "3f2a1c00-0000-4000-8000-000000000001",
		Label: "cn-angellist", Source: auth.CredSourceBearer,
	}

	got := create(t, r, artifacts.CreateRequest{
		NamedSlug:   &slug,
		Title:       "Written by a key",
		ContentType: "text/markdown",
		Content:     "body",
	}, "owner@example.com", keyCred)

	if got.WrittenVia == nil || *got.WrittenVia != keyCred.Ref() {
		t.Fatalf("written_via = %v, want %q", got.WrittenVia, keyCred.Ref())
	}
	if got.WrittenViaName == nil || *got.WrittenViaName != "cn-angellist" {
		t.Fatalf("written_via_name = %v, want the key's name", got.WrittenViaName)
	}
	if got.Creator != "owner@example.com" {
		t.Errorf("creator = %q; attribution must not change who owns the document", got.Creator)
	}
}

// A browser upload is stamped too, so an unstamped document means "written
// before attribution shipped" rather than "written by a person".
func TestCreateFromBrowserRecordsSession(t *testing.T) {
	r, _ := newRouterAndStore(t)
	slug := uniqueSlug("via-session")

	got := create(t, r, artifacts.CreateRequest{
		NamedSlug:   &slug,
		Title:       "Written in a browser",
		ContentType: "text/markdown",
		Content:     "body",
	}, "owner@example.com", auth.Credential{Kind: auth.CredKindSession, Source: auth.CredSourceCookie})

	if got.WrittenVia == nil || *got.WrittenVia != "session" {
		t.Fatalf("written_via = %v, want \"session\"", got.WrittenVia)
	}
	if got.WrittenViaName != nil && *got.WrittenViaName != "" {
		t.Errorf("written_via_name = %v; a session has no name to show", got.WrittenViaName)
	}
}

// Appending is a write like any other, and the version it produces belongs to
// whichever credential appended — not to whoever created v1.
func TestAppendRecordsTheAppendingCredential(t *testing.T) {
	r, _ := newRouterAndStore(t)
	slug := uniqueSlug("via-append")
	create(t, r, artifacts.CreateRequest{
		NamedSlug:   &slug,
		Title:       "Seeded in a browser",
		ContentType: "text/markdown",
		Content:     "v1",
	}, "owner@example.com", auth.Credential{Kind: auth.CredKindSession, Source: auth.CredSourceCookie})

	keyCred := auth.Credential{Kind: auth.CredKindAPIKey, ID: "k9", Label: "agent-key", Source: auth.CredSourceBearer}
	raw, _ := json.Marshal(artifacts.AppendRequest{Content: "v2"})
	req := withCred(httptest.NewRequest(http.MethodPost, "/api/artifacts/by-slug/"+slug+"/append", bytes.NewReader(raw)),
		"owner@example.com", keyCred)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
		t.Fatalf("append: status %d, body %s", rr.Code, rr.Body.String())
	}
	var out artifacts.ArtifactInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.WrittenVia == nil || *out.WrittenVia != "apikey:k9" {
		t.Fatalf("written_via = %v, want the appending key", out.WrittenVia)
	}
}

// `via:` is how a person finds everything one credential wrote — the question
// asked when a key turns out to have been shared.
func TestCatalogFiltersByCredential(t *testing.T) {
	r, _ := newRouterAndStore(t)
	keyCred := auth.Credential{Kind: auth.CredKindAPIKey, ID: uniqueSlug("k"), Label: "shared-key", Source: auth.CredSourceBearer}
	slug := uniqueSlug("via-filter")
	create(t, r, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "Key wrote this", ContentType: "text/markdown", Content: "x",
	}, "owner@example.com", keyCred)
	other := uniqueSlug("via-filter-other")
	create(t, r, artifacts.CreateRequest{
		NamedSlug: &other, Title: "Browser wrote this", ContentType: "text/markdown", Content: "x",
	}, "owner@example.com", auth.Credential{Kind: auth.CredKindSession, Source: auth.CredSourceCookie})

	req := withCred(httptest.NewRequest(http.MethodGet, "/api/artifacts?via="+keyCred.Ref(), nil),
		"owner@example.com", auth.Credential{Kind: auth.CredKindSession, Source: auth.CredSourceCookie})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: status %d, body %s", rr.Code, rr.Body.String())
	}
	var page artifacts.ListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Artifacts) != 1 {
		t.Fatalf("artifacts = %d, want only the document that key wrote", len(page.Artifacts))
	}
	if page.Artifacts[0].NamedSlug == nil || *page.Artifacts[0].NamedSlug != slug {
		t.Errorf("got %v, want %q", page.Artifacts[0].NamedSlug, slug)
	}
}

// `via:` has to survive the search path, not only the list path. Searching
// combines the filter with free text, and a search that quietly returned other
// credentials' documents would be worse than one that returned nothing.
func TestSearchFiltersByCredential(t *testing.T) {
	r, _ := newRouterAndStore(t)
	keyCred := auth.Credential{Kind: auth.CredKindAPIKey, ID: uniqueSlug("k"), Label: "shared-key", Source: auth.CredSourceBearer}
	marker := uniqueSlug("needle")
	slug := uniqueSlug("via-search")
	create(t, r, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "Key wrote this " + marker, ContentType: "text/markdown", Content: "x",
	}, "owner@example.com", keyCred)
	other := uniqueSlug("via-search-other")
	create(t, r, artifacts.CreateRequest{
		NamedSlug: &other, Title: "Browser wrote this " + marker, ContentType: "text/markdown", Content: "x",
	}, "owner@example.com", auth.Credential{Kind: auth.CredKindSession, Source: auth.CredSourceCookie})

	q := "via:" + keyCred.Ref() + " " + marker
	req := withCred(httptest.NewRequest(http.MethodGet, "/api/artifacts/search?q="+url.QueryEscape(q), nil),
		"owner@example.com", auth.Credential{Kind: auth.CredKindSession, Source: auth.CredSourceCookie})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("search: status %d, body %s", rr.Code, rr.Body.String())
	}
	var page artifacts.ListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Artifacts) != 1 {
		t.Fatalf("artifacts = %d, want only the key's document", len(page.Artifacts))
	}
	if page.Artifacts[0].NamedSlug == nil || *page.Artifacts[0].NamedSlug != slug {
		t.Errorf("got %v, want %q", page.Artifacts[0].NamedSlug, slug)
	}
}
