// Package embed serves arti artifacts into external (cross-site) iframes.
//
// It exists for embedders that can't carry the arti_session cookie — a
// third-party iframe (e.g. app.frontapp.com), where the SameSite cookie is
// never sent and an SSO redirect can't render (Google's sign-in sends
// X-Frame-Options: DENY). So these routes live on the PUBLIC ingress (no
// oauth2-proxy) and authenticate via a per-surface shared secret, like the
// comments embed and apps proxy.
//
// A "surface" is a named external place allowed to embed arti content; surfaces
// are configured (ARTI_EMBED_SURFACES) and the routes are generic — Front is one
// surface, not a special case. See web/docs/architecture/front-embed.md.
package embed

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// ArtServer is the slice of the artifacts service the embed routes need. Kept
// as an interface so this package depends on a behavior, not the whole service.
type ArtServer interface {
	// ServeForEmbed renders the artifact (slug or UUID) full-page as `caller`,
	// framable by frameAncestors. surface names the embed surface (for building
	// the sibling-files path). Returns false (writing nothing) on miss.
	ServeForEmbed(w http.ResponseWriter, r *http.Request, surface, ident string, ver *int32, caller, frameAncestors string) bool
	// ServeForEmbedUser is ServeForEmbed for a user-mode surface: no serve-time
	// caller — the surface secret (validated by the route) is the whole gate for
	// the static content; identity arrives later via the popup-minted app token.
	ServeForEmbedUser(w http.ResponseWriter, r *http.Request, surface, ident string, ver *int32, frameAncestors string) bool
	// ServeEmbedFile serves one file from a packaged artifact as `caller`.
	ServeEmbedFile(w http.ResponseWriter, r *http.Request, artifactID, filePath, caller, frameAncestors string) bool
	// ServeEmbedFilePublic serves one file from a packaged artifact with no
	// caller check — for user-mode surfaces, where the verified files token
	// (scoped to exactly this artifact) is the credential.
	ServeEmbedFilePublic(w http.ResponseWriter, r *http.Request, artifactID, filePath, frameAncestors string) bool
}

// TokenVerifier verifies the scoped token in the sibling-files path → what it
// authorizes. Satisfied by apps.Service.
type TokenVerifier interface {
	// VerifyEmbedToken → the email + artifact id a service-mode files token
	// authorizes.
	VerifyEmbedToken(tok string) (email, artifactID string, err error)
	// VerifyEmbedFilesToken → the artifact id a user-mode (email-less) files
	// token authorizes.
	VerifyEmbedFilesToken(tok string) (artifactID string, err error)
}

// Surface is one configured embedder.
type Surface struct {
	// Secret is the shared secret the embedder appends as ?auth_secret=; compared
	// constant-time. For Front this is the value Front mints in the app settings.
	Secret string `json:"secret"`
	// Origin lists the hosts allowed to frame this surface (frame-ancestors).
	// Accepts a single string (the original config shape) or an array — a
	// nested topology (e.g. Front ▸ couch ▸ arti) must enumerate EVERY
	// ancestor, since browsers check frame-ancestors against the full chain.
	Origin OriginList `json:"origin"`
	// Identity picks who artifacts are served as: "service" (default) resolves
	// everything as the fixed Email below; "user" serves the static content on
	// the secret alone and defers identity to the per-user popup handshake
	// (/auth/embed/app-token), so tool calls run as the real viewer.
	Identity string `json:"identity"`
	// Email is the identity artifacts resolve as — the real read-scope gate.
	// Required in service mode; rejected in user mode (it would never be used,
	// and a configured-but-unused service identity is a confusion in waiting).
	Email string `json:"email"`
	// SlugAllow lists patterns (exact or "*" glob) the requested slug must match.
	// ["*"] = any artifact the Email can read.
	SlugAllow []string `json:"slug_allow"`
	// Shell, when set, serves a client adapter at /embed/{surface}/shell. "" =
	// none (the embedder builds the ?slug= URL itself). "front" = Front SDK loader.
	Shell string `json:"shell"`
	// ShellSlug (with a shell) is how the shell builds a slug from the host id,
	// e.g. "deployment-ctx-{id}". Not a gate — SlugAllow is the gate.
	ShellSlug string `json:"shell_slug"`
}

// Service serves the configured surfaces.
type Service struct {
	surfaces map[string]Surface
	art      ArtServer
	tokens   TokenVerifier
	mint     *MintConfig // set by MountMint; nil when no user-mode surface
}

