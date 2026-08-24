package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const (
	CookieName = "arti_session"

	// ACE-style header for service-to-service callers (e.g. couch FE proxy).
	APISecretHeader = "X-Arti-Service-Secret"

	// oauth2-proxy injects the upstream OIDC token here when running
	// inside the cluster; outside callers send Authorization: Bearer.
	ProxyTokenHeader = "X-Auth-Request-Access-Token"

	// LocalEmailHeader lets a request override the identity ONLY when auth is
	// disabled (local dev). It carries no privilege of its own — auth-disabled
	// mode already trusts every caller as ARTI_LOCAL_EMAIL — but it lets a dev
	// emulate a second person (e.g. test-user@…) against a browser logged in as
	// someone else, so multi-user flows like comment notifications are testable
	// locally. Ignored entirely when auth is enabled.
	LocalEmailHeader = "X-Arti-Local-Email"
)

// allowedEmails is the email allowlist. There is deliberately no
// baked-in default: with authentication enabled, an empty allowlist admits
// nobody (fail closed), and deployments configure their own entries via
// AUTH_ALLOWED_EMAILS / auth.allowed_emails.
var (
	allowedEmailsMu sync.RWMutex
	allowedEmails   []string
)

// NormalizeAllowlist lower-cases and trims allowlist entries, drops empty
// ones, and strips a leading "@" so both `example.com` and `@example.com`
// mean the same domain. Shared by SetAllowedEmails and by anything that
// needs to report on the configured gate (doctor), so there is exactly one
// definition of what an entry means.
func NormalizeAllowlist(entries []string) []string {
	cleaned := make([]string, 0, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(strings.ToLower(e))
		e = strings.TrimPrefix(e, "@")
		if e != "" {
			cleaned = append(cleaned, e)
		}
	}
	return cleaned
}

// SetAllowedEmails replaces the email allowlist used by IsAllowed (and
// therefore by RequireAuth, the Dex verifier, and test-mode token
// issuance). Entries are stored lower-cased and may be a bare domain
// (`example.com`, admitting everyone there) or a full address
// (`someone@example.com`, admitting exactly that person). An empty list
// means no email is allowed. Safe to call before any handler is wired.
func SetAllowedEmails(entries []string) {
	cleaned := NormalizeAllowlist(entries)
	allowedEmailsMu.Lock()
	defer allowedEmailsMu.Unlock()
	if len(cleaned) == 0 {
		allowedEmails = nil
		return
	}
	allowedEmails = cleaned
}

// AllowedEmails returns the active allowlist (lower-cased copy). Useful
// for /healthz introspection and tests.
func AllowedEmails() []string {
	allowedEmailsMu.RLock()
	defer allowedEmailsMu.RUnlock()
	out := make([]string, len(allowedEmails))
	copy(out, allowedEmails)
	return out
}

type ctxKey int

const (
	ctxEmail ctxKey = iota + 1
	ctxClaims
	ctxName
	ctxPicture
	ctxPeerAddr
)

// EmailFromContext returns the authenticated caller's email, or "".
func EmailFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxEmail).(string)
	return v
}

// NameFromContext returns the authenticated caller's display name, or "".
func NameFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxName).(string)
	return v
}

// PictureFromContext returns the authenticated caller's profile picture URL, or "".
func PictureFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxPicture).(string)
	return v
}

// ClaimsFromContext returns the verified JWT claims if present.
func ClaimsFromContext(ctx context.Context) (Claims, bool) {
	v, ok := ctx.Value(ctxClaims).(Claims)
	return v, ok
}

// WithIdentity is exposed for tests that bypass the middleware.
func WithIdentity(ctx context.Context, email string) context.Context {
	return context.WithValue(ctx, ctxEmail, email)
}

// WithTestClaims is exposed for tests that need to inject full Claims (including
// Typ, Scopes, etc.) without going through the middleware. Use WithIdentity when
// only an email is needed.
func WithTestClaims(ctx context.Context, c Claims) context.Context {
	return withClaims(ctx, c)
}

// IsAllowed reports whether the email matches the active allowlist
// (case-insensitive) — either as a full address or by its domain. Full
// addresses matter for anyone hosting against a consumer IdP, where the
// only expressible domain (say gmail.com) would otherwise admit every
// account at that provider. It does NOT check email_verified — that's the
// OIDC issuer's responsibility.
func IsAllowed(email string) bool {
	// Normalize here rather than at the call sites. IsAllowed is the shared
	// gate for every auth path — bearer claims, API keys, embed mints,
	// device, MCP OAuth, app tokens — and only the two interactive login
	// handlers trimmed before calling. Leaving it to callers meant one
	// identity could pass a domain entry and fail an address entry.
	email = strings.TrimSpace(email)
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	dom := email[at+1:]
	allowedEmailsMu.RLock()
	defer allowedEmailsMu.RUnlock()
	for _, allowed := range allowedEmails {
		if strings.EqualFold(dom, allowed) || strings.EqualFold(email, allowed) {
			return true
		}
	}
	return false
}

