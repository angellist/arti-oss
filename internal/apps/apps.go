// Package apps implements arti's governed app-serving proxy: the back end for
// APP artifacts. An APP is a sandboxed single-page app served by arti; its JS
// calls window.arti.callTool(server, tool, args), which POSTs here. This
// service authenticates the call with the scoped app token arti injected into
// the page and enforces the app's arti-app.json tool allowlist. arti's own
// tools it then runs in-process as the viewer; for every other server it
// resolves the named upstream and forwards a single tools/call — attaching the
// viewer's per-user upstream credential (OBO) when the server requires auth.
//
// It is mounted OUTSIDE arti's cookie-auth middleware (like the comments embed
// endpoint), because a sandboxed opaque-origin page can't send the session
// cookie — it authenticates with the injected Bearer token instead, over CORS.
package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/comments"
	"github.com/angellist/arti-oss/internal/httperr"
	"github.com/angellist/arti-oss/internal/mcpclient"
	"github.com/angellist/arti-oss/internal/pkgzip"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

const (
	// appScopePrefix scopes an injected app token to exactly one APP artifact,
	// mirroring the comments embed-token pattern. A page can only drive the
	// proxy for its own artifact, nothing else. The same prefix is rejected by
	// the main auth middleware (auth.IsAppScoped) so a leaked app token can't be
	// used as a session credential on the regular API/MCP routes.
	appScopePrefix = auth.AppScopePrefix
	appTokenTTL    = 12 * time.Hour
	// embedUserScope marks a token minted by the /auth/embed/app-token popup
	// handshake (a user-mode embed surface). Unlike a token injected into a
	// page arti served, this one is DELIVERED via postMessage to an opener arti
	// cannot authenticate — so it is marked, minted with a shorter TTL, and
	// audit-logged, enabling monitoring and a targeted kill-switch without
	// touching /app tokens.
	embedUserScope = "embed-user"
	// embedViewerScope marks a token minted by the viewer-mode embed handshake
	// for a DOCUMENT render. Deliberately distinct from embedUserScope so that
	// VerifyEmbedViewerToken can reject an APP token: without it, any token
	// carrying an app scope would authorize a viewer document render, which is the
	// one direction that must not happen (a document render has no second gate).
	//
	// The reverse direction is INTENTIONALLY open. handleMCP accepts any authentic
	// token with an email and an app scope, so a viewer token minted for an APP
	// artifact does authorize tool calls on that same app. That is wanted: an APP
	// embedded in viewer mode must keep working, and the token grants exactly what
	// that viewer would get from a user-mode surface — same identity, same app,
	// same consent click. Narrowing it here would break APP embeds. See
	// TestEmbedViewerToken_ScopeSeparation, which pins both halves.
	//
	// Same TTL as embedUserScope.
	embedViewerScope  = "embed-viewer"
	embedUserTokenTTL = 8 * time.Hour
	// embedViewerTokenTTL is deliberately much shorter than embedUserTokenTTL: a
	// viewer token rides in a URL query parameter, so it can land in browser
	// history, and it only has to survive the gate page's single self-reload,
	// which happens within seconds of consent. An expired one simply re-prompts.
	embedViewerTokenTTL = 5 * time.Minute
	// embedFilesScopePrefix scopes a user-mode sibling-files token to one
	// artifact (scope `app-files:<artifactID>`). It carries NO email: a
	// user-mode surface serves static content gated on the surface secret, not
	// a viewer identity, so its sibling assets are authorized the same way.
	// Deliberately NOT the app: prefix — it must never pass VerifyEmbedToken
	// (which requires an email) or appOf; and it can't be replayed as a session
	// credential because RequireAuth rejects empty-email tokens (IsAllowed).
	embedFilesScopePrefix = "app-files:"
	// ManifestPath is the per-app tool-allowlist file inside the APP zip.
	ManifestPath = "arti-app.json"
	// maxToolCallBytes caps one tool-call body at the same ceiling every other
	// write door has, so an app is never the tighter path.
	maxToolCallBytes = artifacts.MaxUploadBytes
)

// ServerConfig is a named upstream MCP server an APP may reach. Apps reference
// it by Name only; the URL and auth policy live here (server-side), so an app
// author can neither point at an arbitrary endpoint nor embed a secret.
type ServerConfig struct {
	Name        string `json:"name"`
	ResourceURL string `json:"resource_url"`    // the MCP endpoint arti forwards to
	Auth        string `json:"auth"`            // "none" | "oauth"
	Scope       string `json:"scope,omitempty"` // OAuth scope for "oauth"
}

