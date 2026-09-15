//go:build integration

package artifacts_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

const (
	denialOwner    = "owner@example.com"
	denialStranger = "stranger@example.com"
)

type denialAPI struct {
	t   *testing.T
	svc *artifacts.Service
	mux chi.Router
}

func newDenialAPI(t *testing.T) *denialAPI {
	t.Helper()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "https://arti.example.com", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)
	return &denialAPI{t: t, svc: svc, mux: r}
}

// get issues a request carrying `claims` as the verified credential. The
// package's authedRequest helper sets only an email, which leaves
// ClaimsFromContext empty — and the denial route reads the credential's kind,
// not just its address.
func (a *denialAPI) get(path string, claims auth.Claims) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(auth.WithTestClaims(req.Context(), claims))
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req)
	return rec
}

func denialSession(email string) auth.Claims {
	return auth.Claims{Email: email, Scopes: []string{"user"}}
}

func denialAPIKey(email string) auth.Claims {
	return auth.Claims{Email: email, Scopes: []string{"user"}, Typ: auth.TokenTypeAPIKey}
}

// A person holding a link to a document they cannot read is the case this
// route exists for: they must learn what it is and who to ask, so the link
// stops being indistinguishable from a typo.
func TestDenial_DeniedReaderLearnsTitleAndOwner(t *testing.T) {
	api := newDenialAPI(t)
	art := createArtifact(t, api.svc, denialOwner, "denial-basic", withAccess(denialOwner))

	rec := api.get("/api/artifacts/by-slug/"+*art.NamedSlug+"/denial", denialSession(denialStranger))
	if rec.Code != http.StatusOK {
		t.Fatalf("denied reader: %d %s, want 200", rec.Code, rec.Body.String())
	}
	var got artifacts.DenialInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != art.Title || got.Owner != denialOwner || got.NamedSlug == nil || *got.NamedSlug != *art.NamedSlug {
		t.Fatalf("payload = %+v, want title %q owner %q slug %q", got, art.Title, denialOwner, *art.NamedSlug)
	}
}

// The disclosure is capped at what unsticks the reader. A field added to the
// DTO without thinking about who reads it would widen the leak silently, so
// the wire shape is asserted key-by-key rather than field-by-field.
func TestDenial_DisclosesNothingBeyondNameSlugAndOwner(t *testing.T) {
	api := newDenialAPI(t)
	art := createArtifact(t, api.svc, denialOwner, "denial-shape", withAccess(denialOwner))

	rec := api.get("/api/artifacts/by-slug/"+*art.NamedSlug+"/denial", denialSession(denialStranger))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"named_slug", "owner", "title", "version"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want exactly %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %v, want exactly %v", keys, want)
		}
	}
}

// A slug nobody ever published must stay indistinguishable from one the
// caller merely cannot read — otherwise the route is a catalog oracle.
func TestDenial_UnknownSlugIsNotFound(t *testing.T) {
	api := newDenialAPI(t)
	if rec := api.get("/api/artifacts/by-slug/no-such-slug-here/denial", denialSession(denialStranger)); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown slug: %d %s, want 404", rec.Code, rec.Body.String())
	}
}

// Archiving is how an owner takes a document down. Naming it on the denial
// page would undo that, so an archived slug keeps its 404.
func TestDenial_ArchivedSlugIsNotFound(t *testing.T) {
	api := newDenialAPI(t)
	art := createArtifact(t, api.svc, denialOwner, "denial-archived", withAccess(denialOwner))
	if _, err := api.svc.ArchiveBySlug(context.Background(), *art.NamedSlug); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if rec := api.get("/api/artifacts/by-slug/"+*art.NamedSlug+"/denial", denialSession(denialStranger)); rec.Code != http.StatusNotFound {
		t.Fatalf("archived slug: %d %s, want 404", rec.Code, rec.Body.String())
	}
}

// The route unsticks a person at a browser. A long-lived service credential
// could sweep the catalog with it, so it is refused the disclosure.
func TestDenial_APIKeyCredentialIsRefused(t *testing.T) {
	api := newDenialAPI(t)
	art := createArtifact(t, api.svc, denialOwner, "denial-apikey", withAccess(denialOwner))
	if rec := api.get("/api/artifacts/by-slug/"+*art.NamedSlug+"/denial", denialAPIKey(denialStranger)); rec.Code != http.StatusNotFound {
		t.Fatalf("api-key caller: %d %s, want 404", rec.Code, rec.Body.String())
	}
}

// Nothing was denied, so there is nothing to explain — the reader should be
// loading the document, not this page.
func TestDenial_ReaderWithAccessGetsNotFound(t *testing.T) {
	api := newDenialAPI(t)
	art := createArtifact(t, api.svc, denialOwner, "denial-allowed", withAccess(denialOwner, denialStranger))
	if rec := api.get("/api/artifacts/by-slug/"+*art.NamedSlug+"/denial", denialSession(denialStranger)); rec.Code != http.StatusNotFound {
		t.Fatalf("permitted reader: %d %s, want 404", rec.Code, rec.Body.String())
	}
}

