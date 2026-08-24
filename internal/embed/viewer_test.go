package embed

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// viewerSurfaces mirrors testSurfaces but adds the two viewer-mode shapes the
// feature introduces: one with no secret at all (the target state) and one that
// still carries a stale secret (a surface migrated from service mode).
func viewerSurfaces() map[string]Surface {
	return map[string]Surface{
		"v":       {Origin: OriginList{"https://www.notion.so"}, SlugAllow: []string{"*"}, Identity: "viewer"},
		"vsec":    {Secret: "OLD", Origin: OriginList{"https://www.notion.so"}, SlugAllow: []string{"*"}, Identity: "viewer"},
		"vnarrow": {Origin: OriginList{"https://www.notion.so"}, SlugAllow: []string{"only-this"}, Identity: "viewer"},
		"svc":     {Secret: "S", Origin: OriginList{"https://dash.x"}, Email: "svc@x.com", SlugAllow: []string{"*"}},
	}
}

func viewerRouter(art ArtServer, tok TokenVerifier) chi.Router {
	r := chi.NewRouter()
	New(viewerSurfaces(), art, tok).Mount(r)
	return r
}

func get(r chi.Router, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// ── test-plan item 1: validation ─────────────────────────────────────────

func TestParseSurfaces_ViewerModeValidation(t *testing.T) {
	cases := []struct {
		name, json string
		wantErr    string
	}{
		{"viewer needs no secret",
			`{"v":{"origin":"https://x","slug_allow":["*"],"identity":"viewer"}}`, ""},
		{"viewer may keep a stale secret",
			`{"v":{"secret":"S","origin":"https://x","slug_allow":["*"],"identity":"viewer"}}`, ""},
		{"viewer rejects an email",
			`{"v":{"origin":"https://x","slug_allow":["*"],"identity":"viewer","email":"a@b.c"}}`,
			"email must be empty"},
		{"viewer rejects a shell",
			`{"v":{"origin":"https://x","slug_allow":["*"],"identity":"viewer","shell":"front","shell_slug":"a-{id}"}}`,
			"shell is not supported"},
		// The regression that matters most: secret stays mandatory everywhere else.
		{"service still requires a secret",
			`{"s":{"origin":"https://x","slug_allow":["*"],"email":"a@b.c"}}`, "secret required"},
		{"user still requires a secret",
			`{"u":{"origin":"https://x","slug_allow":["*"],"identity":"user"}}`, "secret required"},
		{"unknown identity is still rejected",
			`{"z":{"secret":"S","origin":"https://x","slug_allow":["*"],"identity":"wat"}}`, "unknown identity"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseSurfaces(c.json)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

// ── test-plan item 2: no token serves the gate page, never content ────────

func TestViewerMode_NoTokenServesGateNotContent(t *testing.T) {
	art := &fakeArt{serveOK: true}
	rec := get(viewerRouter(art, viewerTok{}), "/embed/v?slug=secret-doc")

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Sign in to view") {
		t.Fatalf("want the gate page, got: %s", body[:min(200, len(body))])
	}
	if strings.Contains(body, "SERVED") {
		t.Fatal("artifact content was served without a token")
	}
	// The artifact layer must not even be consulted — that is what guarantees no
	// existence signal and no read.
	if art.gotIdent != "" {
		t.Fatalf("artifact layer was consulted pre-auth with ident %q", art.gotIdent)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "allow-popups") {
		t.Fatalf("gate page needs allow-popups for the consent popup, got %q", csp)
	}
	if !strings.Contains(csp, "frame-ancestors 'self' https://www.notion.so") {
		t.Fatalf("gate page must carry the surface frame-ancestors, got %q", csp)
	}
	if rec.Header().Get("X-Frame-Options") != "" {
		t.Fatal("X-Frame-Options must be dropped so frame-ancestors is authoritative")
	}
}

// ── test-plan item 3: the gate page is not an existence oracle ────────────

func TestViewerMode_GateBodyIdenticalAcrossSlugs(t *testing.T) {
	r := viewerRouter(&fakeArt{serveOK: true}, viewerTok{})
	readable := get(r, "/embed/v?slug=readable-doc").Body.String()
	unreadable := get(r, "/embed/v?slug=someone-elses-doc").Body.String()
	missing := get(r, "/embed/v?slug=does-not-exist-at-all").Body.String()

	if readable != unreadable || readable != missing {
		t.Fatal("gate page body differs by slug — it leaks existence")
	}
	for _, s := range []string{"readable-doc", "someone-elses-doc", "does-not-exist-at-all"} {
		if strings.Contains(readable, s) {
			t.Fatalf("gate page echoes the slug %q back into the body", s)
		}
	}
}

// ── test-plan item 4: a valid token serves as the viewer ─────────────────

func TestViewerMode_ValidTokenServesAsViewer(t *testing.T) {
	art := &fakeArt{serveOK: true}
	tok := viewerTok{vEmail: "reader@x.com", vAID: "aid-1"}
	rec := get(viewerRouter(art, tok), "/embed/v?slug=doc&t=TOKEN")

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "SERVED-VIEWER:doc:reader@x.com") {
		t.Fatalf("want the doc served as the viewer, got %s", rec.Body.String())
	}
	// The caller handed to the artifact layer must be the TOKEN's email, never a
	// configured service identity — this is the whole point of the feature.
	if art.gotCaller != "reader@x.com" {
		t.Fatalf("want caller reader@x.com, got %q", art.gotCaller)
	}
	if art.gotPinned != "aid-1" {
		t.Fatalf("want the token's artifact pin passed through, got %q", art.gotPinned)
	}
}

// ── test-plan item 5: ACL denial serves the placeholder, not content ──────

func TestViewerMode_ACLDenialFallsToPlaceholder(t *testing.T) {
	// serveOK:false models resolveIdent's checkAccess refusing this viewer.
	art := &fakeArt{serveOK: false}
	tok := viewerTok{vEmail: "nosy@x.com", vAID: "aid-1"}
	rec := get(viewerRouter(art, tok), "/embed/v?slug=doc&t=TOKEN")

	body := rec.Body.String()
	if strings.Contains(body, "SERVED") {
		t.Fatal("content served to a viewer the ACL denies")
	}
	if !strings.Contains(body, "Nothing to show here yet") {
		t.Fatalf("want the neutral placeholder, got %s", body)
	}
}

// ── test-plan item 6/7: token cannot be moved or substituted ─────────────

func TestViewerMode_RejectsUnusableTokens(t *testing.T) {
	cases := []struct {
		name string
		tok  TokenVerifier
	}{
		// An embed-user (APP) token: VerifyEmbedViewerToken rejects it for want of
		// the embed-viewer scope, which the real verifier enforces.
		{"app token rejected", viewerTok{vErr: errors.New("token missing embed-viewer scope")}},
		{"expired or malformed", viewerTok{vErr: errors.New("bad signature")}},
		{"verifier returns no email", viewerTok{vAID: "aid-1"}},
		{"verifier returns no artifact", viewerTok{vEmail: "a@x.com"}},
		// A verifier that does not implement ViewerTokenVerifier at all.
		{"verifier lacks viewer support", fakeTok{email: "a@x.com", aid: "aid-1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			art := &fakeArt{serveOK: true}
			rec := get(viewerRouter(art, c.tok), "/embed/v?slug=doc&t=TOKEN")
			body := rec.Body.String()
			if strings.Contains(body, "SERVED") {
				t.Fatalf("content served on an unusable token")
			}
			if !strings.Contains(body, "Sign in to view") {
				t.Fatalf("want a re-prompt via the gate page, got %s", body[:min(200, len(body))])
			}
			if art.gotIdent != "" {
				t.Fatal("artifact layer consulted with an unusable token")
			}
		})
	}
}

// ── the secret contract ──────────────────────────────────────────────────

func TestViewerMode_SecretOptionalButServiceUnchanged(t *testing.T) {
	art := &fakeArt{serveOK: true}
	r := viewerRouter(art, viewerTok{vEmail: "r@x.com", vAID: "aid-1"})

	// No secret configured: with and without a stale ?auth_secret=, both work.
	for _, u := range []string{"/embed/v?slug=doc&t=T", "/embed/v?slug=doc&t=T&auth_secret=leftover"} {
		if rec := get(r, u); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "SERVED-VIEWER") {
			t.Fatalf("%s: want the doc served, got %d %s", u, rec.Code, rec.Body.String())
		}
	}
	// A viewer surface that still HAS a secret configured must ALSO stop comparing
	// it, so a clean URL and an old one both work through a migration. Comparing
	// in viewer mode is what breaks one or the other (Review A F2).
	for _, u := range []string{"/embed/vsec?slug=doc&t=T", "/embed/vsec?slug=doc&t=T&auth_secret=OLD", "/embed/vsec?slug=doc&t=T&auth_secret=WRONG"} {
		if rec := get(r, u); rec.Code != http.StatusOK {
			t.Fatalf("%s: viewer mode must not compare the secret at all, got %d", u, rec.Code)
		}
	}
	// Service mode is untouched: no secret is still a 403.
	if rec := get(r, "/embed/svc?slug=doc"); rec.Code != http.StatusForbidden {
		t.Fatalf("service surface must still require its secret, got %d", rec.Code)
	}
}

func TestViewerMode_SlugAllowStillGates(t *testing.T) {
	art := &fakeArt{serveOK: true}
	r := viewerRouter(art, viewerTok{vEmail: "r@x.com", vAID: "aid-1"})
	// Outside slug_allow: refused before any token or artifact work.
	if rec := get(r, "/embed/vnarrow?slug=other-doc&t=T"); rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 outside slug_allow, got %d", rec.Code)
	}
	if art.gotIdent != "" {
		t.Fatal("artifact layer consulted for a slug outside slug_allow")
	}
	if rec := get(r, "/embed/vnarrow?slug=only-this&t=T"); rec.Code != http.StatusOK {
		t.Fatalf("want 200 inside slug_allow, got %d", rec.Code)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── regression: Bugbot #1 on PR 242 (High) ───────────────────────────────
//
// A viewer surface with NO configured secret must accept a leftover
// ?auth_secret= at EVERY gate, not just the doc route. The doc route used
// secretOK (which passes on an empty configured secret) while mint, complete and
// poll used a bare ConstantTimeCompare against "", so a migrated URL rendered
// the gate page and then 403'd the moment the viewer pressed Sign in — the gate
// page forwards the leftover secret into those calls.
func TestViewerMode_StaleSecretAcceptedAtEveryGate(t *testing.T) {
	srv := New(viewerSurfaces(), &fakeArt{serveOK: true}, viewerTok{vEmail: "reader@example.com", vAID: "aid-doc"})
	store := newSpyStore()
	srv.mint = &MintConfig{
		Identify:     func(*http.Request) string { return "reader@example.com" },
		Minter:       &versionSpyMinter{onResolve: func(*int32) {}},
		ChallengeKey: []byte("0123456789abcdef0123456789abcdef"),
		Store:        store,
	}
	r := chi.NewRouter()
	srv.Mount(r)
	r.Get(mintPath, srv.handleMint)

	// The URL a viewer pastes after the surface was migrated off service mode:
	// it still carries the old ?auth_secret=, and the surface no longer has one.
	const stale = "auth_secret=leftover-from-service-mode"

	// 1. The doc route accepts it and serves the gate.
	if rec := get(r, "/embed/v?slug=doc&"+stale); rec.Code != http.StatusOK {
		t.Fatalf("doc route rejected a stale secret: %d", rec.Code)
	}
	// 2. The mint route — the one the gate page's Sign in button opens — must
	//    accept it too. This is what regressed: a bare compare against "" 403'd.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, mintPath+"?surface=v&slug=doc&state=st&"+stale, nil))
	if rec.Code == http.StatusForbidden {
		t.Fatal("mint route 403'd a stale secret: Sign in is broken on every migrated URL")
	}
	// 3. The poll route the gate page then hits.
	if rec := get(r, "/embed/v/token?state=st&"+stale); rec.Code == http.StatusForbidden {
		t.Fatal("poll route 403'd a stale secret")
	}
	// 4. The completion endpoint.
	crec := httptest.NewRecorder()
	creq := httptest.NewRequest(http.MethodPost, "/embed/v/token",
		strings.NewReader("auth_secret=leftover-from-service-mode&consent_challenge=x"))
	creq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(crec, creq)
	if crec.Code == http.StatusForbidden && strings.Contains(crec.Body.String(), "forbidden") {
		t.Fatal("completion endpoint 403'd on the secret rather than on the consent challenge")
	}

	// Non-viewer modes are untouched: the comparison still runs and still bites.
	if rec := get(r, "/embed/svc?slug=doc&auth_secret=wrong"); rec.Code != http.StatusForbidden {
		t.Fatalf("service surface must still reject a wrong secret, got %d", rec.Code)
	}
	if !secretMatches(Surface{Secret: "REAL"}, "REAL") || secretMatches(Surface{Secret: "REAL"}, "wrong") {
		t.Fatal("service-mode comparison changed behaviour")
	}
}

// ── Review A F8: a pin mismatch re-prompts instead of dead-ending ─────────
//
// The common cause is a new version published between consent and reload: the
// token pins the old row's UUID and the request resolves the new one. One more
// prompt mints a token for the current row and self-heals, so the gate page is
// the right response. An ACL denial must still show the placeholder.
func TestViewerMode_PinMismatchReprompts(t *testing.T) {
	art := &fakeArt{serveOK: true, pinMismatch: true}
	r := viewerRouter(art, viewerTok{vEmail: "reader@example.com", vAID: "aid-old"})
	body := get(r, "/embed/v?slug=doc&t=T").Body.String()
	if !strings.Contains(body, "Sign in to view") {
		t.Fatalf("pin mismatch should re-serve the gate, got %s", body[:min(160, len(body))])
	}

	denied := &fakeArt{serveOK: false}
	body = get(viewerRouter(denied, viewerTok{vEmail: "nosy@example.com", vAID: "aid-1"}),
		"/embed/v?slug=doc&t=T").Body.String()
	if !strings.Contains(body, "Nothing to show here yet") {
		t.Fatal("an ACL denial must show the placeholder, not a re-prompt")
	}
}

// ── Review A F7: a consent cannot be redeemed for a different grant ───────
func TestChallengePurpose_DistinguishesGrants(t *testing.T) {
	viewer := Surface{Identity: "viewer"}
	user := Surface{Identity: "user"}
	if challengePurpose(viewer) == challengePurpose(user) {
		t.Fatal("viewer and user consents must not be interchangeable")
	}
	if challengePurpose(Surface{}) != challengePurpose(user) {
		t.Fatal("the default (service) purpose should match user, not viewer")
	}
}

// ── regression: Bugbot #2 on PR 242 (Medium) ─────────────────────────────
//
// Every version of a slug is its own row with its own UUID. The mint resolved
// "latest" while the doc route serves ?version=N, so a versioned embed minted a
// pin that could never match and a successful sign-in fell through to the
// placeholder. The version the doc route will serve must reach the resolve.
func TestViewerMode_VersionReachesTheResolve(t *testing.T) {
	var gotVer *int32
	m := &versionSpyMinter{onResolve: func(v *int32) { gotVer = v }}
	srv := New(viewerSurfaces(), &fakeArt{serveOK: true}, viewerTok{})
	srv.mint = &MintConfig{
		Identify: func(*http.Request) string { return "reader@example.com" },
		Minter:   m, ChallengeKey: []byte("0123456789abcdef0123456789abcdef"),
		Store: newSpyStore(),
	}
	r := chi.NewRouter()
	r.Get(mintPath, srv.handleMint)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		mintPath+"?surface=v&slug=doc&version=3&state=st", nil))

	if gotVer == nil {
		t.Fatal("the requested version never reached EmbedViewerArtifact; the pin cannot match the served row")
	}
	if *gotVer != 3 {
		t.Fatalf("want version 3 forwarded to the resolve, got %d", *gotVer)
	}
}