// TokenProvider yields a per-user upstream Bearer for an auth'd server (the
// arti→upstream OBO leg). BearerFor returns "" when the user hasn't authorized
// yet; AuthorizeURL builds the consent URL the page should open. Implemented by
// internal/obo; nil means "auth'd servers unavailable".
type TokenProvider interface {
	BearerFor(ctx context.Context, email string, sc ServerConfig) (string, error)
	AuthorizeURL(ctx context.Context, email string, sc ServerConfig) (string, error)
}

// Completer runs the built-in single-Claude completion for the `llm` server
// (Auth:"service"). Given the raw tool arguments, it returns the MCP result
// JSON, or (httpStatus>0, errBody) on failure. Implemented by internal/llm
// structurally — neither package imports the other. nil ⇒ the llm server is
// unavailable.
type Completer interface {
	RunCompletion(ctx context.Context, viewer, appID string, args map[string]any) (result json.RawMessage, httpStatus int, errBody json.RawMessage)
}

// ArtiTools runs one of arti's OWN tools in-process, as the caller in ctx (set
// via auth.WithIdentity), returning the MCP content envelope. Implemented by
// *mcp.Server (CallToolInProcess). nil ⇒ arti calls fall back to the OBO path.
// Lets an APP reach other artifacts (as the viewer) with no consent popup.
type ArtiTools interface {
	CallToolInProcess(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
}

// artiInProcess is the set of arti's own tools served in-process — reads and
// writes alike. An app's access to arti must not depend on leaving the
// cluster: routing a write out to a gateway and back made every write wait on
// an internet round trip that could time out, for a hop that authorizes
// nothing the in-process call doesn't. Anything not here (or any non-arti
// server) still takes the OBO path.
var artiInProcess = map[string]bool{
	"get_artifact":           true,
	"read_artifact":          true,
	"read_package_file":      true,
	"list_package_files":     true,
	"list_artifacts":         true,
	"search_artifacts":       true,
	"list_artifact_versions": true,
	"list_comments":          true,
	"add_comment":            true,
	"reply_to_comment":       true,
	"resolve_comment":        true,
	"add_artifact":           true,
	"append_artifact":        true,
	"update_artifact":        true,
	"archive_artifact":       true,
	"map_get":                true,
	"map_put":                true,
	"map_delete":             true,
	"map_snapshot":           true,
}

// isArtiSelf reports whether server names this arti instance. Both spellings
// exist: "arti-self" is the built-in, "arti" is what a deployment's gateway
// map calls it and what app manifests in the wild already declare.
func isArtiSelf(server string) bool { return server == "arti" || server == "arti-self" }

// Service is the apps proxy.
type Service struct {
	art       *pgstore.Store
	signer    *auth.JWTSigner
	mcp       *mcpclient.Client
	servers   map[string]ServerConfig
	tokens    TokenProvider // may be nil (no OBO wired)
	completer Completer     // may be nil (no llm wired)
	arti      ArtiTools     // may be nil (arti calls fall back to OBO)
	logger    *slog.Logger
	rejects   rejectSampler

	maxTimeout time.Duration // cap on a call's requested timeout_ms
	inflight   chan struct{} // upstream calls in progress; nil = unbounded
}

// ProxyPath is the tool-call endpoint. The router exempts it from the blanket
// request timeout because the proxy sets its own per-call deadline.
const ProxyPath = "/api/apps/mcp"

const (
	// defaultCallTimeout applies when the app sends no timeout_ms.
	defaultCallTimeout = 60 * time.Second
	// DefaultMaxCallTimeout caps timeout_ms. arti must answer before the edge
	// does (nginx reads for 95s, Cloudflare for 100s) or the app gets an HTML
	// error page instead of arti's JSON error.
	DefaultMaxCallTimeout = 90 * time.Second
	// DefaultMaxInflight bounds concurrent upstream calls per pod: each one may
	// buffer MaxResponseBytes twice while it is parsed, so inflight × 2 ×
	// MaxResponseBytes must stay well under the pod memory limit.
	DefaultMaxInflight = 16
)

// upstreamFailStatus is the HTTP status for every upstream-side failure the
// proxy reports. Never 502 or 504: Cloudflare fronts the public ingress and
// replaces an origin 502/504 with its own HTML error page, which carries no
// CORS headers, so the sandboxed APP page sees "Failed to fetch" and never the
// JSON body. 503 passes through untouched.
const upstreamFailStatus = http.StatusServiceUnavailable

// SetArtiTools wires the in-process arti path (see ArtiTools). Optional; when
// unset, arti's tools take the OBO path like any other server.
func (s *Service) SetArtiTools(t ArtiTools) { s.arti = t }

func New(art *pgstore.Store, signer *auth.JWTSigner, servers map[string]ServerConfig, tokens TokenProvider, completer Completer, logger *slog.Logger) *Service {
	if servers == nil {
		servers = map[string]ServerConfig{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{art: art, signer: signer, mcp: mcpclient.New(), servers: servers, tokens: tokens, completer: completer, logger: logger}
	s.SetLimits(DefaultMaxCallTimeout, DefaultMaxInflight)
	return s
}

// SetLimits sets the cap on a call's requested timeout and the number of
// upstream calls one process relays at once (0 = unbounded).
func (s *Service) SetLimits(maxTimeout time.Duration, maxInflight int) {
	if maxTimeout <= 0 {
		maxTimeout = DefaultMaxCallTimeout
	}
	s.maxTimeout = maxTimeout
	s.inflight = nil
	if maxInflight > 0 {
		s.inflight = make(chan struct{}, maxInflight)
	}
}

// callTimeout is the deadline for one call: the app's timeout_ms, clamped to
// the cap; the default when it asked for none.
func (s *Service) callTimeout(ms int) time.Duration {
	if ms <= 0 {
		return defaultCallTimeout
	}
	d := time.Duration(ms) * time.Millisecond
	if s.maxTimeout > 0 && d > s.maxTimeout {
		d = s.maxTimeout
	}
	return d
}

// SignAppToken mints the scoped token injected into a served APP page. Wired to
// artifacts.Service.SetAppTokenFn so the bridge can authenticate proxy calls.
func (s *Service) SignAppToken(email, artifactID string) (string, error) {
	return s.signer.Sign(auth.Claims{Email: email, Scopes: []string{appScopePrefix + artifactID}, TTL: appTokenTTL})
}

// VerifyEmbedToken verifies a token minted by SignAppToken and returns the
// caller email + artifact id it is scoped to. Used by the embed sibling-files
// route to authorize reading a packaged artifact's files without a session —
// the token in the files URL path is the credential (it was minted only after
// the embed surface's shared secret was validated on the document request).
func (s *Service) VerifyEmbedToken(tok string) (email, artifactID string, err error) {
	claims, verr := s.signer.Verify(tok)
	if verr != nil {
		return "", "", verr
	}
	app := appOf(claims)
	if claims.Email == "" || app == "" {
		return "", "", errors.New("token missing app scope")
	}
	return claims.Email, app, nil
}

func appOf(c auth.Claims) string {
	for _, sc := range c.Scopes {
		if strings.HasPrefix(sc, appScopePrefix) {
			return strings.TrimPrefix(sc, appScopePrefix)
		}
	}
	return ""
}

// appConsentDomain separates the consent record from every JWT the signer
// mints: the value never parses as a token, so it cannot stand in for a
// session or the bridge.
const (
	appConsentDomain = "arti-app-consent:"
	appConsentTTL    = 30 * 24 * time.Hour
)

type appConsent struct {
	Email string `json:"e"`
	App   string `json:"a"`
	Exp   int64  `json:"x"`
}

// SignAppConsent seals the record that email chose to run artifactID. Wired to
// artifacts.Service.SetAppConsentFns.
func (s *Service) SignAppConsent(email, artifactID string) (string, error) {
	return s.signer.SealBlob(appConsentDomain, appConsent{Email: email, App: artifactID, Exp: time.Now().Add(appConsentTTL).Unix()}), nil
}

// VerifyAppConsent returns the (email, artifactID) a consent record binds.
func (s *Service) VerifyAppConsent(value string) (email, artifactID string, err error) {
	var c appConsent
	if !s.signer.OpenBlob(appConsentDomain, value, &c) {
		return "", "", errors.New("not an app consent record")
	}
	if c.Email == "" || c.App == "" || time.Now().Unix() > c.Exp {
		return "", "", errors.New("app consent record expired or incomplete")
	}
	return c.Email, c.App, nil
}

// EmbedUserApp authorizes a user-mode embed mint request: it verifies email
// can read the version-pinned APP artifact appID (a UUID — mint-by-slug would
// scope the token to a row that can diverge from the one the page was served
// with, 403ing every call) and that it IS an APP. Returns the title + slug for
// the consent page and the mint handler's slug_allow re-check. Denials come
// back as pgstore.ErrNotFound so the handler can 404 without probing leaks.
func (s *Service) EmbedUserApp(ctx context.Context, appID, email string) (title, slug string, err error) {
	id, perr := uuid.Parse(appID)
	if perr != nil {
		return "", "", pgstore.ErrNotFound
	}
	row, ok, err := s.canRead(ctx, id, email)
	if err != nil && !errors.Is(err, pgstore.ErrNotFound) {
		return "", "", err
	}
	if err != nil || !ok || row.ArtifactType != pgstore.TypeApp {
		return "", "", pgstore.ErrNotFound
	}
	if row.NamedSlug != nil {
		slug = *row.NamedSlug
	}
	return row.Title, slug, nil
}

// MintEmbedUserToken mints the per-user app token the /auth/embed/app-token
// popup handshake delivers: the same shape SignAppToken injects into an /app
// page, but re-checked against email's read access here, marked with the
// embed-user scope, and short-TTL (see embedUserScope). The caller (the mint
// handler) owns the human consent gate + audit log around this.
func (s *Service) MintEmbedUserToken(ctx context.Context, appID, email string) (string, error) {
	if _, _, err := s.EmbedUserApp(ctx, appID, email); err != nil {
		return "", err
	}
	return s.signer.Sign(auth.Claims{
		Email:  email,
		Scopes: []string{appScopePrefix + appID, embedUserScope},
		TTL:    embedUserTokenTTL,
	})
}

// EmbedViewerArtifact authorizes a viewer-mode embed request: it verifies email
// can read the artifact named by ident (slug or UUID) and returns its title,
// slug and UUID. Unlike EmbedUserApp it does NOT require an APP — viewer mode
// gates the render of any artifact type. Denials come back as
// pgstore.ErrNotFound so the caller can 404 without leaking existence.
func (s *Service) EmbedViewerArtifact(ctx context.Context, ident string, ver *int32, email string) (title, slug, artifactID string, isApp bool, err error) {
	var row sqlc.Artifact
	if id, perr := uuid.Parse(ident); perr == nil {
		var ok bool
		row, ok, err = s.canRead(ctx, id, email)
		if err != nil && !errors.Is(err, pgstore.ErrNotFound) {
			return "", "", "", false, err
		}
		if err != nil || !ok {
			return "", "", "", false, pgstore.ErrNotFound
		}
	} else if ver == nil {
		// GetLatestBySlugForCaller applies the same read rule as canRead and
		// returns ErrNotFound for both "absent" and "not yours".
		row, err = s.art.GetLatestBySlugForCaller(ctx, ident, email)
		if err != nil {
			return "", "", "", false, pgstore.ErrNotFound
		}
	} else {
		// A pinned version: fetch that exact row, then apply the same read rule.
		// GetBySlug does not access-check, so canRead does it by UUID below.
		vrow, verr := s.art.GetBySlug(ctx, ident, ver)
		if verr != nil {
			return "", "", "", false, pgstore.ErrNotFound
		}
		var ok bool
		row, ok, err = s.canRead(ctx, pgstore.UUIDFromPG(vrow.ArtifactID), email)
		if err != nil && !errors.Is(err, pgstore.ErrNotFound) {
			return "", "", "", false, err
		}
		if err != nil || !ok {
			return "", "", "", false, pgstore.ErrNotFound
		}
	}
	if row.NamedSlug != nil {
		slug = *row.NamedSlug
	}
	return row.Title, slug, pgstore.UUIDFromPG(row.ArtifactID).String(), row.ArtifactType == pgstore.TypeApp, nil
}

// MintEmbedViewerToken mints the short-TTL, embed-viewer-marked token the
// viewer-mode gate page reloads itself with. Re-checks read access here; the
// mint handler owns the human consent gate and the audit log around it.
func (s *Service) MintEmbedViewerToken(ctx context.Context, artifactID, email string) (string, error) {
	// artifactID is a UUID, so this resolve is already version-exact.
	if _, _, _, _, err := s.EmbedViewerArtifact(ctx, artifactID, nil, email); err != nil {
		return "", err
	}
	return s.signer.Sign(auth.Claims{
		Email:  email,
		Scopes: []string{appScopePrefix + artifactID, embedViewerScope},
		TTL:    embedViewerTokenTTL,
	})
}

// VerifyEmbedViewerToken verifies a token minted by MintEmbedViewerToken and
// returns the viewer email + artifact id it is scoped to. It REQUIRES the
// embed-viewer scope, so an APP token (embed-user, or a plain SignAppToken
// token) is rejected here even though it carries the same app scope shape.
func (s *Service) VerifyEmbedViewerToken(tok string) (email, artifactID string, err error) {
	claims, verr := s.signer.Verify(tok)
	if verr != nil {
		return "", "", verr
	}
	if !hasScope(claims, embedViewerScope) {
		return "", "", errors.New("token missing embed-viewer scope")
	}
	app := appOf(claims)
	if claims.Email == "" || app == "" {
		return "", "", errors.New("token missing app scope")
	}
	return claims.Email, app, nil
}

func hasScope(c auth.Claims, want string) bool {
	for _, sc := range c.Scopes {
		if sc == want {
			return true
		}
	}
	return false
}

// SignEmbedFilesToken mints the sibling-files credential for a user-mode embed
// page (see embedFilesScopePrefix). Pure signing — the embed doc handler only
// calls it after the surface's secret + slug_allow gates passed, which is the
// entire access model for user-mode static content.
func (s *Service) SignEmbedFilesToken(artifactID string) (string, error) {
	return s.signer.Sign(auth.Claims{Scopes: []string{embedFilesScopePrefix + artifactID}, TTL: appTokenTTL})
}

// VerifyEmbedFilesToken verifies a SignEmbedFilesToken credential and returns
// the artifact id it is scoped to. Counterpart of VerifyEmbedToken for
// user-mode surfaces, where there is no serve-time viewer identity.
func (s *Service) VerifyEmbedFilesToken(tok string) (artifactID string, err error) {
	claims, verr := s.signer.Verify(tok)
	if verr != nil {
		return "", verr
	}
	if claims.Email != "" {
		return "", errors.New("not a files token")
	}
	for _, sc := range claims.Scopes {
		if strings.HasPrefix(sc, embedFilesScopePrefix) {
			return strings.TrimPrefix(sc, embedFilesScopePrefix), nil
		}
	}
	return "", errors.New("token missing files scope")
}

// MountProxy registers the proxy endpoint on the PUBLIC group (its own Bearer
// auth + CORS). Mount this OUTSIDE the cookie-auth middleware.
func (s *Service) MountProxy(r chi.Router) {
	r.Options(ProxyPath, func(w http.ResponseWriter, _ *http.Request) { setCORS(w); w.WriteHeader(http.StatusNoContent) })
	r.Group(func(g chi.Router) {
		g.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { setCORS(w); next.ServeHTTP(w, req) })
		})
		g.Post(ProxyPath, s.handleMCP)
	})
}

