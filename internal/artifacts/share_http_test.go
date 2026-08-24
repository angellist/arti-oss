//go:build integration

package artifacts_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/artifacts"
)

// shareAPI wraps the owner-facing endpoints with a router that injects an
// identity, standing in for the auth middleware.
type shareAPI struct {
	t   *testing.T
	svc *artifacts.Service
	mux chi.Router
}

func newShareAPI(t *testing.T) *shareAPI {
	t.Helper()
	svc := newShareService(t)
	r := chi.NewRouter()
	artifacts.MountShareAdmin(r, svc, nil)
	return &shareAPI{t: t, svc: svc, mux: r}
}

func (a *shareAPI) do(method, path, body, as string) *httptest.ResponseRecorder {
	a.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := authedRequest(a.t, method, path, rdr, as)
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req)
	return rec
}

func TestShareHTTP_MintAuthorityAndStatuses(t *testing.T) {
	api := newShareAPI(t)
	const owner, writer, stranger = "owner@example.com", "writer@example.com", "stranger@example.com"
	art := createArtifact(t, api.svc, owner, "http-mint",
		withAccess(owner, writer), withWrite(owner, writer))
	path := "/api/artifacts/" + art.ArtifactID + "/shares"

	rec := api.do("POST", path, `{"scope":"version","ttl":"1h"}`, owner)
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner mint: %d %s", rec.Code, rec.Body.String())
	}
	var got artifacts.MintShareResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.URL, "/share/") || got.TokenPrefix == "" {
		t.Fatalf("mint result = %+v", got)
	}

	// A writer can see the document, so the refusal is 403.
	if rec := api.do("POST", path, `{"scope":"version","ttl":"1h"}`, writer); rec.Code != http.StatusForbidden {
		t.Errorf("writer mint: %d, want 403", rec.Code)
	}
	// A stranger cannot, so the refusal is 404 — a 403 would confirm the
	// document exists to someone with no access to it.
	if rec := api.do("POST", path, `{"scope":"version","ttl":"1h"}`, stranger); rec.Code != http.StatusNotFound {
		t.Errorf("stranger mint: %d, want 404", rec.Code)
	}
	// Bad input is 400, not 500.
	if rec := api.do("POST", path, `{"scope":"version","ttl":"99h"}`, owner); rec.Code != http.StatusBadRequest {
		t.Errorf("bad ttl: %d, want 400", rec.Code)
	}
}

func TestShareHTTP_ListNeverCarriesAToken(t *testing.T) {
	api := newShareAPI(t)
	const owner = "owner@example.com"
	art := createArtifact(t, api.svc, owner, "http-list")
	path := "/api/artifacts/" + art.ArtifactID + "/shares"

	rec := api.do("POST", path, `{"scope":"version","ttl":"1h"}`, owner)
	var minted artifacts.MintShareResult
	_ = json.Unmarshal(rec.Body.Bytes(), &minted)
	token := minted.URL[strings.LastIndex(minted.URL, "/")+1:]

	list := api.do("GET", path, "", owner)
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), token) {
		t.Fatalf("list response leaks the token: %s", list.Body.String())
	}
}

func TestShareHTTP_RevokeAndOpens(t *testing.T) {
	api := newShareAPI(t)
	const owner, other = "owner@example.com", "other@example.com"
	art := createArtifact(t, api.svc, owner, "http-revoke", withAccess(owner, other))

	rec := api.do("POST", "/api/artifacts/"+art.ArtifactID+"/shares", `{"scope":"version","ttl":"1h"}`, owner)
	var minted artifacts.MintShareResult
	_ = json.Unmarshal(rec.Body.Bytes(), &minted)

	if r := api.do("GET", "/api/shares/"+minted.ID+"/opens", "", other); r.Code != http.StatusForbidden {
		t.Errorf("non-owner opens: %d, want 403", r.Code)
	}
	if r := api.do("GET", "/api/shares/"+minted.ID+"/opens", "", owner); r.Code != http.StatusOK {
		t.Errorf("owner opens: %d %s", r.Code, r.Body.String())
	}
	if r := api.do("DELETE", "/api/shares/"+minted.ID, "", other); r.Code != http.StatusForbidden {
		t.Errorf("non-owner revoke: %d, want 403", r.Code)
	}
	if r := api.do("DELETE", "/api/shares/"+minted.ID, "", owner); r.Code != http.StatusNoContent {
		t.Errorf("owner revoke: %d %s", r.Code, r.Body.String())
	}
}

// With the feature off the endpoints are absent, not forbidden: a disabled
// feature should look like it does not exist.
func TestShareHTTP_DisabledMountsNothing(t *testing.T) {
	svc := newShareService(t)
	art := createArtifact(t, svc, "owner@example.com", "http-off")
	svc.SetShareConfig(artifacts.ShareConfig{Enabled: false})

	r := chi.NewRouter()
	artifacts.MountShareAdmin(r, svc, nil)
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, authedRequest(t, "POST",
		"/api/artifacts/"+art.ArtifactID+"/shares",
		strings.NewReader(`{"scope":"version","ttl":"1h"}`), "owner@example.com"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled mint: %d, want 404", rec.Code)
	}
}
