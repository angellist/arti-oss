package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// H7: a refresh token is bound to the flow that issued it. The /auth/cli/refresh
// endpoint must reject a device-flow refresh token (identified by its family id
// Fam, or the upload scope) — otherwise an upload-scoped device token could be
// laundered into a full `user` session — and an MCP refresh token. Legacy
// untyped CLI refresh tokens still work (transitional).
func TestCLIRefresh_FlowBinding(t *testing.T) {
	signer := NewJWTSigner([]byte("k"))
	h := CLIRefreshHandler(signer, time.Hour, 24*time.Hour)
	call := func(c Claims) (int, string) {
		tok, _ := signer.Sign(c)
		b, _ := json.Marshal(map[string]string{"refresh_token": tok})
		r := httptest.NewRequest(http.MethodPost, "/auth/cli/refresh", bytes.NewReader(b))
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code, w.Body.String()
	}
	const em = "a@example.com"

	if got, _ := call(Claims{Email: em, Scopes: []string{"refresh"}, Fam: "fam1"}); got != http.StatusUnauthorized {
		t.Errorf("device refresh (Fam) @ cli: want 401, got %d", got)
	}
	if got, _ := call(Claims{Email: em, Scopes: []string{"refresh", "upload"}, Fam: "fam1"}); got != http.StatusUnauthorized {
		t.Errorf("upload refresh @ cli: want 401, got %d", got)
	}
	if got, _ := call(Claims{Email: em, Scopes: []string{"refresh"}, Typ: TokenTypeMCP}); got != http.StatusUnauthorized {
		t.Errorf("mcp refresh @ cli: want 401, got %d", got)
	}
	if got, _ := call(Claims{Email: em, Scopes: []string{"refresh"}}); got != http.StatusOK {
		t.Errorf("legacy untyped cli refresh: want 200, got %d", got)
	}

	st, body := call(Claims{Email: em, Scopes: []string{"refresh"}, Typ: TokenTypeCLI})
	if st != http.StatusOK {
		t.Fatalf("cli refresh: want 200, got %d", st)
	}
	var resp struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.Unmarshal([]byte(body), &resp)
	nc, err := signer.Verify(resp.RefreshToken)
	if err != nil || nc.Typ != TokenTypeCLI {
		t.Errorf("issued cli refresh Typ = %q (err %v), want %q", nc.Typ, err, TokenTypeCLI)
	}
}

// H7: /oauth/token's refresh grant rejects device-flow (Fam) and CLI refresh
// tokens; legacy untyped and MCP-typed tokens work, and a freshly issued MCP
// refresh token carries Typ=mcp.
func TestMCPRefresh_FlowBinding(t *testing.T) {
	signer := NewJWTSigner([]byte("k"))
	cfg := MCPOAuthConfig{Signer: signer, AccessTTL: time.Hour, RefreshTTL: 24 * time.Hour}
	call := func(c Claims) (int, string) {
		tok, _ := signer.Sign(c)
		form := url.Values{"refresh_token": {tok}}
		r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		_ = r.ParseForm()
		w := httptest.NewRecorder()
		mcpTokenRefresh(w, r, cfg)
		return w.Code, w.Body.String()
	}
	const em = "a@example.com"

	if got, _ := call(Claims{Email: em, Scopes: []string{"refresh"}, Fam: "fam1"}); got != http.StatusBadRequest {
		t.Errorf("device refresh (Fam) @ mcp: want 400, got %d", got)
	}
	if got, _ := call(Claims{Email: em, Scopes: []string{"refresh"}, Typ: TokenTypeCLI}); got != http.StatusBadRequest {
		t.Errorf("cli refresh @ mcp: want 400, got %d", got)
	}
	if got, _ := call(Claims{Email: em, Scopes: []string{"refresh"}, Typ: TokenTypeMCP}); got != http.StatusOK {
		t.Errorf("mcp refresh @ mcp: want 200, got %d", got)
	}
	if got, _ := call(Claims{Email: em, Scopes: []string{"refresh"}}); got != http.StatusOK {
		t.Errorf("legacy untyped mcp refresh: want 200, got %d", got)
	}

	_, body := call(Claims{Email: em, Scopes: []string{"refresh"}, Typ: TokenTypeMCP})
	var resp struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.Unmarshal([]byte(body), &resp)
	nc, _ := signer.Verify(resp.RefreshToken)
	if nc.Typ != TokenTypeMCP {
		t.Errorf("issued mcp refresh Typ = %q, want %q", nc.Typ, TokenTypeMCP)
	}
}
