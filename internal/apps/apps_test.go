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
	"time"

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
	svc.SetMapEnabled(true)
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
	appsSvc.SetArtiTools(mcp.NewServer(svc, nil))
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

// A write short-circuits in-process too, and lands attributed to the APP.
// Attribution is the reason writes ever went out to OBO: the viewer presents no
// bearer of their own, so without a stamp of its own an app write would read as
// the viewer working in a browser.
func TestProxyArtiWriteShortCircuits(t *testing.T) {
	st, appsSvc, prov := newAppsStack(t)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	appID := newAppArtifact(t, st, []map[string]string{{"server": "arti", "tool": "add_artifact"}})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	slug := uniqueSlug("app-write")
	rr := proxyPost(t, r, token, appID, "arti", "add_artifact", map[string]any{
		"title": "written by an app", "named_slug": slug,
		"content_type": "text/plain", "content": "hello",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (in-process write); body = %s", rr.Code, rr.Body.String())
	}
	if prov.calls != 0 {
		t.Fatalf("write reached the OBO provider %d time(s); it must never leave the cluster", prov.calls)
	}

	row, err := st.GetBySlug(context.Background(), slug, nil)
	if err != nil {
		t.Fatalf("write did not persist: %v", err)
	}
	if row.Creator != "alice@example.com" {
		t.Errorf("creator = %q, want the viewer", row.Creator)
	}
	if row.WrittenVia == nil || *row.WrittenVia != "app:"+appID {
		t.Errorf("written_via = %v, want app:%s", row.WrittenVia, appID)
	}
	if row.WrittenViaName == nil || *row.WrittenViaName != "demo app" {
		t.Errorf("written_via_name = %v, want the app's title", row.WrittenViaName)
	}
}

// A MAP write short-circuits in-process like every other arti write. Routing it
// out to OBO would make a sign-up click wait on an internet round trip, and the
// gateway authorizes nothing MapPut does not check itself.
func TestProxyArtiMapWriteShortCircuits(t *testing.T) {
	st, appsSvc, prov := newAppsStack(t)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	appID := newAppArtifact(t, st, []map[string]string{
		{"server": "arti", "tool": "map_put"},
		{"server": "arti", "tool": "map_get"},
	})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	slug := uniqueSlug("app-map")
	rr := proxyPost(t, r, token, appID, "arti", "map_put", map[string]any{
		"slug": slug, "title": "app map", "allowed_access": []string{"*"}, "allowed_write": []string{"*"},
		"entries": []map[string]any{{"key": "member:1", "value": map[string]any{"name": "Alice"}}},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("map_put status = %d, want 200 (in-process); body = %s", rr.Code, rr.Body.String())
	}
	if prov.calls != 0 {
		t.Fatalf("map_put reached the OBO provider %d time(s); it must never leave the cluster", prov.calls)
	}

	rr = proxyPost(t, r, token, appID, "arti", "map_get", map[string]any{"slug": slug, "key": "member:1"})
	if rr.Code != http.StatusOK {
		t.Fatalf("map_get status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "alice@example.com") {
		t.Errorf("entry does not carry the viewer as updated_by: %s", rr.Body.String())
	}
}

// A write the viewer may not make is refused by the same access layer a direct
// call hits — 404, not 403, so an app cannot use a write to prove a document it
// cannot read exists.
func TestProxyArtiWriteEnforcesAccess(t *testing.T) {
	st, appsSvc, _ := newAppsStack(t)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	dataSlug := uniqueSlug("secret")
	seedText(t, st, dataSlug, "top secret", "bob@example.com", []string{}) // creator-only

	appID := newAppArtifact(t, st, []map[string]string{{"server": "arti", "tool": "append_artifact"}})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	rr := proxyPost(t, r, token, appID, "arti", "append_artifact",
		map[string]any{"slug": dataSlug, "content": "appended"})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (access enforced); body = %s", rr.Code, rr.Body.String())
	}
	row, err := st.GetBySlug(context.Background(), dataSlug, nil)
	if err != nil {
		t.Fatalf("get seeded row: %v", err)
	}
	// Append auto-creates a slug it finds absent, and the access layer hides
	// bob's row rather than refusing it — so the refusal has to be checked as
	// "bob's v1 is still what the slug resolves to", not merely as a 404.
	if row.Version == nil || *row.Version != 1 || row.Creator != "bob@example.com" {
		t.Errorf("slug resolves to v%v by %q, want v1 by bob — the refused append wrote anyway",
			row.Version, row.Creator)
	}
}

// arti access must not be a deployment's to withhold: with NO servers
// configured at all (no ARTI_APP_MCP_SERVERS, no gateway), an app still reads
// and writes arti. Before the in-process branch moved ahead of the server
// lookup, both of these were "unknown server arti".
func TestProxyArtiNeedsNoConfiguredServer(t *testing.T) {
	pool := newPool(t)
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)
	signer := auth.NewJWTSigner([]byte("test-signing-key-at-least-32-bytes!"))
	appsSvc := apps.New(st, signer, map[string]apps.ServerConfig{}, nil, nil, nil)
	appsSvc.SetArtiTools(mcp.NewServer(svc, nil))
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	dataSlug := uniqueSlug("cfgless")
	seedText(t, st, dataSlug, "readable", "alice@example.com", []string{"*"})

	appID := newAppArtifact(t, st, []map[string]string{
		{"server": "arti", "tool": "read_artifact"},
		{"server": "arti", "tool": "add_artifact"},
	})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	rr := proxyPost(t, r, token, appID, "arti", "read_artifact", map[string]any{"ident": dataSlug})
	if rr.Code != http.StatusOK {
		t.Fatalf("read status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	rr = proxyPost(t, r, token, appID, "arti", "add_artifact", map[string]any{
		"title": "x", "named_slug": uniqueSlug("cfgless-write"),
		"content_type": "text/plain", "content": "y",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("write status = %d, want 200; body = %s", rr.Code, rr.Body.String())
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

// A multi-MiB write is carried, not rejected: an APP publishing a data artifact
// through the proxy has the same body ceiling as any other credential, so it
// does not have to shard its payload to fit a proxy-only limit.
func TestProxyCarriesMultiMiBWrite(t *testing.T) {
	st, appsSvc, prov := newAppsStack(t)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)

	appID := newAppArtifact(t, st, []map[string]string{{"server": "arti", "tool": "add_artifact"}})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	slug := uniqueSlug("big-write")
	rr := proxyPost(t, r, token, appID, "arti", "add_artifact", map[string]any{
		"title": "big", "named_slug": slug, "content_type": "text/plain",
		"content": strings.Repeat("a", 4<<20),
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, truncate(rr.Body.String()))
	}
	if prov.calls != 0 {
		t.Fatalf("a 4 MiB write reached the OBO provider %d time(s); it must never leave the cluster", prov.calls)
	}
	row, err := st.GetBySlug(context.Background(), slug, nil)
	if err != nil {
		t.Fatalf("write did not persist: %v", err)
	}
	if row.SizeBytes == nil || *row.SizeBytes != 4<<20 {
		t.Errorf("size = %v, want %d — the body was truncated", row.SizeBytes, 4<<20)
	}
}

func truncate(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// newStubUpstream builds an apps stack whose only remote server is an
// unauthenticated stub MCP server backed by handler.
func newStubUpstream(t *testing.T, handler http.HandlerFunc) (*pgstore.Store, *apps.Service, chi.Router) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "initialize" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			return
		}
		handler(w, r)
	}))
	t.Cleanup(up.Close)
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	signer := auth.NewJWTSigner([]byte("test-signing-key-at-least-32-bytes!"))
	servers := map[string]apps.ServerConfig{"stub": {Name: "stub", Auth: "none", ResourceURL: up.URL}}
	appsSvc := apps.New(st, signer, servers, nil, nil, nil)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)
	return st, appsSvc, r
}