func New(surfaces map[string]Surface, art ArtServer, tokens TokenVerifier) *Service {
	return &Service{surfaces: surfaces, art: art, tokens: tokens}
}

// ParseSurfaces parses + validates the ARTI_EMBED_SURFACES JSON map. Empty/blank
// → no surfaces (feature off). It fails fast on a misconfigured surface so a
// half-formed surface never serves silently.
func ParseSurfaces(jsonStr string) (map[string]Surface, error) {
	jsonStr = strings.TrimSpace(jsonStr)
	if jsonStr == "" {
		return nil, nil
	}
	var m map[string]Surface
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return nil, fmt.Errorf("parse ARTI_EMBED_SURFACES: %w", err)
	}
	for name, s := range m {
		switch {
		case s.Secret == "":
			return nil, fmt.Errorf("embed surface %q: secret required", name)
		case len(s.Origin) == 0:
			return nil, fmt.Errorf("embed surface %q: origin required", name)
		case len(s.SlugAllow) == 0:
			return nil, fmt.Errorf("embed surface %q: slug_allow required (use [\"*\"] for any)", name)
		}
		for _, o := range s.Origin {
			if strings.TrimSpace(o) == "" {
				return nil, fmt.Errorf("embed surface %q: origin entries must be non-empty", name)
			}
		}
		// Identity-mode validation fails fast in BOTH directions, matching the
		// existing style: a half-formed surface never serves silently.
		switch s.Identity {
		case "", "service":
			if s.Email == "" {
				return nil, fmt.Errorf("embed surface %q: email required (service identity)", name)
			}
		case "user":
			if s.Email != "" {
				return nil, fmt.Errorf("embed surface %q: email must be empty with identity \"user\" (tool calls run as the real viewer)", name)
			}
		default:
			return nil, fmt.Errorf("embed surface %q: unknown identity %q (want \"service\" or \"user\")", name, s.Identity)
		}
		if s.Shell != "" && s.Shell != "front" {
			return nil, fmt.Errorf("embed surface %q: unknown shell %q", name, s.Shell)
		}
		if s.Shell != "" && !strings.Contains(s.ShellSlug, "{id}") {
			return nil, fmt.Errorf("embed surface %q: shell_slug must contain {id}", name)
		}
	}
	return m, nil
}

// OriginList is a surface's frame-ancestors allowlist. It unmarshals from a
// single JSON string (the original one-origin config shape) or an array, so
// existing surface configs keep working unchanged.
type OriginList []string

func (o *OriginList) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*o = OriginList{s}
		return nil
	}
	var l []string
	if err := json.Unmarshal(b, &l); err != nil {
		return err
	}
	*o = l
	return nil
}

// Mount registers the embed routes on the PUBLIC group. No-op when no surfaces
// are configured.
func (s *Service) Mount(r chi.Router) {
	if len(s.surfaces) == 0 {
		return
	}
	r.Get("/embed/{surface}", s.handleDoc)
	r.Get("/embed/{surface}/shell", s.handleShell)
	r.Get("/embed/{surface}/_files/{token}/*", s.handleFile)
	// Public user-mode handshake endpoints (no-op for service-mode surfaces;
	// guarded inside the handlers). Same path, split by method: POST completes
	// the mint (the consent page's Continue fetch), GET polls for the token.
	// CORS so the opaque-origin embedded app / sandboxed popup can call them.
	r.Options("/embed/{surface}/token", func(w http.ResponseWriter, _ *http.Request) {
		setEmbedCORS(w)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
	})
	r.Post("/embed/{surface}/token", s.handleComplete)
	r.Get("/embed/{surface}/token", s.handlePoll)
}