type proxyReq struct {
	AppID     string         `json:"app_id"`
	Server    string         `json:"server"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
	TimeoutMs int            `json:"timeout_ms,omitempty"`
}

func (s *Service) handleMCP(w http.ResponseWriter, r *http.Request) {
	// 1. Authenticate the injected app token.
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, err := s.signer.Verify(tok)
	if err != nil {
		// claims.Email is set when the token is expired-but-authentic (Verify
		// checks the signature before expiry), turning an anonymous 401 stream
		// into "this person's open tab needs a refresh". Empty otherwise.
		s.logReject(r, http.StatusUnauthorized, "invalid app token: "+err.Error(), claims.Email)
		writeErr(w, http.StatusUnauthorized, "invalid app token")
		return
	}
	email := claims.Email
	scopedApp := appOf(claims)
	if email == "" || scopedApp == "" {
		s.logReject(r, http.StatusUnauthorized, "app token missing scope", email)
		writeErr(w, http.StatusUnauthorized, "app token missing scope")
		return
	}
	// Re-check the domain allowlist (mirrors the main auth middleware): a user
	// whose access was pulled must not keep driving OBO tool calls until the
	// 12h token expires.
	if !auth.IsAllowed(email) {
		s.logReject(r, http.StatusForbidden, "email not in allowlist", email)
		writeErr(w, http.StatusForbidden, "not permitted")
		return
	}

	var req proxyReq
	if err := json.NewDecoder(io.LimitReader(r.Body, maxToolCallBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if req.AppID != scopedApp {
		writeErr(w, http.StatusForbidden, "app token does not match app_id")
		return
	}
	id, err := uuid.Parse(req.AppID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad app_id")
		return
	}

	// 2. Load the APP, enforce read access (defense in depth). A genuine
	// missing/forbidden artifact is 404 (so callers can't probe restricted
	// ones); a real store failure (e.g. transient DB error) is 500, not masked
	// as not-found.
	row, ok, err := s.canRead(r.Context(), id, email)
	if err != nil && !errors.Is(err, pgstore.ErrNotFound) {
		writeErr(w, http.StatusInternalServerError, "store error")
		return
	}
	if err != nil || !ok {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if row.ArtifactType != pgstore.TypeApp {
		writeErr(w, http.StatusBadRequest, "artifact is not an APP")
		return
	}

	// 3. Enforce the app's tool allowlist (read fresh from the manifest).
	man, err := s.readManifest(r.Context(), row)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !man.allows(req.Server, req.Tool) {
		writeUpstreamCode(w, http.StatusForbidden, "not_allowlisted", req.Server, req.Tool,
			"tool "+req.Server+"/"+req.Tool+" is not in this app's arti-app.json allowlist")
		return
	}
	// The app's budget covers the tool call only, not arti's own lookups above:
	// a deadline that fired while reading the manifest surfaced as a bare 400.
	timeout := s.callTimeout(req.TimeoutMs)
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	// 4. arti's own tools run in-process as the viewer — reads and writes both.
	// No OBO hop, no consent popup, and no dependence on an `arti` entry in
	// ARTI_APP_MCP_SERVERS, so this works on a deployment that configures no
	// gateway at all and where Runlayer is unreachable (local dev). Access is
	// identical to calling arti directly: the in-process handlers take the
	// caller from the context set here and re-resolve groups/access, so an app
	// reads and writes exactly what this viewer already can. The write is
	// stamped to the APP rather than to the viewer, who presented no bearer of
	// their own — see auth.WithAppCredential. Deliberately ahead of the server
	// lookup below: arti access must not be a deployment's to withhold.
	if isArtiSelf(req.Server) && s.arti != nil && artiInProcess[req.Tool] {
		argsJSON, merr := json.Marshal(req.Arguments)
		if merr != nil {
			writeErr(w, http.StatusBadRequest, "bad arguments: "+merr.Error())
			return
		}
		actx := auth.WithAppCredential(auth.WithIdentity(ctx, email), req.AppID, row.Title)
		out, rerr := s.arti.CallToolInProcess(actx, req.Tool, argsJSON)
		if rerr != nil {
			s.writeInProcessErr(w, r, req.Server, req.Tool, timeout, rerr)
			return
		}
		if len(out) == 0 {
			out = json.RawMessage(`{}`)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
		return
	}

	// 5. Resolve the named server (URL + auth policy live server-side).
	sc, known := s.servers[req.Server]
	if !known {
		writeUpstreamCode(w, http.StatusBadRequest, "unknown_server", req.Server, req.Tool,
			"unknown server "+req.Server+" (not configured on this arti)")
		return
	}

	// 6. Built-in completion (the `llm` server, Auth:"service") — dispatched
	// in-process to the Anthropic Messages API with arti's service key. No OBO,
	// no mcpclient: a completion has no per-user upstream data. The viewer's
	// email is used only for budget + usage attribution inside the completer.
	if sc.Auth == "service" {
		// The llm service serves exactly one tool. The allowlist above already
		// gates access, but don't route an unrelated tool name to completion.
		if req.Tool != "complete" {
			writeErr(w, http.StatusNotFound, "unknown tool "+req.Server+"/"+req.Tool)
			return
		}
		if s.completer == nil {
			writeErr(w, http.StatusNotImplemented, "server "+req.Server+" needs the llm service, which is not configured on this arti")
			return
		}
		result, status, errBody := s.completer.RunCompletion(ctx, email, req.AppID, req.Arguments)
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write(errBody)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(result)
		return
	}

	// 7. Attach the per-user upstream credential (OBO) when required.
	var bearer string
	if sc.Auth == "oauth" {
		if s.tokens == nil {
			writeErr(w, http.StatusNotImplemented, "server "+req.Server+" needs OAuth, which is not configured on this arti")
			return
		}
		b, terr := s.tokens.BearerFor(r.Context(), email, sc)
		if terr != nil {
			writeErr(w, http.StatusInternalServerError, "token lookup: "+terr.Error())
			return
		}
		if b == "" {
			s.writeAuthRequired(w, r.Context(), email, sc)
			return
		}
		bearer = b
	}

	// 8. Forward one tools/call upstream, if this pod has room for another.
	if s.inflight != nil {
		select {
		case s.inflight <- struct{}{}:
			defer func() { <-s.inflight }()
		default:
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": "proxy_busy", "server": req.Server, "tool": req.Tool, "retry_after": 1,
				"detail": fmt.Sprintf("arti is already relaying %d upstream calls; retry in a moment.", cap(s.inflight)),
				"code":   http.StatusText(http.StatusServiceUnavailable),
			})
			return
		}
	}
	result, callErr := s.mcp.CallTool(ctx, sc.ResourceURL, bearer, req.Tool, req.Arguments)
	if errors.Is(callErr, mcpclient.ErrUnauthorized) {
		// A 401 only means "re-consent" for oauth (OBO) servers — the stored
		// token expired/was revoked. For auth:"none" servers (e.g. arti-self) a
		// 401 is a genuine upstream rejection, not something an OAuth consent
		// can fix, so surface it as an upstream error instead of looping the
		// user through a meaningless authorize flow.
		if sc.Auth == "oauth" && s.tokens != nil {
			s.writeAuthRequired(w, r.Context(), email, sc)
			return
		}
		writeUpstreamCode(w, upstreamFailStatus, "upstream_error", req.Server, req.Tool,
			"upstream rejected the call (401) for server "+req.Server)
		return
	}
	if callErr != nil {
		s.writeUpstreamErr(w, r, req.Server, req.Tool, timeout, callErr)
		return
	}
	if len(result) == 0 {
		result = json.RawMessage(`{}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

// writeAuthRequired returns 401 with an authorize_url; the page's shim opens it
// (the OAuth consent), then retries the tool call.
func (s *Service) writeAuthRequired(w http.ResponseWriter, ctx context.Context, email string, sc ServerConfig) {
	authURL, err := s.tokens.AuthorizeURL(ctx, email, sc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "authorize url: "+err.Error())
		return
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error":         "authorization_required",
		"server":        sc.Name,
		"authorize_url": authURL,
	})
}

