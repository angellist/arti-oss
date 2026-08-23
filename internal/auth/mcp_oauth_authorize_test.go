package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

// fakeMCPStore is a minimal MCPOAuthStore with one pre-registered client.
type fakeMCPStore struct {
	client sqlc.McpOauthClient
	code   *sqlc.InsertMCPCodeParams
}

func (f *fakeMCPStore) InsertMCPClient(context.Context, sqlc.InsertMCPClientParams) error { return nil }
func (f *fakeMCPStore) GetMCPClient(_ context.Context, clientID string) (sqlc.McpOauthClient, error) {
	if clientID == f.client.ClientID {
		return f.client, nil
	}
	return sqlc.McpOauthClient{}, errNotFound
}
func (f *fakeMCPStore) TouchMCPClient(context.Context, string) error { return nil }
func (f *fakeMCPStore) InsertMCPCode(_ context.Context, arg sqlc.InsertMCPCodeParams) error {
	f.code = &arg
	return nil
}
func (f *fakeMCPStore) TakeMCPCode(context.Context, string) (sqlc.McpOauthCode, error) {
	return sqlc.McpOauthCode{}, errNotFound
}

var errNotFound = &notFoundErr{}

type notFoundErr struct{}

func (*notFoundErr) Error() string { return "not found" }

const testRedirect = "https://client.example.com/cb"

func authorizeRequest() *http.Request {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", "arti-mcp-test")
	q.Set("redirect_uri", testRedirect)
	q.Set("code_challenge", "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM")
	q.Set("code_challenge_method", "S256")
	q.Set("state", "xyz")
	return httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
}

func newAuthorizeCfg(t *testing.T, trustProxy bool) (MCPOAuthConfig, *fakeMCPStore, *JWTSigner) {
	t.Helper()
	SetAllowedDomains([]string{"example.com"})
	signer := NewJWTSigner([]byte("test-signing-key-at-least-32-bytes-long!!"))
	store := &fakeMCPStore{client: sqlc.McpOauthClient{
		ClientID:                "arti-mcp-test",
		RedirectUris:            []string{testRedirect},
		TokenEndpointAuthMethod: "none",
	}}
	return MCPOAuthConfig{
		BaseURL: "https://arti.example.com", Store: store, Signer: signer,
		AccessTTL: time.Hour, RefreshTTL: time.Hour, TrustProxyHeader: trustProxy,
	}, store, signer
}

// oidc mode: a forged X-Auth-Request-Email must NOT mint a code — it is
// ignored, and an unauthenticated browser is bounced to login. This is the
// regression guard for the C1-class hole on /oauth/authorize.
func TestAuthorize_OIDC_ForgedHeaderRejected(t *testing.T) {
	cfg, store, _ := newAuthorizeCfg(t, false)
	r := authorizeRequest()
	r.Header.Set(IngressEmailHeader, "attacker@example.com")
	w := httptest.NewRecorder()
	MCPAuthorizeHandler(cfg)(w, r)

	if store.code != nil {
		t.Fatalf("a code was minted from a forged header (email=%q) — hole open", store.code.Email)
	}
	if w.Code != http.StatusFound || w.Header().Get("Location") == "" {
		t.Fatalf("want 302 redirect to login, got %d (loc=%q)", w.Code, w.Header().Get("Location"))
	}
	if got := w.Header().Get("Location"); !contains(got, "/auth/login") {
		t.Fatalf("redirect should target login, got %q", got)
	}
}

// oidc mode: a valid arti_session cookie mints a code bound to the cookie's
// email — the per-user OAuth path this enables.
func TestAuthorize_OIDC_ValidCookieMintsCode(t *testing.T) {
	cfg, store, signer := newAuthorizeCfg(t, false)
	tok, err := signer.Sign(Claims{Email: "user@example.com", Scopes: []string{"user"}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	r := authorizeRequest()
	r.AddCookie(&http.Cookie{Name: CookieName, Value: tok})
	r.Header.Set(IngressEmailHeader, "attacker@example.com") // must be ignored
	w := httptest.NewRecorder()
	MCPAuthorizeHandler(cfg)(w, r)

	if w.Code != http.StatusFound {
		t.Fatalf("want 302 to client redirect_uri, got %d", w.Code)
	}
	if store.code == nil || store.code.Email != "user@example.com" {
		t.Fatalf("code not minted for cookie identity: %+v", store.code)
	}
}

// proxy mode: the ingress header is trusted (oauth2-proxy overwrites it).
func TestAuthorize_Proxy_TrustsHeader(t *testing.T) {
	cfg, store, _ := newAuthorizeCfg(t, true)
	r := authorizeRequest()
	r.Header.Set(IngressEmailHeader, "user@example.com")
	w := httptest.NewRecorder()
	MCPAuthorizeHandler(cfg)(w, r)

	if store.code == nil || store.code.Email != "user@example.com" {
		t.Fatalf("proxy mode should mint code from header: %+v", store.code)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