// The invariant this change is closest to breaking: the ordinary read path
// must keep collapsing "no such artifact" and "not for you" into one 404,
// with no hint of the document in the body.
func TestDenial_OrdinaryReadStaysUniform(t *testing.T) {
	api := newDenialAPI(t)
	art := createArtifact(t, api.svc, denialOwner, "denial-uniform", withAccess(denialOwner))

	restricted := api.get("/api/artifacts/by-slug/"+*art.NamedSlug, denialSession(denialStranger))
	missing := api.get("/api/artifacts/by-slug/no-such-slug-at-all", denialSession(denialStranger))
	if restricted.Code != http.StatusNotFound || missing.Code != http.StatusNotFound {
		t.Fatalf("statuses = %d / %d, want 404 / 404", restricted.Code, missing.Code)
	}
	rb, _ := io.ReadAll(restricted.Body)
	mb, _ := io.ReadAll(missing.Body)
	if string(rb) != string(mb) {
		t.Fatalf("bodies diverged:\n restricted %s\n missing    %s", rb, mb)
	}
}

// The /a/<uuid> route lands on the same page as /s/<slug>, so it needs the
// same disclosure.
func TestDenial_ByIDMatchesBySlug(t *testing.T) {
	api := newDenialAPI(t)
	art := createArtifact(t, api.svc, denialOwner, "denial-by-id", withAccess(denialOwner))

	rec := api.get("/api/artifacts/"+art.ArtifactID+"/denial", denialSession(denialStranger))
	if rec.Code != http.StatusOK {
		t.Fatalf("by id: %d %s, want 200", rec.Code, rec.Body.String())
	}
	var got artifacts.DenialInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Owner != denialOwner || got.Title != art.Title {
		t.Fatalf("payload = %+v", got)
	}
}

// Versioning reassigns a row's `creator` to whoever pushed it, so the latest
// version of a document can be credited to a delegated writer who has no
// authority to change its ACL. The page must name the person who can actually
// grant access — the slug's owner — or it sends the reader to someone who will
// have to forward the request.
func TestDenial_NamesSlugOwnerNotLatestVersionCreator(t *testing.T) {
	api := newDenialAPI(t)
	ctx := context.Background()
	const writer = "writer@example.com"

	slug := uniqueSlug("denial-owner")
	readers := []string{denialOwner}
	writers := []string{writer}
	if _, err := api.svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &readers, AllowedWrite: &writers,
	}, denialOwner); err != nil {
		t.Fatalf("v1: %v", err)
	}
	v2, err := api.svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v2", ContentType: "text/markdown", Content: "v2",
	}, writer)
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	if v2.Creator != writer {
		t.Fatalf("fixture: latest version creator = %q, want %q", v2.Creator, writer)
	}

	rec := api.get("/api/artifacts/by-slug/"+slug+"/denial", denialSession(denialStranger))
	if rec.Code != http.StatusOK {
		t.Fatalf("denied reader: %d %s, want 200", rec.Code, rec.Body.String())
	}
	var got artifacts.DenialInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Owner != denialOwner {
		t.Fatalf("owner = %q, want the slug owner %q", got.Owner, denialOwner)
	}
}

// A reader who can still open an older version is not shut out of the
// document, so a restricted newer version's title and number are not theirs to
// see. Every ACL narrowing through the API converges across the slug, so the
// diverged state is built directly in the table — legacy rows and a partial
// convergence can both leave one behind.
func TestDenial_HidesRestrictedNewerVersionFromAnOlderVersionsReader(t *testing.T) {
	api := newDenialAPI(t)
	ctx := context.Background()
	pool := newPool(t)

	slug := uniqueSlug("denial-diverged")
	world := []string{"*"}
	if _, err := api.svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1 open", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world,
	}, denialOwner); err != nil {
		t.Fatalf("v1: %v", err)
	}
	if _, err := api.svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v2 restricted", ContentType: "text/markdown", Content: "v2",
	}, denialOwner); err != nil {
		t.Fatalf("v2: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE artifacts SET allowed_access = $2 WHERE named_slug = $1 AND version = 2`,
		slug, []string{denialOwner}); err != nil {
		t.Fatalf("diverge v2: %v", err)
	}

	// The scenario only exists if the stranger really can still read v1.
	if _, err := api.svc.GetBySlug(ctx, slug, nil, denialStranger); err != nil {
		t.Fatalf("fixture: stranger should still read v1: %v", err)
	}

	rec := api.get("/api/artifacts/by-slug/"+slug+"/denial", denialSession(denialStranger))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("reader of an older version: %d %s, want 404", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "v2 restricted") {
		t.Fatalf("leaked the restricted version's title: %s", rec.Body.String())
	}
}
