//go:build integration

package artifacts_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// minimalPNG is a 1×1 pixel PNG — real magic bytes, no base64 encoding.
var minimalPNG = func() []byte {
	const enc = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwADhQGAWjR9awAAAABJRU5ErkJggg=="
	b, _ := base64.StdEncoding.DecodeString(enc)
	return b
}()

// buildMultipartBody creates a multipart/form-data body. When includeFile is
// false the `file` part is omitted to exercise the missing-part error path.
func buildMultipartBody(t *testing.T, includeFile bool) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	if includeFile {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="file"; filename="pixel.png"`)
		h.Set("Content-Type", "image/png")
		fw, err := mw.CreatePart(h)
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := fw.Write(minimalPNG); err != nil {
			t.Fatalf("write file bytes: %v", err)
		}
	}

	_ = mw.WriteField("title", "Multipart PNG Test")
	_ = mw.WriteField("content_type", "image/png")
	_ = mw.WriteField("artifact_type", "ATTACHMENT")
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func authedRequest(t *testing.T, method, target string, body io.Reader, email string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	return req.WithContext(auth.WithIdentity(req.Context(), email))
}

// TestCreateMultipart_HappyPath posts a multipart/form-data body containing a
// raw PNG `file` part and asserts the artifact is stored with identical bytes
// (no base64 round-trip).
func TestCreateMultipart_HappyPath(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	body, ct := buildMultipartBody(t, true)
	req := authedRequest(t, http.MethodPost, "/api/artifacts", body, "tian@example.com")
	req.Header.Set("Content-Type", ct)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("POST /api/artifacts: want 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var info artifacts.ArtifactInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	if info.ArtifactID == "" {
		t.Fatal("response missing artifact_id")
	}

	// Fetch the raw content via GET /api/artifacts/{id} and compare bytes.
	getReq := authedRequest(t, http.MethodGet, "/api/artifacts/"+info.ArtifactID, nil, "tian@example.com")
	getRR := httptest.NewRecorder()
	r.ServeHTTP(getRR, getReq)

	if getRR.Code != http.StatusOK {
		t.Fatalf("GET /api/artifacts/%s: want 200, got %d: %s", info.ArtifactID, getRR.Code, getRR.Body.String())
	}
	stored := getRR.Body.Bytes()
	if !bytes.Equal(stored, minimalPNG) {
		t.Fatalf("stored bytes differ: got len=%d, want len=%d", len(stored), len(minimalPNG))
	}
}

// TestCreateMultipart_MissingFilePart posts a multipart body without a `file`
// part and expects a 400 Bad Request.
func TestCreateMultipart_MissingFilePart(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	r := chi.NewRouter()
	artifacts.Mount(r, svc)

	body, ct := buildMultipartBody(t, false)
	req := authedRequest(t, http.MethodPost, "/api/artifacts", body, "tian@example.com")
	req.Header.Set("Content-Type", ct)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}
