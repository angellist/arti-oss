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
	// origins is the surface's configured embedder-origin list, handed to the
	// app bridge so it can postMessage a freshly minted token ONLY to a
	// declared embedding host (the zero-click relay contract).
	ServeForEmbedUser(w http.ResponseWriter, r *http.Request, surface, ident string, ver *int32, frameAncestors string, origins []string) bool
	// ServeForEmbedPinned renders the artifact as the real signed-in viewer
	// (viewer mode), refusing when the resolved artifact is not the one the
	// viewer's token was minted for.
	// Returns (served, pinMismatch); pinMismatch distinguishes "the token names a
	// different artifact than this request resolves to" from an access denial, so
	// the caller can re-prompt instead of showing a dead placeholder.
	ServeForEmbedPinned(w http.ResponseWriter, r *http.Request, surface, ident string, ver *int32, viewer, frameAncestors, pinnedArtifactID string) (served, pinMismatch bool)
	// ServeEmbedFile serves one file from a packaged artifact as `caller`.
	ServeEmbedFile(w http.ResponseWriter, r *http.Request, artifactID, filePath, caller, frameAncestors, filesRoot string) bool
	// ServeEmbedFilePublic serves one file from a packaged artifact with no
	// caller check — for user-mode surfaces, where the verified files token
	// (scoped to exactly this artifact) is the credential.
	ServeEmbedFilePublic(w http.ResponseWriter, r *http.Request, artifactID, filePath, frameAncestors, filesRoot string) bool
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

