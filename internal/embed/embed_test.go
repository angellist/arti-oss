package embed

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeArt struct {
	serveOK, fileOK                        bool
	gotSurface, gotIdent, gotCaller, gotFA string
	gotOrigins                             []string
	gotVer                                 *int32
	gotFileAID, gotFilePath, gotFileCaller string
}

func (f *fakeArt) ServeForEmbed(w http.ResponseWriter, _ *http.Request, surface, ident string, ver *int32, caller, fa string) bool {
	f.gotSurface, f.gotIdent, f.gotVer, f.gotCaller, f.gotFA = surface, ident, ver, caller, fa
	if !f.serveOK {
		return false
	}
	w.Header().Set("Content-Type", "text/html")
	_, _ = io.WriteString(w, "SERVED:"+ident)
	return true
}

func (f *fakeArt) ServeEmbedFile(w http.ResponseWriter, _ *http.Request, aid, p, caller, _ string) bool {
	f.gotFileAID, f.gotFilePath, f.gotFileCaller = aid, p, caller
	if !f.fileOK {
		return false
	}
	_, _ = io.WriteString(w, "FILE:"+p)
	return true
}

func (f *fakeArt) ServeForEmbedUser(w http.ResponseWriter, _ *http.Request, surface, ident string, ver *int32, fa string, origins []string) bool {
	f.gotSurface, f.gotIdent, f.gotVer, f.gotCaller, f.gotFA = surface, ident, ver, "<user-mode>", fa
	f.gotOrigins = origins
	if !f.serveOK {
		return false
	}
	w.Header().Set("Content-Type", "text/html")
	_, _ = io.WriteString(w, "SERVED-USER:"+ident)
	return true
}

func (f *fakeArt) ServeEmbedFilePublic(w http.ResponseWriter, _ *http.Request, aid, p, _ string) bool {
	f.gotFileAID, f.gotFilePath, f.gotFileCaller = aid, p, "<user-mode>"
	if !f.fileOK {
		return false
	}
	_, _ = io.WriteString(w, "FILE-USER:"+p)
	return true
}

type fakeTok struct {
	email, aid string
	err        error
}

func (t fakeTok) VerifyEmbedToken(string) (string, string, error) { return t.email, t.aid, t.err }

func (t fakeTok) VerifyEmbedFilesToken(string) (string, error) { return t.aid, t.err }

func testSurfaces() map[string]Surface {
	return map[string]Surface{
		"front": {Secret: "S", Origin: OriginList{"https://app.frontapp.com"}, Email: "e@x.com",
			SlugAllow: []string{"deployment-ctx-*"}, Shell: "front", ShellSlug: "deployment-ctx-{id}",
			ShellQuery: "ctx=deploy-ctx-{id}"},
		"all": {Secret: "S2", Origin: OriginList{"https://dash.x"}, Email: "e2@x.com", SlugAllow: []string{"*"}},
	}
}

func router(art ArtServer, tok TokenVerifier) chi.Router {
	r := chi.NewRouter()
	New(testSurfaces(), art, tok).Mount(r)
	return r
}

