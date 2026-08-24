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
	// RequiredGroups is AUTH_REQUIRED_GROUPS, matched against the groups
	// the front door reports for the caller — the same gate the two
	// interactive login handlers apply.
	RequiredGroups []string
	// CookieSecure marks the consent identity cookie Secure (off only for
	// plain-HTTP local development).
	CookieSecure bool
	// TrustProxyHeaders says an authenticating proxy terminates every
	// request and overwrites the X-Auth-Request-* headers, so this
	// deployment may take the end user's identity from them. It is set
	// from ARTI_AUTH_MODE — true for "proxy" and the legacy "" default,
	// false for "oidc" and "disabled".
	//
	// It is a statement about the deployment, not about the request: a
	// header is a bare assertion by whoever sent it, and arti reachable
	// without a proxy in front means anybody can send one. Trusting a
	// header because it happens to be present is how an attacker becomes
	// an admin. Where this is false the headers are ignored entirely and
	// identity comes from the arti_session cookie alone.
	TrustProxyHeaders bool
}

// mcpCaller is the end user behind an authorization request, and where
// their identity came from.
type mcpCaller struct {
	Email string
	// FromProxy records that the identity came from the proxy headers, so
	// the required-groups gate can read X-Auth-Request-Groups. The cookie
	// path leaves it false and skips that gate on purpose: both interactive
	// login front doors (IngressLoginHandler, OIDCLogin) apply
	// AUTH_REQUIRED_GROUPS before they mint arti_session, so a cookie that
	// verifies has already passed it, and there are no groups on the
	// request to re-check. extractToken documents the same reasoning.
	FromProxy bool
}

// identifyMCPCaller resolves who is making an authorization request, from
// exactly one of two sources and with no fallback between them that could
// launder an untrusted value into a trusted one:
//
//   - the X-Auth-Request-Email header, but only where TrustProxyHeaders
//     says a proxy overwrites it at the edge. Preferred there because it
//     reflects the live front-door session and carries current groups,
//     which is what "proxy" mode declares to be authoritative.
//   - arti's own arti_session cookie, HS256-verified here. Available in
//     every mode, and the only source when no proxy is declared.
//
// Reports ok=false when nothing identifies the caller. The GET turns that
// into a login redirect and the POST into a 401.
func (cfg MCPOAuthConfig) identifyMCPCaller(r *http.Request) (mcpCaller, bool) {
	if cfg.TrustProxyHeaders {
		if email := strings.TrimSpace(r.Header.Get(IngressEmailHeader)); email != "" {
			return mcpCaller{Email: email, FromProxy: true}, true
		}
	}
	if cfg.Signer != nil {
		if ck, err := r.Cookie(CookieName); err == nil {
			// IsSessionCredential, not merely a good signature: every
			// narrower token is signed by the same key, and nothing stops a
			// caller sending one in this cookie by hand.
			if claims, verr := cfg.Signer.Verify(ck.Value); verr == nil && claims.Email != "" &&
				claims.IsSessionCredential() {
				return mcpCaller{Email: claims.Email}, true
			}
		}
	}
	return mcpCaller{}, false
}

// allowCaller applies the access gates that do not depend on the client:
// the email allowlist, and the required-groups gate on the proxy path.
// Shared so the consent GET and the approving POST cannot drift apart.
func (cfg MCPOAuthConfig) allowCaller(r *http.Request, caller mcpCaller) *oauthErr {
	if !IsAllowed(caller.Email) {
		return &oauthErr{http.StatusForbidden, "access_denied", "email domain not allowed"}
	}
	if caller.FromProxy && len(cfg.RequiredGroups) > 0 {
		if !anyMatch(splitGroups(r.Header.Get(IngressGroupsHeader)), cfg.RequiredGroups) {
			return &oauthErr{http.StatusForbidden, "access_denied", "user not in a required group"}
		}
	}
	return nil
}

const (
	mcpConsentStateDomain = "arti-mcp-consent-v1."
	// Separate from loginConfirmIdentityDomain so an identity blob minted for
	// the CLI/device confirmation cannot verify here, or the reverse.
	mcpConsentIdentityDomain = "arti-mcp-consent-identity-v1."
	// Path-scoped to /oauth so it is never sent to the /auth confirmation
	// endpoints, and vice versa.
	mcpConsentCookie = "arti_oauth_consent_identity"
	mcpConsentTTL    = 5 * time.Minute
)

