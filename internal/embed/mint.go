package embed

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/angellist/arti-oss/internal/auth"
)

// This file implements the per-user popup handshake for user-mode embed
// surfaces. A user-mode embed page is served with NO identity token; its bridge
// opens the mint route in a top-level popup, the viewer approves a consent
// page, arti mints a short-TTL per-user app token, stashes it in a shared
// (cross-replica) store keyed by the app's (surface, state) nonce, and the
// embedded app POLLS for it.
//
// WHY POLL AND NOT postMessage — the embedded app is opaque-origin (the app
// sandbox omits allow-same-origin) AND, inside a host like Front, its popup is
// further sandboxed by the host's plugin iframe, so it can neither submit a
// form (no allow-forms) nor reliably reach window.opener. The handshake is
// therefore built to work from a FULLY sandboxed popup: consent is a plain
// link (a top-level GET navigation, which a sandbox always permits), the mint
// carries identity in a signed challenge rather than a cookie-bearing POST, and
// delivery is a server-side single-use store the app pulls from. Nothing here
// is Front-specific; it works in any embedding container.
//
// SECURITY — the consent click stays the load-bearing control. arti issues a
// consent_challenge (HMAC over email|app|surface|state, short TTL) ONLY while
// rendering the consent page to an authenticated session; the mint completes
// only when that challenge comes back. A drive-by page that window.opens the
// mint URL for an SSO'd victim gets a consent page rendered into the popup, but
// cannot read the challenge (cross-origin popup) and cannot click Continue for
// the user — so it cannot complete a mint. The surface secret is required
// throughout, the completion re-checks the live session matches the challenge's
// email, and every mint is audit-logged. A GET without a valid challenge NEVER
// mints.
//
// Routing (self-hosters must arrange this; see the Embed surfaces doc): the
// mint route (/auth/embed/app-token) must sit BEHIND the auth front door so the
// session cookie is available to render consent; the completion + poll routes
// (/embed/{surface}/token) are PUBLIC + CORS so the sandboxed, opaque-origin app
// can fetch them with no cookie (they authenticate via the surface secret + the
// signed challenge + the app's state nonce).

const (
	mintPath = "/auth/embed/app-token"
	// challengeMACDomain domain-separates the consent-challenge MAC.
	challengeMACDomain = "arti-embed-challenge-v1."
	// challengeTTL bounds the window between rendering consent and clicking
	// Continue.
	challengeTTL = 10 * time.Minute
	// pendingTTL bounds how long a minted token waits in the store for the app
	// to poll it. Short: the app polls immediately after the popup completes.
	pendingTTL = 2 * time.Minute
	// maxStateLen bounds the app-supplied nonce.
	maxStateLen = 256
)

// UserTokenMinter authorizes and mints per-user embed app tokens.
type UserTokenMinter interface {
	// EmbedUserApp verifies email can read the version-pinned APP appID (a
	// UUID) and returns its title + slug. Denials are errors.
	EmbedUserApp(ctx context.Context, appID, email string) (title, slug string, err error)
	// MintEmbedUserToken mints the short-TTL, embed-user-marked app token.
	MintEmbedUserToken(ctx context.Context, appID, email string) (string, error)
}

// MintConfig wires the handshake endpoint's dependencies.
type MintConfig struct {
	// Identify resolves the verified browser identity ("" when unauthenticated).
	Identify func(*http.Request) string
	// Minter authorizes + mints the delivered token.
	Minter UserTokenMinter
	// ChallengeKey signs the consent challenge (HMAC-SHA256, typically the JWT
	// signing key — domain-separated from any other use by the MAC prefix).
	ChallengeKey []byte
	// Store hands the minted token to the app's poll, across replicas.
	Store        PendingTokenStore
	CookieSecure bool
	Logger       *slog.Logger
}

// MountMint registers the popup-handshake mint endpoint (GET only — a GET with
// a valid challenge mints, a GET without one only renders consent). No-op
// unless a user-mode surface + all deps are configured.
func (s *Service) MountMint(r chi.Router, cfg MintConfig) {
	hasUser := false
	for _, surf := range s.surfaces {
		if surf.Identity == "user" {
			hasUser = true
			break
		}
	}
	if !hasUser || cfg.Identify == nil || cfg.Minter == nil || cfg.Store == nil {
		return
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if len(cfg.ChallengeKey) < 16 {
		cfg.Logger.Warn("embed mint endpoint NOT mounted: challenge key too short (set JWT_SIGNING_KEY)")
		return
	}
	s.mint = &cfg
	r.Get(mintPath, s.handleMint)
}

// handlePoll is the PUBLIC, CORS-enabled endpoint the embedded app polls for
// its minted token. Credentials are the surface secret + the app's own (opaque)
// state nonce; the token is single-use (Take deletes it). Mounted by Mount on
// the public group — no cookie, so a fully sandboxed app can fetch it.
func (s *Service) handlePoll(w http.ResponseWriter, r *http.Request) {
	setEmbedCORS(w)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	surf, ok := s.surfaces[chi.URLParam(r, "surface")]
	if !ok || surf.Identity != "user" || s.mint == nil || s.mint.Store == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"unknown surface"}`))
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("auth_secret")), []byte(surf.Secret)) != 1 {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" || len(state) > maxStateLen {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad state"}`))
		return
	}
	tok, err := s.mint.Store.Take(r.Context(), chi.URLParam(r, "surface"), state)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"store error"}`))
		return
	}
	// 200 either way; "ready":false means keep polling (the user hasn't
	// finished consent yet). Only a present token ends the poll.
	if tok == "" {
		_, _ = w.Write([]byte(`{"ready":false}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ready": true, "token": tok})
}