func do(r chi.Router, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestParseSurfaces(t *testing.T) {
	if m, err := ParseSurfaces(""); err != nil || m != nil {
		t.Errorf("empty: got (%v,%v), want (nil,nil)", m, err)
	}
	ok := `{"front":{"secret":"s","origin":"https://app.frontapp.com","email":"e@x.com","slug_allow":["deployment-ctx-*"],"shell":"front","shell_slug":"deployment-ctx-{id}"}}`
	if _, err := ParseSurfaces(ok); err != nil {
		t.Errorf("valid surface rejected: %v", err)
	}
	// The per-thread {id} may ride in shell_query instead of shell_slug: a static
	// app slug served with a ?ctx= param. This must PASS, and shell_query round-trips.
	okQuery := `{"front":{"secret":"s","origin":"https://app.frontapp.com","email":"e@x.com","slug_allow":["deployment-panel-v3-app"],"shell":"front","shell_slug":"deployment-panel-v3-app","shell_query":"ctx=deploy-ctx-{id}"}}`
	if mq, err := ParseSurfaces(okQuery); err != nil {
		t.Errorf("shell_query surface rejected: %v", err)
	} else if got := mq["front"].ShellQuery; got != "ctx=deploy-ctx-{id}" {
		t.Errorf("shell_query = %q, want the configured template", got)
	}
	bad := map[string]string{
		"missing secret":     `{"x":{"origin":"o","email":"e","slug_allow":["*"]}}`,
		"missing origin":     `{"x":{"secret":"s","email":"e","slug_allow":["*"]}}`,
		"missing email":      `{"x":{"secret":"s","origin":"o","slug_allow":["*"]}}`,
		"missing slug_allow": `{"x":{"secret":"s","origin":"o","email":"e"}}`,
		"unknown shell":      `{"x":{"secret":"s","origin":"o","email":"e","slug_allow":["*"],"shell":"slack"}}`,
		"shell wo {id}":      `{"x":{"secret":"s","origin":"o","email":"e","slug_allow":["*"],"shell":"front","shell_slug":"x-"}}`,
		// {id} in NEITHER shell_slug nor shell_query — a shell that can never carry
		// the per-thread id must fail fast.
		"shell {id} in neither": `{"x":{"secret":"s","origin":"o","email":"e","slug_allow":["*"],"shell":"front","shell_slug":"static-app","shell_query":"ctx=static"}}`,
		// Empty shell_slug with {id} only in shell_query: passes the {id} rule but an
		// empty slug re-serves the shell loader at runtime — must fail fast.
		"shell empty slug": `{"x":{"secret":"s","origin":"o","email":"e","slug_allow":["*"],"shell":"front","shell_query":"ctx=deploy-ctx-{id}"}}`,
		// Identity-mode validation fails fast in BOTH directions.
		"user mode with email": `{"x":{"secret":"s","origin":"o","identity":"user","email":"e","slug_allow":["*"]}}`,
		"unknown identity":     `{"x":{"secret":"s","origin":"o","identity":"bot","email":"e","slug_allow":["*"]}}`,
		"empty origin entry":   `{"x":{"secret":"s","origin":["https://a"," "],"email":"e","slug_allow":["*"]}}`,
	}
	for name, js := range bad {
		if _, err := ParseSurfaces(js); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}

	// User mode: email absent is REQUIRED-absent; origin accepts an array (the
	// nested topology enumerates every ancestor) or the legacy single string.
	userOK := `{"x":{"secret":"s","origin":["https://app.frontapp.com","https://couch.internal"],"identity":"user","slug_allow":["lp-*"]}}`
	m, err := ParseSurfaces(userOK)
	if err != nil {
		t.Fatalf("valid user-mode surface rejected: %v", err)
	}
	if got := m["x"].Origin; len(got) != 2 || got[0] != "https://app.frontapp.com" || got[1] != "https://couch.internal" {
		t.Errorf("origin list = %v", got)
	}
	if fa := frameAncestors(m["x"].Origin); fa != "'self' https://app.frontapp.com https://couch.internal" {
		t.Errorf("frameAncestors = %q — every ancestor must be listed", fa)
	}
	svcOK := `{"x":{"secret":"s","origin":"https://one.example","identity":"service","email":"e@x.com","slug_allow":["*"]}}`
	if m, err = ParseSurfaces(svcOK); err != nil || len(m["x"].Origin) != 1 {
		t.Errorf("legacy single-string origin must keep working: m=%v err=%v", m, err)
	}
}

func TestSlugAllowed(t *testing.T) {
	cases := []struct {
		pats []string
		slug string
		want bool
	}{
		{[]string{"deployment-ctx-*"}, "deployment-ctx-cnv_1", true},
		{[]string{"deployment-ctx-*"}, "report-x", false},
		{[]string{"report-q3"}, "report-q3", true},
		{[]string{"report-q3"}, "report-q4", false},
		{[]string{"*"}, "anything-at-all", true},
		{[]string{"a-*", "b-*"}, "b-2", true},
	}
	for _, c := range cases {
		if got := slugAllowed(c.pats, c.slug); got != c.want {
			t.Errorf("slugAllowed(%v,%q)=%v, want %v", c.pats, c.slug, got, c.want)
		}
	}
}

func TestHandleDoc_Secret(t *testing.T) {
	art := &fakeArt{serveOK: true}
	r := router(art, fakeTok{})
	// wrong + missing + prefix-of-secret all rejected
	for _, target := range []string{
		"/embed/front?slug=deployment-ctx-c&auth_secret=wrong",
		"/embed/front?slug=deployment-ctx-c",
		"/embed/front?slug=deployment-ctx-c&auth_secret=Sx",
	} {
		if rec := do(r, target); rec.Code != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403", target, rec.Code)
		}
	}
}