// ─── manifest ────────────────────────────────────────────────────────

type manifest struct {
	Name  string `json:"name"`
	Entry string `json:"entry"`
	Tools []struct {
		Server string `json:"server"`
		Tool   string `json:"tool"`
	} `json:"tools"`
}

func (m manifest) allows(server, tool string) bool {
	for _, t := range m.Tools {
		if t.Server == server && t.Tool == tool {
			return true
		}
	}
	return false
}

func (s *Service) readManifest(ctx context.Context, row sqlc.Artifact) (manifest, error) {
	rc, err := s.art.Content(ctx, row)
	if err != nil {
		return manifest{}, errBadRequest("read app bytes: " + err.Error())
	}
	defer rc.Close()
	zb, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return manifest{}, errBadRequest("read app bytes: " + err.Error())
	}
	body, _, err := pkgzip.ReadEntry(zb, ManifestPath)
	if err != nil {
		return manifest{}, errBadRequest("APP is missing its " + ManifestPath + " manifest")
	}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return manifest{}, errBadRequest("bad " + ManifestPath + ": " + err.Error())
	}
	return m, nil
}

// ─── access (mirrors the artifact read rule) ─────────────────────────

func (s *Service) canRead(ctx context.Context, id uuid.UUID, caller string) (sqlc.Artifact, bool, error) {
	row, err := s.art.GetByID(ctx, id)
	if err != nil {
		return row, false, err
	}
	if ok, err := s.art.HasPermission(ctx, caller, rbac.ManageArtifacts); err != nil {
		return row, false, err
	} else if ok {
		return row, true, nil
	}
	if row.DeletedAt.Valid && !strings.EqualFold(row.Creator, caller) {
		return row, false, nil
	}
	groups, err := s.art.CallerGroups(ctx, caller)
	if err != nil {
		return row, false, err
	}
	return row, pgstore.CanAccess(row, caller, groups), nil
}