// mcpConsentState is the HMAC-signed blob that carries an authorization
// request across the consent page. It holds every parameter the code is
// bound to, so nothing can be swapped between the GET that rendered the
// page and the POST that approves it.
type mcpConsentState struct {
	Email         string `json:"e"`
	ClientID      string `json:"c"`
	RedirectURI   string `json:"r"`
	CodeChallenge string `json:"h"`
	Scope         string `json:"s"`
	State         string `json:"t"`
	Exp           int64  `json:"x"`
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
// This endpoint does NOT issue a code. It validates the request and
// renders a consent page whose POST to /oauth/authorize/confirm mints
// it. Minting on the GET made a code a side effect of a navigation: an
// attacker could register a public client (registration is anonymous by
// design), point its redirect_uri at itself, and get any signed-in user
// to open one link — the front door would authenticate the navigation,
// arti would hand the code to the attacker, and the public token
// endpoint would exchange it for a user-scoped token. The CLI and device
// flows already bind their codes behind an arti-served confirmation
// page for the same reason; see loginFinisher.renderConfirmation.
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

		// Look up client and validate redirect_uri.
		c, err := cfg.mcpClientFor(r.Context(), clientID, redirectURI)
		if err != nil {
			writeOAuthErr(w, err.status, err.code, err.desc)
			return
		}
		// redirect_uri is registered, so the client is real and errors may
		// safely be reported to it from here on. Parse it now: a stored URI
		// that won't parse must not reach the consent page.
		if _, perr := url.Parse(redirectURI); perr != nil {
			writeOAuthErr(w, http.StatusBadRequest, "invalid_request", "invalid redirect_uri")
			return
		}

		// Who is asking — the proxy headers only where a proxy is declared,
		// otherwise arti's own signed session. See identifyMCPCaller.
		caller, identified := cfg.identifyMCPCaller(r)
		if !identified {
			// Nobody is signed in yet. Send the browser through arti's own
			// login and back to this same authorization request, so the flow
			// completes without a proxy in front. return_to must stay
			// relative — the login handlers drop anything else — and
			// RequestURI carries every authorize parameter, so the round
			// trip loses nothing.
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, "/auth/login?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			return
		}
		email := caller.Email
		if aerr := cfg.allowCaller(r, caller); aerr != nil {
			writeOAuthErr(w, aerr.status, aerr.code, aerr.desc)
			return
		}

		// Ask the user. The code is minted by the POST, not by this GET.
		consent := signLoginBlob(cfg.Signer, mcpConsentStateDomain, mcpConsentState{
			Email: email, ClientID: clientID, RedirectURI: redirectURI,
			CodeChallenge: codeChallenge, Scope: scope, State: state,
			Exp: time.Now().Add(mcpConsentTTL).Unix(),
		})
		identity := signLoginBlob(cfg.Signer, mcpConsentIdentityDomain, loginConfirmIdentity{
			Email: email, Exp: time.Now().Add(mcpConsentTTL).Unix(),
		})
		http.SetCookie(w, &http.Cookie{
			Name: mcpConsentCookie, Value: identity, Path: "/oauth", Secure: cfg.CookieSecure,
			HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(mcpConsentTTL.Seconds()),
		})
		name := clientID
		if c.ClientName != nil && strings.TrimSpace(*c.ClientName) != "" {
			name = strings.TrimSpace(*c.ClientName)
		}
		w.Header().Set("Cache-Control", "no-store")
		brandPage{
			Title:   "arti — authorize access",
			Heading: "Authorize access to arti",
			Intro:   "An application is requesting access to arti as",
			Email:   email,
			Label:   "application",
			Code:    name,
			Note: "Approving lets it act as you in arti for " + humanDays(cfg.AccessTTL) +
				", with the same access you have. It then returns you to " + redirectURI +
				". This request expires in 5 minutes.",
			Action: &brandAction{
				Method: "POST",
				URL:    "/oauth/authorize/confirm",
				Hidden: []brandField{{Name: "consent", Value: consent}},
				Submit: "Authorize",
			},
			Cancel: "/",
			Footer: "Only authorize if you just started this sign-in yourself.",
		}.render(w)
	}
}

