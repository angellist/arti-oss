//go:build integration

package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// Every test that touches app_mcp_servers lives in this package so none of
// them runs concurrently with the seed test, which needs the table empty.

var testPolicy = ConnectorPolicy{Reserved: []string{"arti-self", "llm"}, Hosts: []string{"mcp.example.com"}}

func connectorStack(t *testing.T) (*pgxpool.Pool, *pgstore.Store, http.Handler, string) {
	t.Helper()
	pool := newPool(t)
	store := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	ctx := context.Background()
	admin := "connector-admin-" + uuid.NewString()[:8] + "@example.com"
	if err := store.AssignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.UnassignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin) })
	svc := NewService(pool, store)
	svc.SetConnectorPolicy(testPolicy)
	return pool, store, newRouter(svc), admin
}

func call(t *testing.T, h http.Handler, method, path, email string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if email != "" {
		req.Header.Set("X-Test-Email", email)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func uniqueConnector(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	name := "conn-test-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM app_mcp_servers WHERE name = $1`, name)
	})
	return name
}

func TestConnectorSeedRunsOnlyOnEmptyTable(t *testing.T) {
	pool := newPool(t)
	store := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM app_mcp_servers`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM app_mcp_servers`) })

	first := []pgstore.AppMCPServer{
		{Name: "seed-a", ResourceURL: "https://mcp.example.com/a", Auth: "oauth", Scope: "s"},
		{Name: "seed-b", ResourceURL: "https://mcp.example.com/b", Auth: "none"},
	}
	if n, err := store.SeedAppMCPServers(ctx, first); err != nil || n != 2 {
		t.Fatalf("first seed = %d, %v; want 2", n, err)
	}
	// A later boot with a different env value must not add, change or
	// restore anything: once seeded, the table is the only source.
	if _, err := pool.Exec(ctx, `UPDATE app_mcp_servers SET enabled = FALSE WHERE name = 'seed-a'`); err != nil {
		t.Fatal(err)
	}
	second := append(first, pgstore.AppMCPServer{Name: "seed-c", ResourceURL: "https://mcp.example.com/c", Auth: "none"})
	if n, err := store.SeedAppMCPServers(ctx, second); err != nil || n != 0 {
		t.Fatalf("second seed = %d, %v; want 0", n, err)
	}
	list, err := store.ListAppMCPServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Enabled || list[0].CreatedBy != "bootstrap" {
		t.Fatalf("after reseed: %+v; want the 2 original rows, seed-a still disabled", list)
	}
}

func TestConnectorEndpointsHiddenFromNonAdmins(t *testing.T) {
	pool, _, h, _ := connectorStack(t)
	name := uniqueConnector(t, pool)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/mcp-servers"},
		{http.MethodPost, "/api/admin/mcp-servers"},
		{http.MethodPatch, "/api/admin/mcp-servers/" + name},
		{http.MethodDelete, "/api/admin/mcp-servers/" + name},
		{http.MethodGet, "/api/admin/mcp-servers/" + name + "/apps"},
	} {
		if rec := call(t, h, c.method, c.path, "nobody@example.com", map[string]any{"name": name}); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s as non-admin: %d, want 404", c.method, c.path, rec.Code)
		}
	}
}