func TestHandleDoc_MissingSlug(t *testing.T) {
	// A NON-shell surface with no slug is a 400 (the embedder must build ?slug=).
	if rec := do(router(&fakeArt{}, fakeTok{}), "/embed/all?auth_secret=S2"); rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
}

func TestHandleDoc_ShellSurfaceBareServesShell(t *testing.T) {
	// A shell surface hit at its bare path with no slug is the shell's front
	// door — serve the adapter (200), not a 400. No secret needed (the loader
	// isn't gated; it forwards the secret to the doc fetch).
	rec := do(router(&fakeArt{}, fakeTok{}), "/embed/front")
	if rec.Code != http.StatusOK {
		t.Fatalf("bare shell surface: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "contextUpdates") || !strings.Contains(body, "deployment-ctx-{id}") {
		t.Errorf("bare path did not serve the front shell: %q", body)
	}
	if rec.Header().Get("X-Frame-Options") != "" {
		t.Error("shell kept X-Frame-Options (Front can't frame it)")
	}
}

func TestHandleDoc_SlugNotAllowed(t *testing.T) {
	if rec := do(router(&fakeArt{serveOK: true}, fakeTok{}), "/embed/front?slug=evil-slug&auth_secret=S"); rec.Code != http.StatusForbidden {
		t.Errorf("got %d, want 403", rec.Code)
	}
}

func TestHandleDoc_ServesWithSurfaceContext(t *testing.T) {
	art := &fakeArt{serveOK: true}
	rec := do(router(art, fakeTok{}), "/embed/front?slug=deployment-ctx-cnv_1&version=3&auth_secret=S")
	if rec.Code != http.StatusOK || rec.Body.String() != "SERVED:deployment-ctx-cnv_1" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if art.gotSurface != "front" || art.gotIdent != "deployment-ctx-cnv_1" || art.gotCaller != "e@x.com" {
		t.Errorf("bad args: surface=%q ident=%q caller=%q", art.gotSurface, art.gotIdent, art.gotCaller)
	}
	if art.gotFA != "'self' https://app.frontapp.com" {
		t.Errorf("frameAncestors = %q", art.gotFA)
	}
	if art.gotVer == nil || *art.gotVer != 3 {
		t.Errorf("version = %v, want 3", art.gotVer)
	}
}

func TestHandleDoc_PlaceholderOnMiss(t *testing.T) {
	rec := do(router(&fakeArt{serveOK: false}, fakeTok{}), "/embed/all?slug=report-x&auth_secret=S2")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 placeholder", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, "report-x") || !strings.Contains(strings.ToLower(b), "nothing") {
		t.Errorf("placeholder body = %q", b)
	}
	if rec.Header().Get("X-Frame-Options") != "" {
		t.Error("placeholder kept X-Frame-Options")
	}
}

func TestHandleShell_Front(t *testing.T) {
	rec := do(router(&fakeArt{}, fakeTok{}), "/embed/front/shell?auth_secret=S")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp != "frame-ancestors https://app.frontapp.com" {
		t.Errorf("shell CSP = %q", csp)
	}
	if rec.Header().Get("X-Frame-Options") != "" {
		t.Error("shell kept X-Frame-Options (Front can't frame it)")
	}
	body := rec.Body.String()
	// The shell_slug literal, the shell_query literal, and the show() logic that
	// {id}-substitutes shell_query and appends it to the doc-route fetch must all
	// be present — that append is what carries the per-thread ctx to the app.
	for _, want := range []string{"contextUpdates", "'front'", "deployment-ctx-{id}", "'ctx=deploy-ctx-{id}'", "SHELL_QUERY.replace"} {
		if !strings.Contains(body, want) {
			t.Errorf("shell body missing %q", want)
		}
	}
}

func TestHandleShell_NoneIs404(t *testing.T) {
	if rec := do(router(&fakeArt{}, fakeTok{}), "/embed/all/shell"); rec.Code != http.StatusNotFound {
		t.Errorf("surface without shell: got %d, want 404", rec.Code)
	}
}