// handleDoc serves the artifact named by ?slug= (+ optional ?version=) for a
// surface, after validating the shared secret and the slug allowlist.
func (s *Service) handleDoc(w http.ResponseWriter, r *http.Request) {
	surf, ok := s.surfaces[chi.URLParam(r, "surface")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	// A shell surface hit at its bare path with no ?slug= is the shell's front
	// door: serve the client adapter (it resolves the host id → slug and
	// re-fetches THIS route with ?slug=). Keeps the configured surface URL clean
	// — /embed/front-deployment-context — with no /shell suffix needed. Like
	// handleShell, the loader itself isn't secret-gated; the secret gates the
	// doc fetch below.
	if slug == "" && surf.Shell != "" {
		s.serveShell(w, chi.URLParam(r, "surface"), surf)
		return
	}
	if !secretOK(surf.Secret, r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if slug == "" {
		http.Error(w, "missing slug", http.StatusBadRequest)
		return
	}
	if !slugAllowed(surf.SlugAllow, slug) {
		http.Error(w, "slug not allowed for this surface", http.StatusForbidden)
		return
	}
	// Deployment docs change; never let a browser/intermediary reuse a cached
	// GET for the same slug+secret URL (matches the shell + placeholder).
	w.Header().Set("Cache-Control", "no-store")
	fa := frameAncestors(surf.Origin)
	served := false
	if surf.Identity == "user" {
		// User mode: the secret + slug_allow checks above are the whole gate for
		// the static content (any secret-holder can read the page's HTML/JS —
		// see the package doc); the viewer's identity only enters via the
		// /auth/embed/app-token popup handshake, per tool call.
		served = s.art.ServeForEmbedUser(w, r, chi.URLParam(r, "surface"), slug, parseVersion(r), fa)
	} else {
		served = s.art.ServeForEmbed(w, r, chi.URLParam(r, "surface"), slug, parseVersion(r), surf.Email, fa)
	}
	if !served {
		writePlaceholder(w, surf.Origin, slug)
	}
}

// handleFile serves a packaged artifact's sibling file. The token in the path
// (minted on the doc request, after the secret check) is the credential.
func (s *Service) handleFile(w http.ResponseWriter, r *http.Request) {
	surf, ok := s.surfaces[chi.URLParam(r, "surface")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	tok := chi.URLParam(r, "token")
	if surf.Identity == "user" {
		// User-mode doc pages carry an email-less files token scoped to the
		// artifact they were served for — same access model as the doc itself.
		aid, err := s.tokens.VerifyEmbedFilesToken(tok)
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !s.art.ServeEmbedFilePublic(w, r, aid, chi.URLParam(r, "*"), frameAncestors(surf.Origin)) {
			http.NotFound(w, r)
		}
		return
	}
	email, aid, err := s.tokens.VerifyEmbedToken(tok)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !s.art.ServeEmbedFile(w, r, aid, chi.URLParam(r, "*"), email, frameAncestors(surf.Origin)) {
		http.NotFound(w, r)
	}
}

// handleShell serves a surface's client adapter at the explicit /shell path
// (kept for back-compat; the bare /embed/{surface} path also serves it now).
func (s *Service) handleShell(w http.ResponseWriter, r *http.Request) {
	surf, ok := s.surfaces[chi.URLParam(r, "surface")]
	if !ok || surf.Shell == "" {
		http.NotFound(w, r)
		return
	}
	s.serveShell(w, chi.URLParam(r, "surface"), surf)
}

// serveShell writes a surface's client adapter. Only "front" is implemented;
// ParseSurfaces rejects any other Shell value, so default is unreachable.
func (s *Service) serveShell(w http.ResponseWriter, surface string, surf Surface) {
	switch surf.Shell {
	case "front":
		serveFrontShell(w, surface, surf)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// secretOK constant-time compares the surface secret against ?auth_secret=.
func secretOK(secret string, r *http.Request) bool {
	got := r.URL.Query().Get("auth_secret")
	return subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
}

// slugAllowed reports whether slug matches any allowlist pattern (exact or "*"
// glob). Slugs contain no "/", so path.Match's wildcard semantics are exact.
func slugAllowed(patterns []string, slug string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, slug); err == nil && ok {
			return true
		}
	}
	return false
}

// frameAncestors is the CSP source list for a surface: 'self' (the same-origin
// shell topology) plus every configured surface origin (the direct-embed and
// nested topologies — browsers match frame-ancestors against ALL ancestors).
func frameAncestors(origins OriginList) string {
	if len(origins) == 0 {
		return "'self'"
	}
	return "'self' " + strings.Join(origins, " ")
}

func parseVersion(r *http.Request) *int32 {
	v := strings.TrimSpace(r.URL.Query().Get("version"))
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	x := int32(n)
	return &x
}

// writePlaceholder renders the "nothing here yet" panel when an artifact doesn't
// resolve, so the embed is never broken during rollout. Framable by the surface.
func writePlaceholder(w http.ResponseWriter, origins OriginList, slug string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "frame-ancestors "+frameAncestors(origins))
	w.Header().Del("X-Frame-Options")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8">`+
		`<style>body{margin:0;font:13px/1.5 -apple-system,system-ui,sans-serif;color:#666;padding:24px}`+
		`code{font:12px ui-monospace,monospace;color:#999}</style></head><body>`+
		`<p>Nothing to show here yet.</p><p><code>%s</code></p></body></html>`,
		html.EscapeString(slug))
}