// MCPAuthorizeConfirmHandler mints the authorization code once the user has
// approved the request on the consent page. Same-origin POST only: the
// signed consent blob cannot be forged by a client that never received
// one, and the Strict-SameSite identity cookie means a cross-site form
// cannot replay a leaked blob.
//
// The blob carries no anti-replay nonce. Submitting it twice needs the
// victim's own browser (Strict, HttpOnly, /oauth-scoped cookie) and a live
// front-door session as the victim, and yields another code for the same
// user to the client they already approved, so replay grants nothing new.
func MCPAuthorizeConfirmHandler(cfg MCPOAuthConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var consent mcpConsentState
		if !verifyLoginBlob(cfg.Signer, mcpConsentStateDomain, r.FormValue("consent"), &consent) ||
			consent.Email == "" || consent.ClientID == "" || consent.RedirectURI == "" || consent.CodeChallenge == "" {
			http.Error(w, "invalid or expired authorization request", http.StatusForbidden)
			return
		}
		identityCookie, err := r.Cookie(mcpConsentCookie)
		if err != nil {
			http.Error(w, "missing login identity", http.StatusUnauthorized)
			return
		}
		var identity loginConfirmIdentity
		if !verifyLoginBlob(cfg.Signer, mcpConsentIdentityDomain, identityCookie.Value, &identity) ||
			!strings.EqualFold(identity.Email, consent.Email) {
			http.Error(w, "login identity mismatch", http.StatusForbidden)
			return
		}
		// A live session must identify this request as the same user the
		// consent page was rendered for, resolved exactly as the GET
		// resolved it. Treating absence as a pass would let the Strict
		// consent cookie stand in for being signed in, so an unidentified
		// POST is refused in every mode.
		caller, identified := cfg.identifyMCPCaller(r)
		if !identified {
			http.Error(w, "not signed in", http.StatusUnauthorized)
			return
		}
		if !strings.EqualFold(caller.Email, consent.Email) {
			http.Error(w, "login identity mismatch", http.StatusForbidden)
			return
		}
		// Re-check the access gates and the client: any of them may have
		// changed while the page sat open. Same conditions as the GET, so a
		// caller who lost access cannot approve a page they still hold.
		if aerr := cfg.allowCaller(r, caller); aerr != nil {
			http.Error(w, aerr.desc, aerr.status)
			return
		}
		if _, cerr := cfg.mcpClientFor(r.Context(), consent.ClientID, consent.RedirectURI); cerr != nil {
			http.Error(w, cerr.desc, cerr.status)
			return
		}
		u, err := url.Parse(consent.RedirectURI)
		if err != nil {
			http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
			return
		}

		code := randomToken(32)
		var scopePtr *string
		if consent.Scope != "" {
			scopePtr = &consent.Scope
		}
		if err := cfg.Store.InsertMCPCode(r.Context(), sqlc.InsertMCPCodeParams{
			Code:                code,
			ClientID:            consent.ClientID,
			RedirectUri:         consent.RedirectURI,
			CodeChallenge:       consent.CodeChallenge,
			CodeChallengeMethod: "S256",
			Email:               consent.Email,
			Scope:               scopePtr,
			ExpiresAt:           pgTimestamp(time.Now().Add(10 * time.Minute)),
		}); err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: mcpConsentCookie, Value: "", Path: "/oauth", MaxAge: -1,
			Secure: cfg.CookieSecure, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		})

		qq := u.Query()
		qq.Set("code", code)
		if consent.State != "" {
			qq.Set("state", consent.State)
		}
		u.RawQuery = qq.Encode()
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
}

// humanDays renders a token lifetime for the consent page, so the person
// approving sees how long the access lasts.
func humanDays(d time.Duration) string {
	if days := int(d.Hours() / 24); days >= 1 {
		return plural(days, "day")
	}
	return plural(int(d.Hours()), "hour")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// oauthErr carries the response an OAuth validation failure should produce,
// so the authorize GET (JSON errors) and the consent POST (plain text) can
// share one client lookup.
type oauthErr struct {
	status int
	code   string
	desc   string
}

func (e *oauthErr) Error() string { return e.desc }

// mcpClientFor loads a registered client and verifies that redirectURI is
// one of its registered redirect URIs.
func (cfg MCPOAuthConfig) mcpClientFor(ctx context.Context, clientID, redirectURI string) (sqlc.McpOauthClient, *oauthErr) {
	c, err := cfg.Store.GetMCPClient(ctx, clientID)
	if err != nil {
		return c, &oauthErr{http.StatusUnauthorized, "invalid_client", "unknown client_id"}
	}
	if !sliceContains(c.RedirectUris, redirectURI) {
		return c, &oauthErr{http.StatusBadRequest, "invalid_request", "redirect_uri is not registered for this client"}
	}
	return c, nil
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