// ViewerTokenVerifier verifies the viewer-mode document token. Kept separate
// from TokenVerifier so the viewer feature is additive: a TokenVerifier that
// does not implement it simply never authenticates a viewer, and the gate page
// is served instead of the document.
type ViewerTokenVerifier interface {
	// VerifyEmbedViewerToken → the viewer email + artifact id an embed-viewer
	// token authorizes. It MUST reject a token lacking the embed-viewer scope.
	VerifyEmbedViewerToken(tok string) (email, artifactID string, err error)
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
	// (/auth/embed/app-token), so tool calls run as the real viewer; "viewer"
	// gates the DOCUMENT RENDER itself on the real viewer's own ACL — nothing is
	// served until the viewer completes the same popup handshake, and then
	// checkAccess decides. "viewer" is the only mode in which the URL is not a
	// credential, and therefore the only mode in which Secret may be empty.
	Identity string `json:"identity"`
	// Email is the identity artifacts resolve as — the real read-scope gate.
	// Required in service mode; rejected in user mode (it would never be used,
	// and a configured-but-unused service identity is a confusion in waiting).
	Email string `json:"email"`
	// SlugAllow lists patterns (exact or "*" glob) the requested slug must match.
	// ["*"] = any artifact the Email can read (service mode) or any artifact the
	// signed-in viewer can read (viewer mode).
	SlugAllow []string `json:"slug_allow"`
	// Shell, when set, serves a client adapter at /embed/{surface}/shell. "" =
	// none (the embedder builds the ?slug= URL itself). "front" = Front SDK loader.
	Shell string `json:"shell"`
	// ShellSlug (with a shell) is how the shell builds a slug from the host id,
	// e.g. "deployment-ctx-{id}". Not a gate — SlugAllow is the gate.
	ShellSlug string `json:"shell_slug"`
	// ShellQuery (with a shell) is an extra query-fragment template appended to
	// the doc-route fetch, {id}-substituted (e.g. "ctx=deploy-ctx-{id}"). Lets
	// shell_slug be a static app slug while the per-thread id rides in the query.
	// "" = none.
	ShellQuery string `json:"shell_query"`
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
		// In viewer mode nothing is served until the viewer authenticates, so the
		// secret protects nothing and is optional. It stays REQUIRED for service
		// and user mode, where it is the entire access gate.
		if s.Secret == "" && s.Identity != "viewer" {
			return nil, fmt.Errorf("embed surface %q: secret required", name)
		}
		switch {
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
		case "viewer":
			if s.Email != "" {
				return nil, fmt.Errorf("embed surface %q: email must be empty with identity \"viewer\" (the document is served as the real viewer)", name)
			}
			// A shell resolves a slug client-side and re-fetches the doc route; the
			// viewer gate page does its own reload with a token. Combining them has
			// no defined meaning, so reject it rather than serve something odd.
			if s.Shell != "" {
				return nil, fmt.Errorf("embed surface %q: shell is not supported with identity \"viewer\"", name)
			}
		default:
			return nil, fmt.Errorf("embed surface %q: unknown identity %q (want \"service\", \"user\" or \"viewer\")", name, s.Identity)
		}
		if s.Shell != "" && s.Shell != "front" {
			return nil, fmt.Errorf("embed surface %q: unknown shell %q", name, s.Shell)
		}
		// A shell ALWAYS builds a slug from shell_slug and fetches the doc route with it;
		// an empty slug re-serves the shell loader (handleDoc), so the app never loads.
		// Require it non-empty even though {id} may live in shell_query instead.
		if s.Shell != "" && s.ShellSlug == "" {
			return nil, fmt.Errorf("embed surface %q: shell_slug required with a shell (an empty slug re-serves the shell loader instead of the artifact)", name)
		}
		if s.Shell != "" && !strings.Contains(s.ShellSlug, "{id}") && !strings.Contains(s.ShellQuery, "{id}") {
			return nil, fmt.Errorf("embed surface %q: shell_slug or shell_query must contain {id}", name)
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
	if !secretOK(surf, r) {
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
	if surf.Identity == "viewer" {
		// Viewer mode: the render is gated on the REAL viewer's ACL. With no
		// token we serve only the gate page, which runs the consent handshake and
		// reloads itself with ?t=. Nothing about the artifact — not its title, not
		// whether it exists — reaches an unauthenticated caller.
		email, artifactID, ok := s.viewerFromToken(r)
		if !ok {
			s.writeGatePage(w, chi.URLParam(r, "surface"), surf, slug, fa)
			return
		}
		// The token pins one artifact. Serving as `email` re-runs checkAccess, and
		// the pin stops a token for artifact A serving artifact B.
		//
		// A pin MISMATCH re-serves the gate rather than the placeholder: the common
		// cause is a new version published between consent and reload, and one more
		// prompt mints a token for the current row and self-heals. An ACL denial
		// still falls through to the placeholder, so the two are not conflated.
		w.Header().Set("Referrer-Policy", "no-referrer")
		var mismatch bool
		served, mismatch = s.art.ServeForEmbedPinned(w, r, chi.URLParam(r, "surface"), slug, parseVersion(r), email, fa, artifactID)
		if mismatch {
			s.writeGatePage(w, chi.URLParam(r, "surface"), surf, slug, fa)
			return
		}
	} else if surf.Identity == "user" {
		// User mode: the secret + slug_allow checks above are the whole gate for
		// the static content (any secret-holder can read the page's HTML/JS —
		// see the package doc); the viewer's identity only enters via the
		// /auth/embed/app-token popup handshake, per tool call.
		served = s.art.ServeForEmbedUser(w, r, chi.URLParam(r, "surface"), slug, parseVersion(r), fa, surf.Origin)
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
		if !s.art.ServeEmbedFilePublic(w, r, aid, chi.URLParam(r, "*"), frameAncestors(surf.Origin), "/embed/"+chi.URLParam(r, "surface")+"/_files/"+tok+"/") {
			http.NotFound(w, r)
		}
		return
	}
	email, aid, err := s.tokens.VerifyEmbedToken(tok)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !s.art.ServeEmbedFile(w, r, aid, chi.URLParam(r, "*"), email, frameAncestors(surf.Origin), "/embed/"+chi.URLParam(r, "surface")+"/_files/"+tok+"/") {
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

// viewerFromToken verifies the ?t= embed-viewer token and returns the viewer
// email + the artifact UUID the token is pinned to. ok=false whenever the token
// is absent, malformed, expired, or not an embed-viewer token — every one of
// those falls through to the gate page rather than to an error, so a stale
// token simply re-prompts.
func (s *Service) viewerFromToken(r *http.Request) (email, artifactID string, ok bool) {
	tok := strings.TrimSpace(r.URL.Query().Get("t"))
	if tok == "" || s.tokens == nil {
		return "", "", false
	}
	vv, isViewer := s.tokens.(ViewerTokenVerifier)
	if !isViewer {
		return "", "", false
	}
	email, artifactID, err := vv.VerifyEmbedViewerToken(tok)
	if err != nil || email == "" || artifactID == "" {
		return "", "", false
	}
	return email, artifactID, true
}

// secretOK constant-time compares the surface secret against ?auth_secret=.
func secretOK(surf Surface, r *http.Request) bool {
	return secretMatches(surf, r.URL.Query().Get("auth_secret"))
}

// secretMatches reports whether a presented secret satisfies the surface.
//
// THE INVARIANT: in viewer mode the comparison never executes. The render is
// gated on the viewer's own ACL, so the secret protects nothing there, and any
// comparison at all creates a failure mode — a surface migrated off service mode
// breaks either its old `?auth_secret=` URLs (if it dropped the secret) or its
// clean ones (if it kept it). Skipping the comparison outright is the only rule
// under which both keep working.
//
// Every gate on the surface must route through here rather than compare inline:
// the doc route and the three handshake routes (mint, complete, poll) all see the
// same forwarded value, and one of them disagreeing 403s the Sign in button on
// URLs the doc route accepts.
func secretMatches(surf Surface, presented string) bool {
	if surf.Identity == "viewer" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(surf.Secret)) == 1
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
// gatePageHTML is the viewer-mode sign-in gate. It is deliberately STATIC —
// every value it needs (surface, slug, version, stale auth_secret) already sits
// in its own URL, so nothing about the requested artifact is interpolated into
// the body. That is what keeps the gate from becoming an existence oracle: the
// bytes are identical for a readable slug, an unreadable slug and a slug that
// was never created.
//
// The handshake it drives is the one built for user-mode APP surfaces: open the
// mint route in a top-level popup (first-party on arti's origin, so the session
// cookie flows and /auth/login can redirect), poll the public token endpoint,
// then reload THIS url with ?t=<token>. It uses fetch + a popup and never a form
// submit, because the effective sandbox inside a Notion embed omits allow-forms.
const gatePageHTML = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
 body{margin:0;font:14px/1.6 -apple-system,system-ui,sans-serif;color:#333;
      display:flex;align-items:center;justify-content:center;height:100vh}
 .c{text-align:center;max-width:22rem;padding:20px}
 button{font:inherit;padding:8px 18px;border:1px solid #c8c8c8;border-radius:6px;
        background:#fafafa;cursor:pointer}
 button:hover{background:#f0f0f0}
 p{color:#777;margin:10px 0 18px}
 .e{color:#b04141;min-height:1.4em;font-size:13px;margin-top:14px}
 .f{font-size:13px;margin-top:12px}
 .f a{color:#2563eb}
</style></head><body><div class="c">
 <p>Sign in to view this document.</p>
 <button id="b">Sign in</button>
 <div class="f" id="f"></div>
 <div class="e" id="e"></div>
</div><script>
(function(){
 var q=new URLSearchParams(location.search);
 var surface=location.pathname.replace(/^.*\/embed\//,"").split("/")[0];
 var secret=q.get("auth_secret")||"";
 var state=Math.random().toString(36).slice(2)+Date.now().toString(36);
 var btn=document.getElementById("b"),err=document.getElementById("e"),
     fb=document.getElementById("f"),timer=null;
 function qs(o){var p=new URLSearchParams();for(var k in o){if(o[k])p.set(k,o[k]);}return p.toString();}
 function stop(m){if(timer){clearInterval(timer);timer=null;}btn.disabled=false;err.textContent=m||"";}
 function done(tok){
   if(timer){clearInterval(timer);timer=null;}
   var u=new URL(location.href);u.searchParams.set("t",tok);location.replace(u.toString());
 }
 btn.onclick=function(){
   err.textContent="";fb.innerHTML="";btn.disabled=true;
   var mint="/auth/embed/app-token?"+qs({surface:surface,slug:q.get("slug"),
     version:q.get("version"),state:state,auth_secret:secret});
   var w=null;
   try{ w=window.open(mint,"_blank","width=520,height=680"); }catch(e){}
   // A null return does NOT mean nothing opened. An Electron host (the Notion
   // desktop app) intercepts window.open, hands the URL to the OS browser, and
   // returns null to this page. The consent flow then completes out there and the
   // token lands in the pending store under our own state nonce — so we must keep
   // polling regardless, or the desktop app can never finish a sign-in that has
   // already succeeded. Only surface a manual link, never an error.
   if(!w){ fb.innerHTML=""; var a=document.createElement("a");
           a.href=mint; a.target="_blank"; a.rel="noopener";
           a.textContent="If no sign-in window opened, continue here";
           fb.appendChild(a); }
   var tries=0;
   timer=setInterval(function(){
     if(++tries>150){stop("Sign-in did not complete. Try again.");return;}
     fetch("/embed/"+encodeURIComponent(surface)+"/token?"+qs({state:state,auth_secret:secret}),
           {cache:"no-store"})
       .then(function(r){return r.ok?r.json():null;})
       .then(function(j){if(j&&j.token){done(j.token);}})
       .catch(function(){});
   },2000);
 };
})();
</script></body></html>`

// writeGatePage serves the viewer-mode sign-in gate with the app-grade sandbox,
// which is what permits the consent popup, and the surface's frame-ancestors.
// slug is accepted for symmetry with the other writers but deliberately unused:
// see gatePageHTML.
func (s *Service) writeGatePage(w http.ResponseWriter, surface string, surf Surface, slug, fa string) {
	_ = surface
	_ = surf
	_ = slug
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy",
		"sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox "+
			"allow-top-navigation-by-user-activation allow-downloads; frame-ancestors "+fa)
	w.Header().Del("X-Frame-Options")
	w.Header().Set("Cache-Control", "no-store")
	// The reload this page performs puts a token in the URL, so keep the URL out
	// of any outbound Referer. The browser default already trims to origin
	// cross-site; this makes it explicit on the one page that mints the URL.
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write([]byte(gatePageHTML))
}

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