func proxyPostTimeout(t *testing.T, h http.Handler, token, appID string, timeoutMs int) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"app_id": appID, "server": "stub", "tool": "slow", "arguments": map[string]any{}, "timeout_ms": timeoutMs})
	req := httptest.NewRequest(http.MethodPost, apps.ProxyPath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// The app's timeout_ms is the deadline the proxy holds the upstream to: a call
// that asks for 100ms against a 400ms tool gets a structured 504 naming that
// timeout, and the same tool succeeds when the app allows it 2s.
func TestProxyHonorsPerCallTimeout(t *testing.T) {
	st, appsSvc, r := newStubUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(400 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"done"}]}}`))
	})
	appID := newAppArtifact(t, st, []map[string]string{{"server": "stub", "tool": "slow"}})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	rr := proxyPostTimeout(t, r, token, appID, 100)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", rr.Code, rr.Body.String())
	}
	var body map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["error"] != "upstream_timeout" || body["server"] != "stub" || body["tool"] != "slow" || !strings.Contains(body["detail"], "100ms") {
		t.Fatalf("body = %v, want upstream_timeout for stub/slow naming 100ms", body)
	}

	rr = proxyPostTimeout(t, r, token, appID, 2000)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a 2s budget; body = %s", rr.Code, rr.Body.String())
	}
}

// A timeout_ms above the server cap is clamped, not rejected: the cap is what
// the 504 names.
func TestProxyClampsPerCallTimeout(t *testing.T) {
	st, appsSvc, r := newStubUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	appsSvc.SetLimits(150*time.Millisecond, 0)
	appID := newAppArtifact(t, st, []map[string]string{{"server": "stub", "tool": "slow"}})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	start := time.Now()
	rr := proxyPostTimeout(t, r, token, appID, 60000)
	if rr.Code != http.StatusServiceUnavailable || time.Since(start) > 2*time.Second {
		t.Fatalf("status = %d after %s, want 503 at the 150ms cap; body = %s", rr.Code, time.Since(start), rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "150ms") {
		t.Fatalf("detail should name the cap that applied: %s", rr.Body.String())
	}
}

// With the in-flight bound reached, a further upstream call answers 503
// proxy_busy immediately instead of buffering another response. Three calls
// race for one slot: exactly one parks upstream and the other two are turned
// away at once; releasing the upstream lets the holder finish.
func TestProxyBusyWhenInflightFull(t *testing.T) {
	release := make(chan struct{})
	st, appsSvc, r := newStubUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{}}`))
	})
	appsSvc.SetLimits(0, 1)
	appID := newAppArtifact(t, st, []map[string]string{{"server": "stub", "tool": "slow"}})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	results := make(chan *httptest.ResponseRecorder, 3)
	for i := 0; i < 3; i++ {
		go func() { results <- proxyPostTimeout(t, r, token, appID, 5000) }()
	}
	for i := 0; i < 2; i++ {
		select {
		case rr := <-results:
			var body map[string]any
			_ = json.Unmarshal(rr.Body.Bytes(), &body)
			if rr.Code != http.StatusServiceUnavailable || body["error"] != "proxy_busy" || body["retry_after"] != float64(1) || rr.Header().Get("Retry-After") != "1" {
				t.Fatalf("turned-away call: status %d body %v Retry-After %q; want 503 proxy_busy with retry_after in the body and the header", rr.Code, body, rr.Header().Get("Retry-After"))
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the two calls without a slot were not turned away promptly")
		}
	}
	close(release)
	if rr := <-results; rr.Code != http.StatusOK {
		t.Fatalf("slot holder status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
}

// A budget too small for even arti's own in-process tool is reported as the
// tool's timeout, never as a failure of the manifest or access lookups that
// precede it.
func TestProxyTinyBudgetIsTheToolsTimeout(t *testing.T) {
	st, appsSvc, _ := newAppsStack(t)
	r := chi.NewRouter()
	appsSvc.MountProxy(r)
	appID := newAppArtifact(t, st, []map[string]string{{"server": "arti", "tool": "search_artifacts"}})
	token := mustToken(t, appsSvc, "alice@example.com", appID)

	body, _ := json.Marshal(map[string]any{"app_id": appID, "server": "arti", "tool": "search_artifacts", "arguments": map[string]any{"q": "x"}, "timeout_ms": 1})
	req := httptest.NewRequest(http.MethodPost, apps.ProxyPath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if rr.Code != http.StatusServiceUnavailable || out["error"] != "upstream_timeout" || out["tool"] != "search_artifacts" {
		t.Fatalf("status = %d body = %s, want 503 upstream_timeout for arti/search_artifacts", rr.Code, rr.Body.String())
	}
}
