package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

// MCPOAuthStore is the database surface the MCP OAuth handlers use.
// Implemented by *pgstore.Store.
type MCPOAuthStore interface {
	InsertMCPClient(ctx context.Context, arg sqlc.InsertMCPClientParams) error
	GetMCPClient(ctx context.Context, clientID string) (sqlc.McpOauthClient, error)
	TouchMCPClient(ctx context.Context, clientID string) error
	InsertMCPCode(ctx context.Context, arg sqlc.InsertMCPCodeParams) error
	TakeMCPCode(ctx context.Context, code string) (sqlc.McpOauthCode, error)
}

// MCPOAuthConfig wires the handlers.
type MCPOAuthConfig struct {
	BaseURL    string // e.g. https://arti.example.com
	Store      MCPOAuthStore
	Signer     *JWTSigner
	AccessTTL  time.Duration
	RefreshTTL time.Duration

	// TrustProxyHeader controls how MCPAuthorizeHandler learns the end-user
	// identity. When true (proxy mode) the request is behind oauth2-proxy,
	// which overwrites X-Auth-Request-Email, so that header is trustworthy.
	// When false (oidc mode, or anything on the public ingress) the header
	// is client-forgeable — see the C1 fix in middleware.go — so identity is
	// taken from the arti-signed session cookie instead, exactly as every
	// other route does. Fail closed: default false trusts no header.
	TrustProxyHeader bool
}