type versionSpyMinter struct{ onResolve func(*int32) }

func (m *versionSpyMinter) EmbedUserApp(_ context.Context, _, _ string) (string, string, error) {
	return "", "", nil
}
func (m *versionSpyMinter) MintEmbedUserToken(_ context.Context, _, _ string) (string, error) {
	return "t", nil
}
func (m *versionSpyMinter) EmbedViewerArtifact(_ context.Context, ident string, ver *int32, _ string) (string, string, string, bool, error) {
	m.onResolve(ver)
	return "Title", "doc", "aid-v3", false, nil
}
func (m *versionSpyMinter) MintEmbedViewerToken(_ context.Context, _, _ string) (string, error) {
	return "t", nil
}

type spyStore struct{ m map[string]string }

func newSpyStore() *spyStore { return &spyStore{m: map[string]string{}} }
func (s *spyStore) Put(_ context.Context, surface, state, tok, _ string, _ time.Duration) error {
	s.m[surface+"|"+state] = tok
	return nil
}
func (s *spyStore) Take(_ context.Context, surface, state string) (string, error) {
	return s.m[surface+"|"+state], nil
}

// ── regression: Bugbot #4 on PR 242 (Medium) ─────────────────────────────
//
// The consent copy must describe the grant the token ACTUALLY carries. Viewer
// mode's first wording said "Nothing else is granted", which is true for a
// document and false for an APP: a viewer token minted for an APP also
// authorizes that app's tool calls through the apps proxy. Understating the
// grant at the load-bearing click is the defect this pins.
func TestConsentCopy_MatchesTheActualGrant(t *testing.T) {
	render := func(surf Surface, isApp bool) string {
		srv := New(viewerSurfaces(), &fakeArt{}, viewerTok{})
		srv.mint = &MintConfig{CookieSecure: true}
		rec := httptest.NewRecorder()
		srv.writeConsentPage(rec, "v", surf, "aid", "st", "reader@example.com", "Thing", "ch", isApp)
		return rec.Body.String()
	}
	viewer := Surface{Identity: "viewer"}

	doc := render(viewer, false)
	if !strings.Contains(doc, "Nothing else is granted") {
		t.Fatal("a document consent should say nothing else is granted")
	}
	if strings.Contains(doc, "allowlisted tools") {
		t.Fatal("a document consent must not promise tool calls")
	}

	app := render(viewer, true)
	if !strings.Contains(app, "allowlisted tools") {
		t.Fatal("an APP consent must disclose that tools run as the viewer")
	}
	if strings.Contains(app, "Nothing else is granted") {
		t.Fatal("an APP consent must not claim nothing else is granted — the token carries tool access")
	}

	// User mode is unchanged.
	if u := render(Surface{Identity: "user"}, true); !strings.Contains(u, "allowlisted tools") {
		t.Fatal("user-mode consent copy regressed")
	}
}

