//go:build integration

package apps_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/apps"
	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/mcp"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	// CI sets ARTI_TEST_DATABASE_URL (Postgres sidecar); fall back to the local
	// dev DB port otherwise. Mirrors internal/mcp's newPool.
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

func uniqueSlug(prefix string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

// recordingProvider is a TokenProvider that records every OBO touch and never
// returns a bearer — so a call that reaches it produces a 401 authorize flow.
// The point of recording is to PROVE that arti read tools never reach OBO.
type recordingProvider struct{ calls int }

func (p *recordingProvider) BearerFor(ctx context.Context, email string, sc apps.ServerConfig) (string, error) {
	p.calls++
	return "", nil
}
func (p *recordingProvider) AuthorizeURL(ctx context.Context, email string, sc apps.ServerConfig) (string, error) {
	return "https://consent.example/authorize", nil
}

func newAppsStack(t *testing.T) (*pgstore.Store, *apps.Service, *recordingProvider) {
	t.Helper()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	signer := auth.NewJWTSigner([]byte("test-signing-key-at-least-32-bytes!"))
	prov := &recordingProvider{}
	servers := map[string]apps.ServerConfig{
		"arti":   {Name: "arti", Auth: "oauth", ResourceURL: "https://example/arti", Scope: "s"},
		"notion": {Name: "notion", Auth: "oauth", ResourceURL: "https://example/notion", Scope: "s"},
		// Unreachable ResourceURL on purpose: reads must short-circuit
		// in-process and never dial it.
		"arti-self": {Name: "arti-self", Auth: "none", ResourceURL: "http://127.0.0.1:1/mcp"},
	}
	appsSvc := apps.New(st, signer, servers, prov, nil, nil)
	appsSvc.SetArtiReader(mcp.NewServer(svc, nil))
	return st, appsSvc, prov
}

func seedText(t *testing.T, st *pgstore.Store, slug, body, creator string, access []string) {
	t.Helper()
	if _, err := st.Put(context.Background(), pgstore.PutInput{
		ArtifactType:  pgstore.TypeText,
		NamedSlug:     &slug,
		Title:         "data " + slug,
		ContentType:   "text/plain",
		Content:       []byte(body),
		Creator:       creator,
		AllowedAccess: access,
	}); err != nil {
		t.Fatalf("seed text: %v", err)
	}
}

// buildAppZip returns APP package bytes carrying the given arti-app.json.
func buildAppZip(t *testing.T, manifest string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"arti-app.json": manifest, "index.html": "<html></html>"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// newAppArtifact stores an APP whose arti-app.json allowlists `tools`, and
// returns its UUID string. Default access ('*'), so any viewer can read the APP.
func newAppArtifact(t *testing.T, st *pgstore.Store, tools []map[string]string) string {
	t.Helper()
	man, _ := json.Marshal(map[string]any{"name": "demo", "entry": "index.html", "tools": tools})
	slug := uniqueSlug("data-app")
	row, err := st.Put(context.Background(), pgstore.PutInput{
		ArtifactType: pgstore.TypeApp,
		NamedSlug:    &slug,
		Title:        "demo app",
		ContentType:  "application/zip",
		Content:      buildAppZip(t, string(man)),
		Creator:      "alice@example.com",
	})
	if err != nil {
		t.Fatalf("put app: %v", err)
	}
	return uuid.UUID(row.ArtifactID.Bytes).String()
}

func proxyPost(t *testing.T, h http.Handler, token, appID, server, tool string, args map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"app_id": appID, "server": server, "tool": tool, "arguments": args})
	req := httptest.NewRequest(http.MethodPost, "/api/apps/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func mustToken(t *testing.T, s *apps.Service, email, appID string) string {
	t.Helper()
	tok, err := s.SignAppToken(email, appID)
	if err != nil {
		t.Fatalf("sign app token: %v", err)
	}
	return tok
}

// A viewer's APP reads another artifact in-process, as the viewer: 200 with the
// content, and the OBO provider is NEVER touched (no consent popup).
func TestProxyArtiReadShortCircuits(t *testing.T) {
	st, appsSvc, prov := newAppsStack(t)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	dataSlug := uniqueSlug("data")
	seedText(t, st, dataSlug, "hello data", "alice@example.com", nil)

	appID := newAppArtifact(t, st, []map[string]string{{"server": "arti", "tool": "read_artifact"}})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	rr := proxyPost(t, r, token, appID, "arti", "read_artifact", map[string]any{"ident": dataSlug})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "hello data") {
		t.Fatalf("response missing content: %s", rr.Body.String())
	}
	if prov.calls != 0 {
		t.Fatalf("OBO provider called %d times; arti reads must not hit OBO", prov.calls)
	}
}