// ─── helpers ─────────────────────────────────────────────────────────

func setCORS(w http.ResponseWriter) {
	// Bearer-token auth (no cookie) → `*` is safe; the token is the credential.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Max-Age", "600")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"detail": msg, "code": http.StatusText(code)})
}

// writeUpstreamErr answers a failed tools/call. The detail goes in the response
// body, which nothing stores, so the failure is also logged with the server and
// tool that produced it: 82 of these landed in one prod day with nothing
// recorded but a duration, and no way to tell which upstream was at fault.
//
// A cancelled request means the viewer closed the app mid-call, which is
// neither an upstream nor a server fault, so it answers 499 and logs nothing. A
// deadline is a real fault and stays logged. httperr.ClientGone draws that line.
//
// Every failure carries a stable `error` code so an app can react to it
// instead of pattern-matching a message; the status is always
// upstreamFailStatus (see there for why not 502/504). "upstream_timeout": the
// upstream may well have finished the work after arti stopped waiting (a
// Snowflake statement keeps running and its result stays retrievable by
// handle), so the right recovery is to resume, not to re-run — re-running is
// how one slow query became three. "upstream_response_too_large": the fix is a
// smaller page. "tool_error": a JSON-RPC error object, the tool itself
// refused, so a retry cannot help. Anything else is "upstream_error".
func (s *Service) writeUpstreamErr(w http.ResponseWriter, r *http.Request, server, tool string, timeout time.Duration, err error) {
	if httperr.ClientGone(r, err) {
		w.WriteHeader(httperr.StatusClientClosedRequest)
		return
	}
	s.logger.Error("apps proxy: upstream failed",
		"server", server, "tool", tool, "timeout", timeout, "err", err)
	var rpcErr *mcpclient.RPCError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeUpstreamCode(w, upstreamFailStatus, "upstream_timeout", server, tool,
			fmt.Sprintf("upstream %s/%s did not answer within %s. Work the call started may still be running "+
				"upstream; resume it (e.g. by statement handle) rather than re-running it, ask the tool to "+
				"return sooner, or pass a larger timeout_ms to callTool (capped at %s).", server, tool, timeout, s.maxTimeout))
	case errors.Is(err, mcpclient.ErrResponseTooLarge):
		writeUpstreamCode(w, upstreamFailStatus, "upstream_response_too_large", server, tool,
			fmt.Sprintf("upstream %s/%s returned more than %d bytes in one response; request smaller pages "+
				"(e.g. max_rows/max_bytes).", server, tool, mcpclient.MaxResponseBytes))
	case errors.As(err, &rpcErr):
		writeUpstreamCode(w, upstreamFailStatus, "tool_error", server, tool,
			fmt.Sprintf("upstream %s/%s returned error %d: %s", server, tool, rpcErr.Code, rpcErr.Message))
	default:
		writeUpstreamCode(w, upstreamFailStatus, "upstream_error", server, tool, "upstream: "+err.Error())
	}
}

