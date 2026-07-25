package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// MetadataConfig configures the OAuth metadata documents.
type MetadataConfig struct {
	BaseURL   string // e.g. "https://arti.example.com"
	IssuerURL string // upstream OIDC issuer, e.g. "https://dex.example.com"
}

const (
	// relProtectedResourcePath is the well-known path of the OAuth
	// protected-resource metadata document (RFC 9728) for the bare-origin
	// resource.
	relProtectedResourcePath = "/.well-known/oauth-protected-resource"
	// relAuthorizationServerPath is the well-known path of the OAuth
	// authorization-server metadata document (RFC 8414). arti's AS issuer is the
	// origin (no path), so its metadata lives here at the root — the /mcp
	// resource's protected-resource metadata points to it via authorization_servers.
	relAuthorizationServerPath = "/.well-known/oauth-authorization-server"
	// mcpResourcePath is the path component of arti's MCP endpoint (see
	// cmd_serve.go's r.Handle("/mcp", …)). It is the path of the canonical MCP
	// resource identifier (RFC 8707 §2), so per RFC 9728 §3.1 the protected-
	// resource metadata for that resource is served with the path inserted
	// between the well-known segment and the host.
	mcpResourcePath = "/mcp"
	// relProtectedResourceMCPPath is that RFC 9728 §3.1 path-insertion location:
	// /.well-known/oauth-protected-resource/mcp. A spec-compliant MCP client that
	// builds the metadata URL from the canonical /mcp resource identifier looks
	// here (Runlayer's discovery probe does), so it must resolve rather than fall
	// through to the SSO-redirecting FE catch-all.
	relProtectedResourceMCPPath = relProtectedResourcePath + mcpResourcePath
)

var (
	resourceMetadataMu sync.RWMutex
	// The value advertised in the 401 WWW-Authenticate `resource_metadata`
	// parameter. It points at the /mcp-scoped protected-resource metadata (whose
	// `resource` matches the canonical /mcp resource a client authenticates to),
	// per RFC 9728 §3.1. Relative until SetMetadataBaseURL runs.
	resourceMetadataURL = relProtectedResourceMCPPath
)

// SetMetadataBaseURL configures the absolute base URL advertised in the
// WWW-Authenticate `resource_metadata` parameter on 401s. RFC 9728 / OAuth 2.1
// MCP clients (e.g. Runlayer) require an absolute http(s) URL there and reject
// a relative path as an unsafe upstream URL. An empty base keeps the relative
// fallback. Safe to call before any handler is wired.
func SetMetadataBaseURL(baseURL string) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	resourceMetadataMu.Lock()
	defer resourceMetadataMu.Unlock()
	if baseURL == "" {
		resourceMetadataURL = relProtectedResourceMCPPath
		return
	}
	resourceMetadataURL = baseURL + relProtectedResourceMCPPath
}

// ProtectedResourceMetadataURL returns the value to advertise in the
// WWW-Authenticate `resource_metadata` parameter.
func ProtectedResourceMetadataURL() string {
	resourceMetadataMu.RLock()
	defer resourceMetadataMu.RUnlock()
	return resourceMetadataURL
}

// WellKnownRoutes serves the OAuth-protected-resource and
// authorization-server metadata documents that MCP clients (and others)
// use to discover this service.
func WellKnownRoutes(mc MetadataConfig) http.Handler {
	mux := http.NewServeMux()

	// RFC 9728 protected-resource metadata, served at BOTH the bare-origin
	// location and — because arti's MCP endpoint lives at a path (/mcp) — the
	// RFC 9728 §3.1 path-insertion location .../oauth-protected-resource/mcp, so
	// a client that constructs the URL from the canonical /mcp resource
	// identifier resolves it. Each document's `resource` is the exact identifier
	// it describes; both delegate authorization to arti (origin) as the AS.
	mux.HandleFunc("GET "+relProtectedResourcePath,
		func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, protectedResourceDoc(mc.BaseURL, mc.BaseURL))
		})
	mux.HandleFunc("GET "+relProtectedResourceMCPPath,
		func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, protectedResourceDoc(mc.BaseURL+mcpResourcePath, mc.BaseURL))
		})

	// arti delegates upstream authorization to Dex. We surface a
	// minimal authorization-server metadata document that points
	// callers at Dex for `authorization_endpoint` (set via
	// IssuerURL); our own token endpoint still issues the in-process
	// HS256 JWTs used by `arti login --email` test mode.
	mux.HandleFunc("GET "+relAuthorizationServerPath,
		func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"issuer":                                mc.BaseURL,
				"authorization_endpoint":                mc.BaseURL + "/oauth/authorize",
				"token_endpoint":                        mc.BaseURL + "/oauth/token",
				"registration_endpoint":                 mc.BaseURL + "/oauth/register",
				"response_types_supported":              []string{"code"},
				"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
				"code_challenge_methods_supported":      []string{"S256"},
				"token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic", "client_secret_post"},
				"scopes_supported":                      []string{"artifacts:read", "artifacts:write"},
			})
		})

	return mux
}

// protectedResourceDoc builds an RFC 9728 protected-resource metadata document
// for the given resource identifier, delegating authorization to baseURL (arti
// itself, as the origin authorization server).
func protectedResourceDoc(resource, baseURL string) map[string]any {
	return map[string]any{
		"resource":                 resource,
		"authorization_servers":    []string{baseURL},
		"bearer_methods_supported": []string{"header"},
		"resource_documentation":   baseURL + "/openapi.yaml",
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