// Config bundles the verifiers + optional shared-secret used by the
// authentication middleware. Any field may be nil/empty; the middleware
// honors whichever subset is configured.
type Config struct {
	// OIDC is the Dex / oauth2-proxy verifier — used for raw Bearer JWTs.
	OIDC *OIDCVerifier

	// HS256 is the in-process JWT signer used by `arti login --email`
	// (test-mode), the CLI pair flow, and the browser arti_session cookie.
	HS256 *JWTSigner

	// RequiredGroups is the AUTH_REQUIRED_GROUPS list; matched against
	// oauth2-proxy's X-Auth-Request-Groups header (domain-suffix stripped).
	RequiredGroups []string

	// ServiceSecret enables the X-Arti-Service-Secret header for
	// service-to-service callers (matches ACE's X-AL-ACE-API-Secret).
	// Stored as a sha256 to avoid string comparison on a known secret.
	ServiceSecretHash [32]byte
	HasServiceSecret  bool

	// ServiceSecretEmail is the synthetic email recorded as the
	// creator for requests authenticated only via the shared secret.
	ServiceSecretEmail string

	// APIKeys authenticates opaque "arti_" bearers against the registry; nil disables the rung.
	APIKeys APIKeyAuthenticator
}

// NewConfig wires verifiers + secret with sensible empty defaults. The
// caller may override ServiceSecretEmail (config auth.service_email) after
// construction; the default is a reserved non-routable identity.
func NewConfig(oidc *OIDCVerifier, signer *JWTSigner, serviceSecret string, requiredGroups []string) Config {
	c := Config{OIDC: oidc, HS256: signer, RequiredGroups: requiredGroups, ServiceSecretEmail: "service@arti.invalid"}
	if serviceSecret != "" {
		c.ServiceSecretHash = sha256.Sum256([]byte(serviceSecret))
		c.HasServiceSecret = true
	}
	return c
}

// RequireAuth returns middleware that admits a request if ANY of the
// configured paths accept the credentials it carries:
//
//  1. X-Arti-Service-Secret header == configured secret → service caller
//  2. Dex / oauth2-proxy OIDC bearer (verified against the issuer JWKS)
//  3. In-process HS256 JWT (test-mode + dev `arti login --email`)
//
// On success the caller's email is stashed into the request context.
// On failure the request gets a 401 with a WWW-Authenticate hint.
func RequireAuth(cfg Config) func(http.Handler) http.Handler {
	return requireAuth(cfg, "")
}

// RequireAuthOrRedirect returns authentication middleware that redirects
// unauthenticated browser navigations to loginPath while preserving the
// existing JSON responses for API-style requests.
func RequireAuthOrRedirect(cfg Config, loginPath string) func(http.Handler) http.Handler {
	return requireAuth(cfg, loginPath)
}

type authFailure struct {
	status int
	reason string
}

func requireAuth(cfg Config, loginPath string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, failure := authenticate(cfg, r)
			if failure == nil {
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if loginPath != "" && isBrowserHTMLNavigation(r) {
				http.Redirect(w, r, loginPath+"?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
			if failure.status == http.StatusForbidden {
				forbidden(w, failure.reason)
				return
			}
			unauth(w, failure.reason)
		})
	}
}

func authenticate(cfg Config, r *http.Request) (context.Context, *authFailure) {
	// ── 1. shared service secret ───────────────────────────
	if cfg.HasServiceSecret {
		if HashEqual(r.Header.Get(APISecretHeader), cfg.ServiceSecretHash) {
			return withIdent(r.Context(), cfg.ServiceSecretEmail, []string{"service"}), nil
		}
	}

	// ── 2. (intentionally absent) X-Auth-Request-Email trust ──
	// We must NOT trust the oauth2-proxy identity header here.
	// Every route guarded by RequireAuth is served on the PUBLIC
	// ingress (/api/*, /mcp) — which has no oauth2-proxy in front
	// and does not strip client-supplied headers — so an inbound
	// X-Auth-Request-Email on these paths is attacker-controlled,
	// not verified identity. Trusting it was an unauthenticated
	// identity-spoofing hole (C1): a request was earlier admitted
	// as any email solely on a forgeable header.
	//
	// Legitimate callers don't need it here: browsers authenticate
	// via the arti_session cookie (rung 3/4 below, HS256-verified),
	// services via the shared secret (rung 1), and CLI/MCP/device
	// clients via Bearer (rung 3). The genuine oauth2-proxy header
	// path lives only on the ProtectedIngress handlers
	// (IngressLoginHandler at /auth/google/login, MCPAuthorizeHandler
	// at /oauth/authorize), where oauth2-proxy overwrites the header
	// via proxy_set_header so a client value cannot reach the pod.

	// ── 3/4. bearer token (OIDC first, HS256 fallback) ─
	tok := extractToken(r)
	if tok == "" {
		return nil, &authFailure{status: http.StatusUnauthorized, reason: "missing token"}
	}

	// ── API-key rung: opaque "arti_" bearer, registry-backed ──
	if cfg.APIKeys != nil && strings.HasPrefix(tok, APIKeyPrefix) {
		if c, err := cfg.APIKeys.Authenticate(r.Context(), tok); err == nil && IsAllowed(c.Email) {
			return withClaims(r.Context(), c), nil
		}
		return nil, &authFailure{status: http.StatusUnauthorized, reason: "invalid api key"}
	}

	if cfg.OIDC != nil {
		if c, err := cfg.OIDC.Verify(r.Context(), tok); err == nil {
			ctx := WithProfile(withIdent(r.Context(), c.Email, c.Scopes), c.Name, c.Picture)
			return ctx, nil
		} else if errors.Is(err, ErrForbidden) {
			return nil, &authFailure{status: http.StatusForbidden, reason: "user not authorized"}
		}
		// Other OIDC errors fall through to HS256 (may be a CLI token).
	}

	if cfg.HS256 != nil {
		c, err := cfg.HS256.Verify(tok)
		// Reject embed- and app-scoped tokens here: they are minted for
		// the comments overlay / APP bridge and exposed in served-page
		// source, so they must only work via their own middleware (which
		// pins them to a single artifact/app). Accepting one as a session
		// credential would let a leaked page token drive the full API as
		// that user.
		if err == nil && IsAllowed(c.Email) && !c.IsEmbedScoped() && !c.IsAppScoped() {
			ctx := WithProfile(withClaims(r.Context(), c), c.Name, c.Picture)
			return ctx, nil
		}
	}

	return nil, &authFailure{status: http.StatusUnauthorized, reason: "invalid token"}
}

func isBrowserHTMLNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return r.Header.Get("Authorization") == "" &&
		strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
}

// Disabled returns middleware that bypasses authentication entirely.
// It injects the caller email + a synthetic Claims into context so
// downstream handlers see a valid identity. Intended only for local dev.
// Production deployments MUST NOT enable this (the binary logs a loud
// warning at startup).
func Disabled(localEmail string) func(http.Handler) http.Handler {
	if localEmail == "" {
		localEmail = "local@example.com"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			email := localEmail
			if h := strings.TrimSpace(r.Header.Get(LocalEmailHeader)); h != "" {
				email = h // dev-only per-request identity override (see LocalEmailHeader)
			}
			next.ServeHTTP(w, r.WithContext(withIdent(r.Context(), email, []string{"user"})))
		})
	}
}

func withIdent(ctx context.Context, email string, scopes []string) context.Context {
	ctx = context.WithValue(ctx, ctxEmail, email)
	ctx = context.WithValue(ctx, ctxClaims, Claims{Email: email, Scopes: scopes})
	return ctx
}

// withClaims stores the fully-parsed verified claims (preserving Fam/JTI/Scopes)
// so scope-aware middleware like EnforceUploadScope can read them. Used on the
// HS256 bearer path, where device upload tokens (which carry Fam) arrive.
func withClaims(ctx context.Context, c Claims) context.Context {
	ctx = context.WithValue(ctx, ctxEmail, c.Email)
	ctx = context.WithValue(ctx, ctxClaims, c)
	return ctx
}

// WithProfile enriches the context with profile information (display name
// and picture URL). Exported so the embed-auth middleware can set them.
func WithProfile(ctx context.Context, name, picture string) context.Context {
	if name != "" {
		ctx = context.WithValue(ctx, ctxName, name)
	}
	if picture != "" {
		ctx = context.WithValue(ctx, ctxPicture, picture)
	}
	return ctx
}

func extractToken(r *http.Request) string {
	// 1. Explicit Authorization: Bearer — API / CLI / MCP callers.
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	// 2. arti's own arti_session cookie — preferred over the oauth2-proxy
	// access token below. On an oauth2-proxy'd HTML route (e.g. /app) a browser
	// carries BOTH; the cookie is arti-signed and minted only after the
	// required-groups gate at login (IngressLoginHandler, via
	// X-Auth-Request-Groups), whereas the forwarded access token has no `groups`
	// claim — so verifying the access token instead fails RequireAuth's OIDC
	// group check and 403s every app for every user (regression from the C1 fix,
	// #96). Preferring the verified cookie fixes that without weakening the gate.
	if c, err := r.Cookie(CookieName); err == nil {
		return c.Value
	}
	// 3. Fallback: oauth2-proxy's forwarded access token (verified downstream).
	if h := r.Header.Get(ProxyTokenHeader); h != "" {
		return strings.TrimSpace(h)
	}
	return ""
}

func unauth(w http.ResponseWriter, reason string) {
	w.Header().Set("WWW-Authenticate",
		`Bearer realm="arti", resource_metadata="`+ProtectedResourceMetadataURL()+`"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"detail":"` + jsonEsc(reason) + `","code":"unauthorized"}`))
}

func forbidden(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"detail":"` + jsonEsc(reason) + `","code":"forbidden"}`))
}

func jsonEsc(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return r.Replace(s)
}