// writeInProcessErr answers a failed in-process arti tool call. A deadline is
// the same timeout as any upstream's. Otherwise comments.WriteStatus and
// artifacts.WriteStatus are the tables the REST front doors answer from, so a
// conflict stays a 409 here rather than arriving as a generic failure; a target
// the viewer cannot see reaches it as ErrNotFound (the access layer's own
// choice, so an app can't probe), and 403 is reserved for a refusal on a
// document the viewer CAN see. A 500 becomes upstreamFailStatus like any other
// upstream fault.
func (s *Service) writeInProcessErr(w http.ResponseWriter, r *http.Request, server, tool string, timeout time.Duration, err error) {
	if errors.Is(err, context.DeadlineExceeded) || httperr.ClientGone(r, err) {
		s.writeUpstreamErr(w, r, server, tool, timeout, err)
		return
	}
	status, msg, ok := comments.WriteStatus(err)
	code := "comment-error"
	if !ok {
		status, code, msg = artifacts.WriteStatus(err)
	}
	if status == http.StatusInternalServerError {
		status, msg = upstreamFailStatus, "arti: "+msg
	}
	writeUpstreamCode(w, status, code, server, tool, msg)
}

// writeUpstreamCode is writeErr plus a machine-readable `error` code and the
// server/tool the call was for.
func writeUpstreamCode(w http.ResponseWriter, status int, code, server, tool, detail string) {
	writeJSON(w, status, map[string]string{
		"error":  code,
		"server": server,
		"tool":   tool,
		"detail": detail,
		"code":   http.StatusText(status),
	})
}