// MCPRegisterHandler implements RFC 7591 Dynamic Client Registration.
// Anonymous: this endpoint MUST be reachable without auth so that a
// new MCP gateway can announce itself before any user has logged in.
//
// Validates redirect_uris (https:// or loopback only), persists a row
// in mcp_oauth_clients, and returns the canonical RFC 7591 response.
func MCPRegisterHandler(cfg MCPOAuthConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ClientName              string   `json:"client_name"`
			RedirectURIs            []string `json:"redirect_uris"`
			GrantTypes              []string `json:"grant_types"`
			ResponseTypes           []string `json:"response_types"`
			TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
			Scope                   string   `json:"scope"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeOAuthErr(w, http.StatusBadRequest, "invalid_client_metadata", "request body is not valid JSON")
			return
		}
		if len(req.RedirectURIs) == 0 {
			writeOAuthErr(w, http.StatusBadRequest, "invalid_redirect_uri", "at least one redirect_uri is required")
			return
		}
		for _, u := range req.RedirectURIs {
			if !validRedirectURI(u) {
				writeOAuthErr(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uri must be https:// or http://localhost (RFC 8252)")
				return
			}
		}
		authMethod := req.TokenEndpointAuthMethod
		if authMethod == "" {
			authMethod = "none"
		}
		if authMethod != "none" && authMethod != "client_secret_basic" && authMethod != "client_secret_post" {
			writeOAuthErr(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported token_endpoint_auth_method")
			return
		}
		grants := req.GrantTypes
		if len(grants) == 0 {
			grants = []string{"authorization_code", "refresh_token"}
		}

		clientID := "arti-mcp-" + randomToken(16)
		var secretHash *string
		var clientSecret string
		if authMethod != "none" {
			clientSecret = randomToken(32)
			h := sha256.Sum256([]byte(clientSecret))
			s := base64.RawURLEncoding.EncodeToString(h[:])
			secretHash = &s
		}

		id := uuid.New()
		err := cfg.Store.InsertMCPClient(r.Context(), sqlc.InsertMCPClientParams{
			ID:                      pgUUID(id),
			ClientID:                clientID,
			ClientSecretHash:        secretHash,
			ClientName:              strPtr(req.ClientName),
			RedirectUris:            req.RedirectURIs,
			GrantTypes:              grants,
			Scope:                   strPtr(req.Scope),
			TokenEndpointAuthMethod: authMethod,
		})
		if err != nil {
			http.Error(w, "internal: "+err.Error(), http.StatusInternalServerError)
			return
		}

		resp := map[string]any{
			"client_id":                  clientID,
			"client_id_issued_at":        time.Now().Unix(),
			"redirect_uris":              req.RedirectURIs,
			"grant_types":                grants,
			"response_types":             []string{"code"},
			"token_endpoint_auth_method": authMethod,
		}
		if req.ClientName != "" {
			resp["client_name"] = req.ClientName
		}
		if req.Scope != "" {
			resp["scope"] = req.Scope
		}
		if clientSecret != "" {
			resp["client_secret"] = clientSecret
			resp["client_secret_expires_at"] = 0 // never expires
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// MCPAuthorizeHandler implements the authorization-code grant
// initiation. The browser arrives here after the MCP client builds an
// authorization request. arti-server expects oauth2-proxy headers to
// already identify the user (i.e. this endpoint must be on
// ProtectedIngress).
//
// Required query params: client_id, redirect_uri, response_type=code,
// code_challenge, code_challenge_method=S256, state (recommended).
// Optional: scope.
//
// Generates a single-use authorization code (10-minute TTL), persists
// it bound to (client_id, redirect_uri, PKCE challenge, email), and
// redirects to redirect_uri with ?code=...&state=....
func MCPAuthorizeHandler(cfg MCPOAuthConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		clientID := q.Get("client_id")
		redirectURI := q.Get("redirect_uri")
		responseType := q.Get("response_type")
		codeChallenge := q.Get("code_challenge")
		codeChallengeMethod := q.Get("code_challenge_method")
		state := q.Get("state")
		scope := q.Get("scope")

		if responseType != "code" {
			writeOAuthErr(w, http.StatusBadRequest, "unsupported_response_type", "only response_type=code is supported")
			return
		}
		if clientID == "" || redirectURI == "" || codeChallenge == "" {
			writeOAuthErr(w, http.StatusBadRequest, "invalid_request", "client_id, redirect_uri, and code_challenge are required")
			return
		}
		if codeChallengeMethod != "" && codeChallengeMethod != "S256" {
			writeOAuthErr(w, http.StatusBadRequest, "invalid_request", "code_challenge_method must be S256")
			return
		}
		codeChallengeMethod = "S256"

		// Look up client and validate redirect_uri.
		c, err := cfg.Store.GetMCPClient(r.Context(), clientID)
		if err != nil {
			writeOAuthErr(w, http.StatusUnauthorized, "invalid_client", "unknown client_id")
			return
		}
		if !sliceContains(c.RedirectUris, redirectURI) {
			writeOAuthErr(w, http.StatusBadRequest, "invalid_request", "redirect_uri is not registered for this client")
			return
		}

		// Resolve the end-user identity. The client and redirect_uri were
		// already validated above, so a login redirect below can't be driven
		// by an unregistered client.
		var email string
		if cfg.TrustProxyHeader {
			// Proxy mode: oauth2-proxy overwrites this header, so trust it.
			email = strings.TrimSpace(r.Header.Get(IngressEmailHeader))
			if email == "" {
				writeOAuthErr(w, http.StatusUnauthorized, "access_denied", "no upstream identity — is oauth2-proxy in front?")
				return
			}
		} else {
			// oidc / public-ingress mode: X-Auth-Request-Email is forgeable
			// here (no proxy strips it), so it is NOT consulted. Identity comes
			// from the arti-signed session cookie; when it is absent or invalid
			// we bounce the browser through login and return to this exact
			// authorize request (cookie set) to mint the code. App/embed-scoped
			// tokens are page credentials, never a session — reject them too.
			ck, cerr := r.Cookie(CookieName)
			if cerr == nil && ck.Value != "" && cfg.Signer != nil {
				if c, verr := cfg.Signer.Verify(ck.Value); verr == nil && !c.IsEmbedScoped() && !c.IsAppScoped() {
					email = strings.TrimSpace(c.Email)
				}
			}
			if email == "" {
				http.Redirect(w, r, "/auth/login?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
		}
		if !IsAllowed(email) {
			writeOAuthErr(w, http.StatusForbidden, "access_denied", "email domain not allowed")
			return
		}

		// Mint the code.
		code := randomToken(32)
		var scopePtr *string
		if scope != "" {
			scopePtr = &scope
		}
		err = cfg.Store.InsertMCPCode(r.Context(), sqlc.InsertMCPCodeParams{
			Code:                code,
			ClientID:            clientID,
			RedirectUri:         redirectURI,
			CodeChallenge:       codeChallenge,
			CodeChallengeMethod: codeChallengeMethod,
			Email:               email,
			Scope:               scopePtr,
			ExpiresAt:           pgTimestamp(time.Now().Add(10 * time.Minute)),
		})
		if err != nil {
			writeOAuthErr(w, http.StatusInternalServerError, "server_error", err.Error())
			return
		}

		// Redirect to client.
		u, err := url.Parse(redirectURI)
		if err != nil {
			writeOAuthErr(w, http.StatusBadRequest, "invalid_request", "invalid redirect_uri")
			return
		}
		qq := u.Query()
		qq.Set("code", code)
		if state != "" {
			qq.Set("state", state)
		}
		u.RawQuery = qq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
}

// MCPTokenHandler implements the authorization-code token exchange and
// refresh-token grant. Accepts application/x-www-form-urlencoded per
// RFC 6749. Verifies PKCE for public clients, then mints arti's
// standard HS256 access + refresh tokens.
func MCPTokenHandler(cfg MCPOAuthConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			writeOAuthErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		grantType := r.PostForm.Get("grant_type")
		switch grantType {
		case "authorization_code":
			mcpTokenAuthCode(w, r, cfg)
		case "refresh_token":
			mcpTokenRefresh(w, r, cfg)
		default:
			writeOAuthErr(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
		}
	}
}

func mcpTokenAuthCode(w http.ResponseWriter, r *http.Request, cfg MCPOAuthConfig) {
	code := r.PostForm.Get("code")
	redirectURI := r.PostForm.Get("redirect_uri")
	codeVerifier := r.PostForm.Get("code_verifier")
	clientID := mcpClientIDFromRequest(r)
	if code == "" || redirectURI == "" || clientID == "" {
		writeOAuthErr(w, http.StatusBadRequest, "invalid_request", "code, redirect_uri, and client_id are required")
		return
	}
	row, err := cfg.Store.TakeMCPCode(r.Context(), code)
	if err != nil {
		writeOAuthErr(w, http.StatusBadRequest, "invalid_grant", "code is invalid, used, or expired")
		return
	}
	if row.ClientID != clientID || row.RedirectUri != redirectURI {
		writeOAuthErr(w, http.StatusBadRequest, "invalid_grant", "code does not match client_id / redirect_uri")
		return
	}
	if !verifyPKCE(codeVerifier, row.CodeChallenge) {
		writeOAuthErr(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	// Verify client auth (only for confidential clients).
	c, err := cfg.Store.GetMCPClient(r.Context(), clientID)
	if err != nil {
		writeOAuthErr(w, http.StatusUnauthorized, "invalid_client", "unknown client")
		return
	}
	if c.TokenEndpointAuthMethod != "none" {
		secret := mcpClientSecretFromRequest(r)
		if !checkSecret(secret, c.ClientSecretHash) {
			writeOAuthErr(w, http.StatusUnauthorized, "invalid_client", "invalid client_secret")
			return
		}
	}
	_ = cfg.Store.TouchMCPClient(r.Context(), clientID)

	scopes := []string{"user"}
	mcpIssue(w, cfg, row.Email, scopes)
}

func mcpTokenRefresh(w http.ResponseWriter, r *http.Request, cfg MCPOAuthConfig) {
	refresh := r.PostForm.Get("refresh_token")
	if refresh == "" {
		writeOAuthErr(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	claims, err := cfg.Signer.Verify(refresh)
	if err != nil {
		writeOAuthErr(w, http.StatusBadRequest, "invalid_grant", "invalid refresh_token")
		return
	}
	if !sliceContains(claims.Scopes, "refresh") {
		writeOAuthErr(w, http.StatusBadRequest, "invalid_grant", "token is not a refresh token")
		return
	}
	// Flow-binding (H7): only MCP-issued (or legacy untyped) refresh tokens are
	// valid here. A device-flow refresh token (Fam set / upload scope) or a CLI
	// refresh token must use its own endpoint; accepting them here would launder
	// a narrower token into a full `user` session.
	if claims.Fam != "" || claims.IsUploadScoped() || claims.Typ == TokenTypeCLI {
		writeOAuthErr(w, http.StatusBadRequest, "invalid_grant", "refresh token not valid for this endpoint")
		return
	}
	if !IsAllowed(claims.Email) {
		writeOAuthErr(w, http.StatusForbidden, "access_denied", "email domain not allowed")
		return
	}
	mcpIssue(w, cfg, claims.Email, []string{"user"})
}

func mcpIssue(w http.ResponseWriter, cfg MCPOAuthConfig, email string, scopes []string) {
	at, err := cfg.Signer.Sign(Claims{Email: email, Scopes: scopes, TTL: cfg.AccessTTL})
	if err != nil {
		writeOAuthErr(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	// Typ binds the refresh token to the MCP flow (H7); device tokens (Fam) and
	// CLI tokens are rejected at /oauth/token's refresh grant.
	rt, _ := cfg.Signer.Sign(Claims{Email: email, Scopes: []string{"refresh"}, Typ: TokenTypeMCP, TTL: cfg.RefreshTTL})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  at,
		"refresh_token": rt,
		"token_type":    "Bearer",
		"expires_in":    int(cfg.AccessTTL.Seconds()),
		"scope":         strings.Join(scopes, " "),
	})
}

// ─── helpers ────────────────────────────────────────────────────────

// validRedirectURI returns true for https:// URIs (any host) or
// loopback http:// per RFC 8252 §7.3 (used by native + CLI clients).
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		host := u.Hostname()
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	}
	return false
}

func randomToken(nbytes int) string {
	b := make([]byte, nbytes)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func verifyPKCE(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}
	h := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(h[:])
	return expected == challenge
}

func mcpClientIDFromRequest(r *http.Request) string {
	if id := r.PostForm.Get("client_id"); id != "" {
		return id
	}
	if u, _, ok := r.BasicAuth(); ok {
		return u
	}
	return ""
}

func mcpClientSecretFromRequest(r *http.Request) string {
	if s := r.PostForm.Get("client_secret"); s != "" {
		return s
	}
	if _, p, ok := r.BasicAuth(); ok {
		return p
	}
	return ""
}

func checkSecret(plain string, hashed *string) bool {
	if hashed == nil || *hashed == "" {
		return false
	}
	h := sha256.Sum256([]byte(plain))
	got := base64.RawURLEncoding.EncodeToString(h[:])
	return got == *hashed
}

func sliceContains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func pgUUID(u uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: u, Valid: true}
}

func pgTimestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func writeOAuthErr(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": desc,
	})
}

// Avoid unused-import errors when this file is the only consumer of
// `errors`/`fmt`/`pgx`.
var _ = errors.New
var _ = fmt.Errorf
var _ = pgx.ErrNoRows
