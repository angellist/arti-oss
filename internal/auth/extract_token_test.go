package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A browser hitting an oauth2-proxy'd route (e.g. /app) carries BOTH arti's own
// arti_session cookie AND oauth2-proxy's forwarded X-Auth-Request-Access-Token.
// The cookie is arti-signed and minted only after the required-groups gate at
// login (IngressLoginHandler, via X-Auth-Request-Groups); the proxy access token
// has no `groups` claim, so verifying IT fails RequireAuth's OIDC group check and
// 403s every app for every user. extractToken must prefer the cookie over the
// proxy header. Regression guard for the C1 fix (#96).
func TestExtractTokenPrefersSessionCookieOverProxyToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/app/demo", nil)
	req.Header.Set(ProxyTokenHeader, "proxy-access-token")
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "session-cookie"})
	got, source := extractToken(req)
	if got != "session-cookie" || source != CredSourceCookie {
		t.Fatalf("extractToken = %q/%q, want the arti_session cookie", got, source)
	}
}

// An explicit Authorization: Bearer still wins (API / CLI / MCP callers).
func TestExtractTokenPrefersBearerOverCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("Authorization", "Bearer explicit-bearer")
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "session-cookie"})
	got, source := extractToken(req)
	if got != "explicit-bearer" || source != CredSourceBearer {
		t.Fatalf("extractToken = %q/%q, want the bearer token", got, source)
	}
}

// The proxy access token remains a fallback when nothing else is present.
func TestExtractTokenFallsBackToProxyToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/app/demo", nil)
	req.Header.Set(ProxyTokenHeader, "proxy-access-token")
	got, source := extractToken(req)
	if got != "proxy-access-token" || source != CredSourceProxy {
		t.Fatalf("extractToken = %q/%q, want the proxy token fallback", got, source)
	}
}