// ── regression: the Notion desktop app (2026-08-19) ──────────────────────
//
// An Electron host intercepts window.open, hands the URL to the OS browser, and
// returns null to the page. The first gate page treated null as "popup blocked",
// showed an error and RETURNED before starting its poll — so in the Notion
// desktop app the consent flow completed out in the browser, the token landed in
// the pending store under the page's own state nonce, and the iframe never
// looked. It worked in Notion web and failed in the desktop app for that reason
// alone.
//
// The invariant: polling starts regardless of what window.open returns.
func TestGatePage_PollsEvenWhenWindowOpenReturnsNull(t *testing.T) {
	js := gatePageHTML

	// The exact shape of the bug: bail out of the click handler on a null window.
	if strings.Contains(js, `if(!w){stop(`) {
		t.Fatal("gate page still aborts sign-in when window.open returns null; Electron hosts can never complete")
	}
	// The poll must not sit inside a branch guarded by the window handle.
	openIdx := strings.Index(js, "window.open")
	pollIdx := strings.Index(js, "setInterval")
	if openIdx < 0 || pollIdx < 0 || pollIdx < openIdx {
		t.Fatal("expected the poll to be set up after the window.open attempt")
	}
	between := js[openIdx:pollIdx]
	if strings.Contains(between, "return;") {
		t.Fatal("an early return sits between window.open and the poll, so a null window skips polling")
	}
	// A genuine popup block still needs a manual route out, and it must be a
	// link rather than an error: in Electron the window really did open.
	if !strings.Contains(js, `a.target="_blank"`) || !strings.Contains(js, "continue here") {
		t.Fatal("gate page offers no manual sign-in link when window.open returns null")
	}
	if strings.Contains(js, "Allow pop-ups and try again") {
		t.Fatal("gate page still tells Electron users to allow pop-ups, which is not their problem")
	}
}
