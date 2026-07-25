package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The WWW-Authenticate `resource_metadata` parameter must be an ABSOLUTE
// http(s) URL once a base URL is configured — RFC 9728 / OAuth 2.1 MCP clients
// (e.g. Runlayer) reject a relative path with "unsafe MCP upstream URL", which
// breaks the whole OAuth discovery → no token → 401 on every call. It points at
// the /mcp-scoped protected-resource metadata (RFC 9728 §3.1), whose `resource`
// matches the canonical /mcp resource a client authenticates to.
func TestProtectedResourceMetadataURL_Absolute(t *testing.T) {
	t.Cleanup(func() { SetMetadataBaseURL("") }) // restore default for other tests

	SetMetadataBaseURL("https://arti.example.com/") // trailing slash trimmed
	const want = "https://arti.example.com/.well-known/oauth-protected-resource/mcp"
	if got := ProtectedResourceMetadataURL(); got != want {
		t.Fatalf("ProtectedResourceMetadataURL() = %q, want %q", got, want)
	}

	rec := httptest.NewRecorder()
	unauth(rec, "missing token")
	wa := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(wa, `resource_metadata="`+want+`"`) {
		t.Fatalf("WWW-Authenticate missing absolute resource_metadata: %q", wa)
	}
	if strings.Contains(wa, `resource_metadata="/.well-known`) {
		t.Fatalf("WWW-Authenticate still advertises a relative URL: %q", wa)
	}
}

// With no base URL configured we keep the (path-scoped) relative path as a safe
// fallback.
func TestProtectedResourceMetadataURL_RelativeFallback(t *testing.T) {
	t.Cleanup(func() { SetMetadataBaseURL("") })

	SetMetadataBaseURL("")
	if got, want := ProtectedResourceMetadataURL(), "/.well-known/oauth-protected-resource/mcp"; got != want {
		t.Fatalf("fallback = %q, want %q", got, want)
	}
}

// arti's MCP endpoint lives at a path (/mcp), so per RFC 9728 §3.1 an MCP client
// constructs the protected-resource metadata URL by inserting the well-known
// segment ahead of that path: /.well-known/oauth-protected-resource/mcp. Before
// this route existed the probe fell through to the FE reverse proxy and 307'd
// into the SSO login chain, which the client (Runlayer) reported as "too many
// redirects". The document must advertise the canonical /mcp resource identifier
// and delegate authorization to arti at the origin.
func TestWellKnown_PathScopedProtectedResource(t *testing.T) {
	const base = "https://arti.example.com"
	h := WellKnownRoutes(MetadataConfig{BaseURL: base})

	var doc struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if rec := getJSON(t, h, "/.well-known/oauth-protected-resource/mcp", &doc); rec.Code != http.StatusOK {
		t.Fatalf("path-scoped PRM status = %d, want 200", rec.Code)
	}
	if want := base + "/mcp"; doc.Resource != want {
		t.Errorf("resource = %q, want %q (the canonical /mcp identifier)", doc.Resource, want)
	}
	if len(doc.AuthorizationServers) != 1 || doc.AuthorizationServers[0] != base {
		t.Errorf("authorization_servers = %v, want [%q] (arti at the origin)", doc.AuthorizationServers, base)
	}

	// The bare-origin document stays available for backwards compatibility and
	// describes the origin resource.
	var root struct {
		Resource string `json:"resource"`
	}
	if rec := getJSON(t, h, "/.well-known/oauth-protected-resource", &root); rec.Code != http.StatusOK {
		t.Fatalf("root PRM status = %d, want 200", rec.Code)
	}
	if root.Resource != base {
		t.Errorf("root resource = %q, want %q", root.Resource, base)
	}
}

// The authorization-server metadata (the endpoint the RFC 9728 chain resolves to
// via authorization_servers) exposes a registration_endpoint — that is what
// Runlayer's "client registration" probe needs to succeed. arti's AS issuer is
// the origin, so this metadata lives at the ROOT well-known path; we deliberately
// do NOT serve a path-scoped /.well-known/oauth-authorization-server/mcp, which
// would imply a second, conflicting issuer identity for the same server.
func TestWellKnown_AuthorizationServerMetadata(t *testing.T) {
	const base = "https://arti.example.com"
	h := WellKnownRoutes(MetadataConfig{BaseURL: base})

	var as struct {
		Issuer               string `json:"issuer"`
		RegistrationEndpoint string `json:"registration_endpoint"`
	}
	if rec := getJSON(t, h, "/.well-known/oauth-authorization-server", &as); rec.Code != http.StatusOK {
		t.Fatalf("AS metadata status = %d, want 200", rec.Code)
	}
	if as.Issuer != base {
		t.Errorf("issuer = %q, want %q (the origin)", as.Issuer, base)
	}
	if as.RegistrationEndpoint != base+"/oauth/register" {
		t.Errorf("registration_endpoint = %q, want %q", as.RegistrationEndpoint, base+"/oauth/register")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server/mcp", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("path-scoped AS metadata status = %d, want 404 (single origin issuer)", rec.Code)
	}
}

func getJSON(t *testing.T, h http.Handler, path string, v any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code == http.StatusOK && v != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	return rec
}