// arti-self reads take the same in-process, viewer-attributed path as "arti"
// — its ResourceURL is deliberately unreachable in the test stack, so a pass
// proves the proxy never dialed it. With auth enabled, the HTTP fallback
// (credential-free 127.0.0.1) would just 401, so this is what makes arti-self
// reads work on real deployments.
func TestProxyArtiSelfReadShortCircuits(t *testing.T) {
	st, appsSvc, prov := newAppsStack(t)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	dataSlug := uniqueSlug("selfdata")
	seedText(t, st, dataSlug, "hello self", "alice@example.com", nil)

	appID := newAppArtifact(t, st, []map[string]string{{"server": "arti-self", "tool": "read_artifact"}})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	rr := proxyPost(t, r, token, appID, "arti-self", "read_artifact", map[string]any{"ident": dataSlug})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "hello self") {
		t.Fatalf("response missing content: %s", rr.Body.String())
	}
	if prov.calls != 0 {
		t.Fatalf("OBO provider called %d times; arti-self reads must not hit OBO", prov.calls)
	}
}

// The in-process read still enforces per-caller access: alice's APP cannot read
// bob's creator-only artifact, so the proxy returns 404 (not the content).
func TestProxyArtiReadEnforcesAccess(t *testing.T) {
	st, appsSvc, _ := newAppsStack(t)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	dataSlug := uniqueSlug("secret")
	seedText(t, st, dataSlug, "top secret", "bob@example.com", []string{}) // creator-only

	appID := newAppArtifact(t, st, []map[string]string{{"server": "arti", "tool": "read_artifact"}})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	rr := proxyPost(t, r, token, appID, "arti", "read_artifact", map[string]any{"ident": dataSlug})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (access enforced); body = %s", rr.Code, rr.Body.String())
	}
}

// Writes are NOT short-circuited: add_artifact takes the OBO path, so it reaches
// the provider (here surfacing the 401 authorize flow) — attribution is unchanged.
func TestProxyArtiWriteUsesOBO(t *testing.T) {
	st, appsSvc, prov := newAppsStack(t)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	appID := newAppArtifact(t, st, []map[string]string{{"server": "arti", "tool": "add_artifact"}})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	rr := proxyPost(t, r, token, appID, "arti", "add_artifact",
		map[string]any{"title": "x", "content_type": "text/plain", "content": "y"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 authorization_required (OBO path); body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "authorization_required") {
		t.Fatalf("write should surface OBO authorize flow; got %s", rr.Body.String())
	}
	if prov.calls == 0 {
		t.Fatalf("write should have reached the OBO provider")
	}
}

// A non-arti server (notion) is never short-circuited — it takes the OBO path.
func TestProxyNonArtiUsesOBO(t *testing.T) {
	st, appsSvc, prov := newAppsStack(t)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	appID := newAppArtifact(t, st, []map[string]string{{"server": "notion", "tool": "search"}})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	rr := proxyPost(t, r, token, appID, "notion", "search", map[string]any{})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (OBO path); body = %s", rr.Code, rr.Body.String())
	}
	if prov.calls == 0 {
		t.Fatalf("notion call should have reached the OBO provider")
	}
}

// The manifest allowlist still governs: a read tool absent from arti-app.json is
// 403, even though it's an in-process-eligible arti read tool.
func TestProxyReadToolNotInManifest(t *testing.T) {
	st, appsSvc, _ := newAppsStack(t)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	// Only read_artifact is allowlisted; search_artifacts is not.
	appID := newAppArtifact(t, st, []map[string]string{{"server": "arti", "tool": "read_artifact"}})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	rr := proxyPost(t, r, token, appID, "arti", "search_artifacts", map[string]any{"q": "x"})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (not in allowlist); body = %s", rr.Code, rr.Body.String())
	}
}