func TestHandleFile_TokenIsCredential(t *testing.T) {
	art := &fakeArt{fileOK: true}
	rec := do(router(art, fakeTok{email: "e@x.com", aid: "AID-1"}), "/embed/front/_files/anytoken/css/app.css")
	if rec.Code != http.StatusOK || rec.Body.String() != "FILE:css/app.css" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if art.gotFileAID != "AID-1" || art.gotFilePath != "css/app.css" || art.gotFileCaller != "e@x.com" {
		t.Errorf("bad file args: aid=%q path=%q caller=%q", art.gotFileAID, art.gotFilePath, art.gotFileCaller)
	}
}

func TestHandleFile_BadToken(t *testing.T) {
	rec := do(router(&fakeArt{fileOK: true}, fakeTok{err: errors.New("bad")}), "/embed/front/_files/x/a.js")
	if rec.Code != http.StatusForbidden {
		t.Errorf("got %d, want 403", rec.Code)
	}
}

// userRouter mounts a user-mode surface next to the service-mode ones.
func userRouter(art ArtServer, tok TokenVerifier) chi.Router {
	surfaces := testSurfaces()
	surfaces["panel"] = Surface{Secret: "US", Origin: OriginList{"https://app.frontapp.com", "https://couch.internal"},
		Identity: "user", SlugAllow: []string{"lp-*"}}
	r := chi.NewRouter()
	New(surfaces, art, tok).Mount(r)
	return r
}

func TestHandleDoc_UserModeServesWithoutCaller(t *testing.T) {
	art := &fakeArt{serveOK: true}
	rec := do(userRouter(art, fakeTok{}), "/embed/panel?slug=lp-ctx&auth_secret=US")
	if rec.Code != http.StatusOK || rec.Body.String() != "SERVED-USER:lp-ctx" {
		t.Fatalf("got %d %q, want the user-mode serve path", rec.Code, rec.Body.String())
	}
	if art.gotCaller != "<user-mode>" {
		t.Errorf("caller = %q — user mode must not resolve as any fixed identity", art.gotCaller)
	}
	if art.gotFA != "'self' https://app.frontapp.com https://couch.internal" {
		t.Errorf("frameAncestors = %q — must list every ancestor", art.gotFA)
	}
	// The surface's raw origin list must reach the app bridge (the token-relay
	// postMessage targets) — same set frame-ancestors admits, without 'self'.
	if len(art.gotOrigins) != 2 || art.gotOrigins[0] != "https://app.frontapp.com" || art.gotOrigins[1] != "https://couch.internal" {
		t.Errorf("origins = %v — surface origins must be forwarded for the token relay", art.gotOrigins)
	}
	// The secret + slug_allow gates still apply in user mode.
	if rec := do(userRouter(&fakeArt{serveOK: true}, fakeTok{}), "/embed/panel?slug=lp-ctx&auth_secret=nope"); rec.Code != http.StatusForbidden {
		t.Errorf("user mode without secret: got %d, want 403", rec.Code)
	}
	if rec := do(userRouter(&fakeArt{serveOK: true}, fakeTok{}), "/embed/panel?slug=evil&auth_secret=US"); rec.Code != http.StatusForbidden {
		t.Errorf("user mode off-allowlist slug: got %d, want 403", rec.Code)
	}
}

func TestHandleFile_UserModeUsesFilesToken(t *testing.T) {
	// User-mode files ride the email-less files token (VerifyEmbedFilesToken)
	// and the public (no-caller) serve path.
	art := &fakeArt{fileOK: true}
	rec := do(userRouter(art, fakeTok{aid: "AID-9"}), "/embed/panel/_files/tok/css/app.css")
	if rec.Code != http.StatusOK || rec.Body.String() != "FILE-USER:css/app.css" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if art.gotFileAID != "AID-9" || art.gotFileCaller != "<user-mode>" {
		t.Errorf("bad file args: aid=%q caller=%q", art.gotFileAID, art.gotFileCaller)
	}
	if rec := do(userRouter(&fakeArt{fileOK: true}, fakeTok{err: errors.New("bad")}), "/embed/panel/_files/x/a.js"); rec.Code != http.StatusForbidden {
		t.Errorf("bad files token: got %d, want 403", rec.Code)
	}
}

func TestMount_NoSurfacesNoRoutes(t *testing.T) {
	r := chi.NewRouter()
	New(nil, &fakeArt{}, fakeTok{}).Mount(r)
	if rec := do(r, "/embed/front/shell"); rec.Code != http.StatusNotFound {
		t.Errorf("routes registered with no surfaces: got %d, want 404", rec.Code)
	}
}