func TestConnectorCreateRejectsInvalid(t *testing.T) {
	pool, _, h, admin := connectorStack(t)
	name := uniqueConnector(t, pool)
	ok := map[string]any{"name": name, "resource_url": "https://mcp.example.com/x", "auth": "oauth", "scope": "s"}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for kk, vv := range ok {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	for label, body := range map[string]map[string]any{
		"bad name":       with("name", "Bad_Name"),
		"built-in name":  with("name", "llm"),
		"non-https":      with("resource_url", "http://mcp.example.com/x"),
		"off-allowlist":  with("resource_url", "https://evil.example.net/x"),
		"service auth":   with("auth", "service"),
		"oauth no scope": with("scope", ""),
	} {
		if rec := call(t, h, http.MethodPost, "/api/admin/mcp-servers", admin, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", label, rec.Code, rec.Body.String())
		}
	}
	if rec := call(t, h, http.MethodPost, "/api/admin/mcp-servers", admin, ok); rec.Code != http.StatusCreated {
		t.Fatalf("valid create: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, h, http.MethodPost, "/api/admin/mcp-servers", admin, ok); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create: %d, want 409", rec.Code)
	}
}

// Disable is not revoke: re-enabling must not send every viewer back through
// consent. Delete is revoke, unless another connector still uses the URL.
func TestConnectorDisableKeepsTokensDeleteRevokes(t *testing.T) {
	pool, store, h, admin := connectorStack(t)
	ctx := context.Background()
	name, twin := uniqueConnector(t, pool), uniqueConnector(t, pool)
	resource := "https://mcp.example.com/" + name
	if _, err := pool.Exec(ctx, `
		INSERT INTO oauth_obo_tokens (email, resource, access_token_enc, expiry)
		VALUES ('viewer@example.com', $1, '\x00', now() + interval '1 hour')`, resource); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM oauth_obo_tokens WHERE resource = $1`, resource) })
	tokens := func() int {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM oauth_obo_tokens WHERE resource = $1`, resource).Scan(&n)
		return n
	}
	for _, n := range []string{name, twin} {
		body := map[string]any{"name": n, "resource_url": resource, "auth": "oauth", "scope": "s"}
		if rec := call(t, h, http.MethodPost, "/api/admin/mcp-servers", admin, body); rec.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", n, rec.Code, rec.Body.String())
		}
	}

	if rec := call(t, h, http.MethodPatch, "/api/admin/mcp-servers/"+name, admin, map[string]any{"enabled": false}); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	got, found, err := store.LookupAppMCPServer(ctx, name)
	if err != nil || !found || got.Enabled || got.UpdatedBy != admin {
		t.Fatalf("after disable: %+v found=%v err=%v; want disabled row updated by %s", got, found, err, admin)
	}
	if tokens() != 1 {
		t.Fatalf("disable revoked the viewer's token")
	}

	if rec := call(t, h, http.MethodDelete, "/api/admin/mcp-servers/"+name, admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if tokens() != 1 {
		t.Fatalf("delete revoked a token another connector still uses")
	}
	if rec := call(t, h, http.MethodDelete, "/api/admin/mcp-servers/"+twin, admin, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete twin: %d %s", rec.Code, rec.Body.String())
	}
	if tokens() != 0 {
		t.Fatalf("deleting the last connector for a URL left its tokens")
	}
	if _, found, _ := store.LookupAppMCPServer(ctx, name); found {
		t.Fatalf("deleted connector still resolves")
	}
}

func TestConnectorAppsListsDeclaringApps(t *testing.T) {
	pool, store, h, admin := connectorStack(t)
	name := uniqueConnector(t, pool)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("arti-app.json")
	_, _ = w.Write([]byte(`{"name":"x","entry":"index.html","tools":[{"server":"` + name + `","tool":"t"}]}`))
	w, _ = zw.Create("index.html")
	_, _ = w.Write([]byte("<html></html>"))
	_ = zw.Close()
	slug := "conn-apps-" + uuid.NewString()[:8]
	if _, err := store.Put(context.Background(), pgstore.PutInput{
		ArtifactType: pgstore.TypeApp, NamedSlug: &slug, Title: "declares it",
		ContentType: "application/zip", Content: buf.Bytes(), Creator: "author@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	rec := call(t, h, http.MethodGet, "/api/admin/mcp-servers/"+name+"/apps", admin, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), slug) {
		t.Fatalf("apps scan: %d %s; want it to list %s", rec.Code, rec.Body.String(), slug)
	}
}

// Two admins patching different fields at once must both land: each PATCH
// merges into the row it locked, never into a copy read earlier.
func TestConnectorConcurrentPatchesBothLand(t *testing.T) {
	pool, store, h, admin := connectorStack(t)
	name := uniqueConnector(t, pool)
	body := map[string]any{"name": name, "resource_url": "https://mcp.example.com/" + name, "auth": "none"}
	if rec := call(t, h, http.MethodPost, "/api/admin/mcp-servers", admin, body); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	for i := 0; i < 10; i++ {
		var wg sync.WaitGroup
		for _, patch := range []map[string]any{{"enabled": i%2 == 1}, {"notes": fmt.Sprint(i)}} {
			wg.Add(1)
			go func(p map[string]any) {
				defer wg.Done()
				_ = call(t, h, http.MethodPatch, "/api/admin/mcp-servers/"+name, admin, p)
			}(patch)
		}
		wg.Wait()
		got, _, err := store.LookupAppMCPServer(context.Background(), name)
		if err != nil || got.Enabled != (i%2 == 1) || got.Notes != fmt.Sprint(i) {
			t.Fatalf("round %d: %+v, %v; want enabled=%v notes=%d", i, got, err, i%2 == 1, i)
		}
	}
}