// Rejection-log sampling: at most rejectLogBurst lines per rejectLogWindow,
// so a brute force against the proxy cannot turn its own rejections into a
// log flood. The observed benign case (a stale browser tab polling on a 60s
// timer) is far below the cap and always logged.
const (
	rejectLogWindow = time.Minute
	rejectLogBurst  = 10
)

type rejectSampler struct {
	mu        sync.Mutex
	windowEnd time.Time
	n         int
}

// allow reports whether a rejection may be logged now. note is true exactly
// once per window, on the first suppressed line, so the log records that
// sampling kicked in without repeating itself.
func (rs *rejectSampler) allow(now time.Time) (ok, note bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if now.After(rs.windowEnd) {
		rs.windowEnd = now.Add(rejectLogWindow)
		rs.n = 0
	}
	rs.n++
	return rs.n <= rejectLogBurst, rs.n == rejectLogBurst+1
}

// logReject records an authentication rejection on the apps proxy. The access
// log line for these requests carries no identity, IP, or user-agent, so
// without this a stream of 401s is unattributable (observed in prod: 61/24h,
// one per minute — an open tab whose 12h app token had expired, but nobody
// could prove that). email may be empty when the token never carried one.
// Never logs the token itself.
func (s *Service) logReject(r *http.Request, status int, reason, email string) {
	ok, note := s.rejects.allow(time.Now())
	if note {
		s.logger.Warn("apps proxy: rejection log rate exceeded, sampling", "window", rejectLogWindow.String())
	}
	if !ok {
		return
	}
	// r.RemoteAddr is the client IP: the globally-registered chimid.RealIP
	// middleware rewrites it from X-Forwarded-For / X-Real-IP before any
	// route (same convention as embed/mint.go's audit line).
	s.logger.Warn("apps proxy: rejected",
		"s", status, "reason", reason, "email", email,
		"ip", r.RemoteAddr, "ua", r.Header.Get("User-Agent"))
}

type badRequest struct{ msg string }

func (e badRequest) Error() string        { return e.msg }
func errBadRequest(msg string) badRequest { return badRequest{msg: msg} }