// setEmbedCORS allows the opaque-origin embedded app (origin "null") to fetch
// the poll endpoint. The surface secret + state nonce are the credential (not a
// cookie), so `*` is safe — same rationale as the apps proxy.
func setEmbedCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Max-Age", "600")
}

func (s *Service) handleMint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	surfName := r.URL.Query().Get("surface")
	surf, ok := s.surfaces[surfName]
	if !ok || surf.Identity != "user" {
		http.NotFound(w, r)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("auth_secret")), []byte(surf.Secret)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	appID := r.URL.Query().Get("app")
	state := r.URL.Query().Get("state")
	if appID == "" || state == "" || len(state) > maxStateLen {
		http.Error(w, "missing or bad app/state", http.StatusBadRequest)
		return
	}

	email := s.mint.Identify(r)
	if email == "" {
		// First-party popup with no arti session yet: bounce through the
		// mode-agnostic login (works behind oauth2-proxy AND in oidc mode; both
		// honor a relative return_to) and land back here.
		http.Redirect(w, r, "/auth/login?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	if !auth.IsAllowed(email) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Mint by the version-pinned UUID the served page carries, so the token
	// always matches the page's app_id.
	title, slug, err := s.mint.Minter.EmbedUserApp(r.Context(), appID, email)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// A surface only hands out tokens for apps it may embed. Mirror the doc
	// route: the allowlisted ident may be the slug OR the UUID.
	if !slugAllowed(surf.SlugAllow, slug) && !slugAllowed(surf.SlugAllow, appID) {
		http.Error(w, "app not allowed for this surface", http.StatusForbidden)
		return
	}

	// This GET NEVER mints — it only renders consent. It issues a
	// consent_challenge (proof it rendered to THIS authenticated session) that
	// the consent page's Continue button POSTs to the public completion
	// endpoint. Minting is deliberately NOT reachable by a GET: a GET that
	// minted would be triggerable by link prefetch / preview / scanners,
	// silently bypassing the human click. _ = slug (checked above).
	_ = slug
	challenge := s.signChallenge(challengePayload{
		Email: email, App: appID, Surface: surfName, State: state,
		Exp: time.Now().Add(challengeTTL).Unix(),
	})
	s.writeConsentPage(w, surfName, surf, appID, state, email, title, challenge)
}

// handleComplete is the PUBLIC, CORS-enabled mint step the consent page's
// Continue button POSTs to. It is a POST (never a GET) so it can't be triggered
// by prefetch/preview — only a real click runs the fetch. It carries NO cookie;
// identity comes from the signed consent_challenge, which arti issued only when
// it rendered the consent page to an authenticated session and which an
// attacker's drive-by popup can't read (cross-origin) or click for the user.
func (s *Service) handleComplete(w http.ResponseWriter, r *http.Request) {
	setEmbedCORS(w)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	surfName := chi.URLParam(r, "surface")
	surf, ok := s.surfaces[surfName]
	if !ok || surf.Identity != "user" || s.mint == nil || s.mint.Store == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"unknown surface"}`))
		return
	}
	_ = r.ParseForm()
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("auth_secret")), []byte(surf.Secret)) != 1 {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
		return
	}
	p, ok := s.parseChallenge(r.PostFormValue("consent_challenge"))
	if !ok || p.Surface != surfName {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"invalid or expired consent"}`))
		return
	}
	if !auth.IsAllowed(p.Email) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
		return
	}
	// Re-check read access + surface allowlist at completion (defense in depth;
	// a public endpoint shouldn't lean only on the challenge's issue-time gate).
	_, slug, err := s.mint.Minter.EmbedUserApp(r.Context(), p.App, p.Email)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
		return
	}
	if !slugAllowed(surf.SlugAllow, slug) && !slugAllowed(surf.SlugAllow, p.App) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"app not allowed for this surface"}`))
		return
	}
	tok, err := s.mint.Minter.MintEmbedUserToken(r.Context(), p.App, p.Email)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"mint failed"}`))
		return
	}
	if err := s.mint.Store.Put(r.Context(), surfName, p.State, tok, p.Email, pendingTTL); err != nil {
		s.mint.Logger.Error("embed mint: store put failed", "err", err, "surface", surfName)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"store error"}`))
		return
	}
	s.mint.Logger.Info("embed-user app token minted",
		"email", p.Email, "app", p.App, "slug", slug, "surface", surfName, "ip", r.RemoteAddr)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// ─── consent challenge (proof the human clicked) ─────────────────────

type challengePayload struct {
	Email   string `json:"e"`
	App     string `json:"a"`
	Surface string `json:"s"`
	State   string `json:"t"`
	Exp     int64  `json:"x"`
}

func (s *Service) signChallenge(p challengePayload) string {
	body, _ := json.Marshal(p)
	b := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, s.mint.ChallengeKey)
	mac.Write([]byte(challengeMACDomain + b))
	return b + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// parseChallenge verifies the MAC + expiry and returns the payload. The caller
// checks the payload fields (surface match, etc.) against the request.
func (s *Service) parseChallenge(v string) (challengePayload, bool) {
	dot := strings.LastIndexByte(v, '.')
	if dot < 0 {
		return challengePayload{}, false
	}
	b, sig := v[:dot], v[dot+1:]
	mac := hmac.New(sha256.New, s.mint.ChallengeKey)
	mac.Write([]byte(challengeMACDomain + b))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return challengePayload{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(b)
	if err != nil {
		return challengePayload{}, false
	}
	var p challengePayload
	if err := json.Unmarshal(raw, &p); err != nil || time.Now().Unix() > p.Exp {
		return challengePayload{}, false
	}
	return p, true
}

// ─── pages (link-based so they work in a fully sandboxed popup) ──────

// setMintPageHeaders hardens both interactive pages: top-level only (refuse all
// framing — anti-clickjack) and never cached.
func setMintPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store")
}

// writeConsentPage renders the human gate. Continue is a BUTTON that fetches
// the completion endpoint (a POST) — not a link — so the mint can't be
// triggered by link prefetch/preview, only by a real click. This needs
// allow-scripts (which the embedding host grants, since the app itself runs
// JS) but NOT allow-forms (which a sandboxed popup like Front's does not), so
// it works where a form submit doesn't. On success the app's poll picks up the
// token; this window just closes.
func (s *Service) writeConsentPage(w http.ResponseWriter, surfName string, surf Surface, appID, state, email, title, challenge string) {
	setMintPageHeaders(w)
	if title == "" {
		title = "this app"
	}
	// All values are JSON-encoded into a JS object literal; json.Marshal
	// escapes </script> and quotes so nothing breaks out.
	cfg, _ := json.Marshal(map[string]string{
		"complete":  "/embed/" + surfName + "/token",
		"secret":    surf.Secret,
		"challenge": challenge,
	})
	esc := html.EscapeString
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>arti — connect</title>
<style>body{font-family:system-ui;max-width:26rem;margin:18vh auto 0;padding:0 24px;color:#333;line-height:1.5}
h1{font-size:1.15em;font-weight:600}p{color:#555;font-size:.92em}
button{font:inherit;padding:8px 18px;border-radius:6px;border:1px solid #ccc;background:#fff;cursor:pointer}
button.go{background:#2563eb;border-color:#2563eb;color:#fff}#s{color:#888}</style></head><body>
<h1>Continue as %s?</h1>
<p>The embedded app <b>%s</b> will be able to call its allowlisted tools <b>as you</b> while this connection lasts. Only continue if you opened it yourself.</p>
<p><button id="go" class="go">Continue</button> <button id="no">Cancel</button></p>
<p id="s"></p>
<script>(function(){
  var C=%s, go=document.getElementById("go"), no=document.getElementById("no"), s=document.getElementById("s");
  no.onclick=function(){ try{window.close();}catch(e){} s.textContent="You can close this window."; };
  go.onclick=function(){
    go.disabled=true; s.textContent="Connecting…";
    var b=new URLSearchParams(); b.set("auth_secret",C.secret); b.set("consent_challenge",C.challenge);
    fetch(C.complete,{method:"POST",headers:{"Content-Type":"application/x-www-form-urlencoded"},body:b.toString(),credentials:"omit"})
      .then(function(r){ return r.json().catch(function(){return {};}).then(function(j){return {ok:r.ok,j:j};}); })
      .then(function(res){
        if(res.ok && res.j && res.j.ok){ s.textContent="Connected ✓ — you can close this window."; setTimeout(function(){try{window.close();}catch(e){}},700); }
        else { go.disabled=false; s.textContent="Could not connect: "+((res.j&&res.j.error)||"error")+". Try again."; }
      })
      .catch(function(){ go.disabled=false; s.textContent="Network error — try again."; });
  };
})();</script>
</body></html>`,
		esc(email), esc(title), string(cfg))
}
