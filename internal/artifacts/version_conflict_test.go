//go:build integration

package artifacts_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// postAs posts a JSON create body as `email`, with the claims a real caller of
// that kind would carry. The claims matter: the ATTACHMENT slug rule keys off
// them, and auth.WithIdentity alone (no claims) reads as a service credential.
func postAs(t *testing.T, r chi.Router, email string, claims auth.Claims, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/artifacts", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	claims.Email = email
	req = req.WithContext(auth.WithTestClaims(req.Context(), claims))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func session() auth.Claims { return auth.Claims{Scopes: []string{"user"}} }
func apiKey() auth.Claims {
	return auth.Claims{Scopes: []string{"user"}, Typ: auth.TokenTypeAPIKey}
}

func errCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode error envelope %q: %v", rr.Body.String(), err)
	}
	return e.Code
}

func createdInfo(t *testing.T, rr *httptest.ResponseRecorder) artifacts.ArtifactInfo {
	t.Helper()
	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var info artifacts.ArtifactInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return info
}

func router(t *testing.T) chi.Router {
	t.Helper()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	r := chi.NewRouter()
	artifacts.Mount(r, artifacts.NewService(st, "http://localhost", nil, nil))
	return r
}

func textBody(slug, ct, content string, extra map[string]any) map[string]any {
	m := map[string]any{
		"title":         "Notes",
		"named_slug":    slug,
		"content_type":  ct,
		"artifact_type": "TEXT",
		"content":       content,
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// The race the web modal recovers from: someone publishes while an upload is
// being prepared. Pinning the base version turns a silent overwrite into a 409.
func TestCreate_RejectsAStaleBaseVersion(t *testing.T) {
	r := router(t)
	slug := uniqueSlug("stale-base")

	v1 := createdInfo(t, postAs(t, r, "tian@example.com", session(), textBody(slug, "text/markdown", "one", nil)))
	if v1.Version == nil || *v1.Version != 1 {
		t.Fatalf("want v1, got %+v", v1.Version)
	}
	// A concurrent publish lands.
	createdInfo(t, postAs(t, r, "other@example.com", session(), textBody(slug, "text/markdown", "two", nil)))

	rr := postAs(t, r, "tian@example.com", session(),
		textBody(slug, "text/markdown", "three", map[string]any{"expected_latest_version": 1}))
	if rr.Code != http.StatusConflict || errCode(t, rr) != "stale-base-version" {
		t.Fatalf("want 409 stale-base-version, got %d %s", rr.Code, rr.Body.String())
	}

	// And nothing was written: the slug is still at v2.
	getReq := httptest.NewRequest(http.MethodGet, "/api/artifacts/by-slug/"+slug, nil)
	getReq = getReq.WithContext(auth.WithTestClaims(getReq.Context(), auth.Claims{Email: "tian@example.com", Scopes: []string{"user"}}))
	getRR := httptest.NewRecorder()
	r.ServeHTTP(getRR, getReq)
	var latest artifacts.ArtifactInfo
	if err := json.Unmarshal(getRR.Body.Bytes(), &latest); err != nil {
		t.Fatalf("decode latest: %v", err)
	}
	if latest.Version == nil || *latest.Version != 2 {
		t.Fatalf("rejected publish must not create a version; slug is at %v", latest.Version)
	}

	// Re-aimed at the version that is actually there, it goes through.
	v3 := createdInfo(t, postAs(t, r, "tian@example.com", session(),
		textBody(slug, "text/markdown", "three", map[string]any{"expected_latest_version": 2})))
	if v3.Version == nil || *v3.Version != 3 {
		t.Fatalf("want v3, got %v", v3.Version)
	}
}

// Changing what the document IS needs saying so — including a content-type
// change inside TEXT, which is how an HTML page silently became markdown.
func TestCreate_TypeChangeNeedsOptIn(t *testing.T) {
	r := router(t)
	slug := uniqueSlug("type-change")

	createdInfo(t, postAs(t, r, "tian@example.com", session(), textBody(slug, "text/html", "<h1>hi</h1>", nil)))

	rr := postAs(t, r, "tian@example.com", session(), textBody(slug, "text/markdown", "# hi", nil))
	if rr.Code != http.StatusConflict || errCode(t, rr) != "type-change" {
		t.Fatalf("want 409 type-change, got %d %s", rr.Code, rr.Body.String())
	}

	// Re-publishing the same content type is not a change.
	createdInfo(t, postAs(t, r, "tian@example.com", session(), textBody(slug, "text/html; charset=utf-8", "<h1>ho</h1>", nil)))

	ok := createdInfo(t, postAs(t, r, "tian@example.com", session(),
		textBody(slug, "text/markdown", "# hi", map[string]any{"allow_type_change": true})))
	if ok.ContentType != "text/markdown" {
		t.Fatalf("want the acknowledged change to land, got %q", ok.ContentType)
	}
}

// A person naming a slug on a binary publishes a versioned document; the same
// request from a service credential is refused rather than silently stripped
// of its slug, which is what couch's chat uploads relied on.
func TestCreate_SluggedAttachment(t *testing.T) {
	r := router(t)
	slug := uniqueSlug("slugged-attachment")
	body := func() map[string]any {
		return map[string]any{
			"title":          "Pixel",
			"named_slug":     slug,
			"content_type":   "image/png",
			"artifact_type":  "ATTACHMENT",
			"content_base64": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwADhQGAWjR9awAAAABJRU5ErkJggg==",
		}
	}

	v1 := createdInfo(t, postAs(t, r, "tian@example.com", session(), body()))
	if v1.NamedSlug == nil || *v1.NamedSlug != slug {
		t.Fatalf("interactive caller should keep the slug, got %v", v1.NamedSlug)
	}
	v2 := createdInfo(t, postAs(t, r, "tian@example.com", session(), body()))
	if v2.Version == nil || *v2.Version != 2 {
		t.Fatalf("a slugged attachment should version, got %v", v2.Version)
	}

	rr := postAs(t, r, "svc@example.com", apiKey(), map[string]any{
		"title":          "Chat file",
		"named_slug":     "service-" + slug,
		"content_type":   "image/png",
		"artifact_type":  "ATTACHMENT",
		"content_base64": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwADhQGAWjR9awAAAABJRU5ErkJggg==",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a service credential naming a slug, got %d: %s", rr.Code, rr.Body.String())
	}

	// Without a slug it is the chat upload it always was: slugless, creator-only.
	sl := createdInfo(t, postAs(t, r, "svc@example.com", apiKey(), map[string]any{
		"title":          "Chat file",
		"content_type":   "image/png",
		"artifact_type":  "ATTACHMENT",
		"content_base64": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwADhQGAWjR9awAAAABJRU5ErkJggg==",
	}))
	if sl.NamedSlug != nil || len(sl.AllowedAccess) != 0 {
		t.Fatalf("want slugless + creator-only, got slug=%v access=%v", sl.NamedSlug, sl.AllowedAccess)
	}
}

// The multipart front door carries the same version controls as the JSON one.
// Without them a `409 type-change` would be a dead end for exactly the callers
// that upload binaries — the ones most likely to change a document's type.
func TestCreateMultipart_CanOptIntoATypeChange(t *testing.T) {
	r := router(t)
	slug := uniqueSlug("multipart-type-change")
	createdInfo(t, postAs(t, r, "tian@example.com", session(), textBody(slug, "text/markdown", "notes", nil)))

	post := func(fields map[string]string) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile("file", "pixel.png")
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := fw.Write(minimalPNG); err != nil {
			t.Fatalf("write file bytes: %v", err)
		}
		for k, v := range fields {
			_ = mw.WriteField(k, v)
		}
		_ = mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/artifacts", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req = req.WithContext(auth.WithTestClaims(req.Context(), auth.Claims{Email: "tian@example.com", Scopes: []string{"user"}}))
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}

	base := map[string]string{
		"title":         "Pixel",
		"named_slug":    slug,
		"content_type":  "image/png",
		"artifact_type": "ATTACHMENT",
	}
	rr := post(base)
	if rr.Code != http.StatusConflict || errCode(t, rr) != "type-change" {
		t.Fatalf("want 409 type-change, got %d %s", rr.Code, rr.Body.String())
	}

	base["allow_type_change"] = "true"
	base["expected_latest_version"] = "1"
	info := createdInfo(t, post(base))
	if info.ArtifactType != "ATTACHMENT" || info.Version == nil || *info.Version != 2 {
		t.Fatalf("want an ATTACHMENT v2, got %s v%v", info.ArtifactType, info.Version)
	}

	base["expected_latest_version"] = "1" // now stale: the slug is at v2
	if rr := post(base); rr.Code != http.StatusConflict || errCode(t, rr) != "stale-base-version" {
		t.Fatalf("want 409 stale-base-version, got %d %s", rr.Code, rr.Body.String())
	}

	base["expected_latest_version"] = "not-a-number"
	if rr := post(base); rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a non-integer expected_latest_version, got %d", rr.Code)
	}
}
