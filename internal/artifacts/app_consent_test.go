package artifacts

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

const consentTestApp = "0b6f2e9e-9d1a-4b1c-8f1e-1d2c3b4a5f60"

// consentTestService wires fake minter/consent fns whose "signature" is just a
// recognizable string, so the gate's decisions can be tested without a DB.
func consentTestService() *Service {
	s := NewService(nil, "http://x", nil, nil)
	s.SetAppTokenFn(func(email, id string) (string, error) { return "BRIDGE-" + id, nil })
	s.SetAppTokenVerifyFn(func(tok string) (string, string, error) { return "", "", nil })
	s.SetAppConsentFns(
		func(email, id string) (string, error) { return "ok|" + email + "|" + id, nil },
		func(tok string) (string, string, error) {
			parts := strings.Split(tok, "|")
			if len(parts) != 3 || parts[0] != "ok" {
				return "", "", errBadRequest("bad consent")
			}
			return parts[1], parts[2], nil
		},
		true,
	)
	return s
}

func appRow(t *testing.T, manifest string, opts ...func(*sqlc.Artifact)) sqlc.Artifact {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"arti-app.json": manifest, "index.html": "<html><body>app</body></html>"} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	id := uuid.MustParse(consentTestApp)
	slug, ver := "demo-app", int32(3)
	row := sqlc.Artifact{
		ArtifactID: pgtype.UUID{Bytes: id, Valid: true}, ArtifactType: pgstore.TypeApp,
		NamedSlug: &slug, Version: &ver, Title: "Demo <app>", ContentType: "application/zip",
		InlineContent: buf.Bytes(), Creator: "alice@example.com",
		CreatedAt: pgtype.Timestamptz{Time: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC), Valid: true},
	}
	for _, o := range opts {
		o(&row)
	}
	return row
}

func TestAppConsented(t *testing.T) {
	s := consentTestService()
	row := appRow(t, `{"tools":[{"server":"slack","tool":"send"}]}`)
	req := httptest.NewRequest(http.MethodGet, "/app/demo-app", nil)
	if s.appConsented(req, "alice@example.com", row) {
		t.Fatal("no cookie must mean no consent")
	}
	req.AddCookie(&http.Cookie{Name: appConsentCookiePrefix + consentTestApp, Value: "ok|alice@example.com|" + consentTestApp})
	if !s.appConsented(req, "Alice@Example.com", row) {
		t.Fatal("a valid record for this viewer+version must count")
	}
	if s.appConsented(req, "bob@example.com", row) {
		t.Fatal("another viewer must not inherit the record")
	}
	other := appRow(t, `{}`, func(r *sqlc.Artifact) {
		r.ArtifactID = pgtype.UUID{Bytes: uuid.MustParse("11111111-2222-4333-8444-555555555555"), Valid: true}
	})
	if s.appConsented(req, "alice@example.com", other) {
		t.Fatal("a record for one version must not unlock another")
	}
	req3 := httptest.NewRequest(http.MethodGet, "/app/x", nil)
	req3.AddCookie(&http.Cookie{Name: appConsentCookiePrefix + consentTestApp, Value: "tampered"})
	if s.appConsented(req3, "alice@example.com", row) {
		t.Fatal("an unverifiable record must not count")
	}

	// Minter wired, verifier missing: fail closed.
	closed := NewService(nil, "http://x", nil, nil)
	closed.SetAppTokenFn(func(string, string) (string, error) { return "t", nil })
	if closed.appConsented(req, "alice@example.com", row) {
		t.Fatal("a minter without a consent verifier must refuse")
	}
	// No minter at all: nothing to protect.
	if !NewService(nil, "http://x", nil, nil).appConsented(req, "alice@example.com", row) {
		t.Fatal("with no bridge minter the gate is moot")
	}
}

// A slug-wide record unlocks later versions only while the publisher and the
// declared tools are unchanged; either changing asks again.
func TestAppConsentedForAllVersions(t *testing.T) {
	s := consentTestService()
	v3 := appRow(t, `{"tools":[{"server":"slack","tool":"send"}]}`)
	rec := httptest.NewRecorder()
	if err := s.grantAppConsent(rec, httptest.NewRequest(http.MethodPost, "/app/demo-app/consent", nil), v3, "alice@example.com", true); err != nil {
		t.Fatal(err)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 2 {
		t.Fatalf("want version + slug cookies, got %d", len(cs))
	}
	withCookies := func(t *testing.T) *http.Request {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/app/demo-app", nil)
		for _, c := range cs {
			r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
		return r
	}
	newID := func(r *sqlc.Artifact) {
		r.ArtifactID = pgtype.UUID{Bytes: uuid.MustParse("22222222-2222-4333-8444-555555555555"), Valid: true}
		v := int32(4)
		r.Version = &v
	}
	v4 := appRow(t, `{"tools":[{"server":"slack","tool":"send"}]}`, newID)
	if !s.appConsented(withCookies(t), "alice@example.com", v4) {
		t.Fatal("same publisher, same tools: the slug record must unlock v4")
	}
	if s.appConsented(withCookies(t), "bob@example.com", v4) {
		t.Fatal("another viewer must not inherit the slug record")
	}
	widened := appRow(t, `{"tools":[{"server":"slack","tool":"send"},{"server":"snowflake","tool":"run_query"}]}`, newID)
	if s.appConsented(withCookies(t), "alice@example.com", widened) {
		t.Fatal("a version that declares different tools must ask again")
	}
	otherPublisher := appRow(t, `{"tools":[{"server":"slack","tool":"send"}]}`, newID, func(r *sqlc.Artifact) { r.Creator = "mallory@example.com" })
	if s.appConsented(withCookies(t), "alice@example.com", otherPublisher) {
		t.Fatal("a version from a different publisher must ask again")
	}
	otherSlug := appRow(t, `{"tools":[{"server":"slack","tool":"send"}]}`, newID, func(r *sqlc.Artifact) { sl := "other-app"; r.NamedSlug = &sl })
	if s.appConsented(withCookies(t), "alice@example.com", otherSlug) {
		t.Fatal("a slug record must not unlock another slug")
	}
}

func TestWriteAppConsentPage(t *testing.T) {
	s := consentTestService()
	via := "app:9f9f9f9f-0000-4000-8000-000000000000"
	row := appRow(t, `{"name":"demo","entry":"index.html","tools":[{"server":"slack","tool":"send"},{"server":"arti","tool":"add_comment"},{"server":"slack","tool":"read"}]}`,
		func(r *sqlc.Artifact) { r.WrittenVia = &via })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/app/demo-app?x=1&y=2", nil)
	s.writeAppConsentPage(rec, req, row)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(body, "BRIDGE-") || strings.Contains(body, "__ARTI_APP__") {
		t.Fatal("consent page must carry no bridge token")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); strings.Contains(csp, "sandbox") {
		t.Fatalf("consent page must not be sandboxed, got CSP %q", csp)
	}
	for _, want := range []string{
		"alice@example.com", "Demo &lt;app&gt;", "demo-app", "v3", consentTestApp,
		`<span class="srv">arti</span> <span class="chip chip-count">1 write</span>`,
		"I trust this creator, run this app as me",
		`name="scope" value="slug"`, "Trust all versions of <code>demo-app</code>",
		`<li><code>add_comment</code> <span class="chip chip-write">write</span></li>`,
		`<li><code>read</code> <span class="chip chip-read">read</span></li>`,
		`<li><code>send</code> <span class="chip chip-write">write</span></li>`,
		`action="/app/` + consentTestApp + `/consent"`,
		`name="next" value="/app/demo-app?x=1&amp;y=2"`,
		"automated process",
		`href="/app/` + consentTestApp + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("consent page missing %q\n%s", want, body)
		}
	}

	// A person-published app with no tools says so, and is not flagged.
	plain := appRow(t, `{"name":"demo","entry":"index.html"}`)
	rec2 := httptest.NewRecorder()
	s.writeAppConsentPage(rec2, httptest.NewRequest(http.MethodGet, "/app/demo-app", nil), plain)
	if b := rec2.Body.String(); !strings.Contains(b, "No connector tools declared") || strings.Contains(b, "automated process") {
		t.Fatalf("plain app page wrong:\n%s", b)
	}
	// A couch-scoped row is flagged even without written_via.
	couch := appRow(t, `{}`, func(r *sqlc.Artifact) { r.Scopes = []string{pgstore.ScopeAppCouch} })
	rec3 := httptest.NewRecorder()
	s.writeAppConsentPage(rec3, httptest.NewRequest(http.MethodGet, "/app/demo-app", nil), couch)
	if !strings.Contains(rec3.Body.String(), "automated process") {
		t.Fatal("app:couch row must be flagged as automated")
	}
}

func TestGrantAppConsentCookie(t *testing.T) {
	s := consentTestService()
	rec := httptest.NewRecorder()
	if err := s.grantAppConsent(rec, httptest.NewRequest(http.MethodPost, "/x", nil), appRow(t, `{}`), "alice@example.com", false); err != nil {
		t.Fatal(err)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("want one cookie, got %d", len(cs))
	}
	c := cs[0]
	if c.Name != appConsentCookiePrefix+consentTestApp || c.Value != "ok|alice@example.com|"+consentTestApp {
		t.Fatalf("cookie %s=%s", c.Name, c.Value)
	}
	if !c.HttpOnly || !c.Secure || c.Path != "/" || c.SameSite != http.SameSiteLaxMode || c.MaxAge <= 0 {
		t.Fatalf("cookie attributes: %+v", c)
	}
}

// Once a version is consented the frame shows the app, so a further consent
// POST is the app's code talking through the relay; it must not grant slug scope.
func TestGrantAppConsentIgnoresAnAlreadyConsentedViewer(t *testing.T) {
	s := consentTestService()
	slug := "demo-app"
	row := appRow(t, `{}`, func(r *sqlc.Artifact) { r.NamedSlug = &slug })
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.AddCookie(&http.Cookie{Name: appConsentCookiePrefix + consentTestApp, Value: "ok|alice@example.com|" + consentTestApp})
	rec := httptest.NewRecorder()
	if err := s.grantAppConsent(rec, req, row, "alice@example.com", true); err != nil {
		t.Fatal(err)
	}
	if cs := rec.Result().Cookies(); len(cs) != 0 {
		t.Fatalf("an already-consented viewer must get no new cookie, got %v", cs)
	}
	// A different viewer with the same cookie value is not consented and does get one.
	rec = httptest.NewRecorder()
	if err := s.grantAppConsent(rec, req, row, "bob@example.com", false); err != nil {
		t.Fatal(err)
	}
	if cs := rec.Result().Cookies(); len(cs) != 1 {
		t.Fatalf("want one cookie for bob, got %d", len(cs))
	}
}

func TestSafeAppNext(t *testing.T) {
	for next, ok := range map[string]bool{
		"/app/demo?x=1":                       true,
		"/api/artifacts/abc/files/index.html": true,
		"//evil.example/app/x":                false,
		"https://evil.example/app/x":          false,
		"/s/demo":                             false,
		"/app/x\r\nSet-Cookie: a=b":           false,
	} {
		if got := safeAppNext(next); got != ok {
			t.Errorf("safeAppNext(%q) = %v, want %v", next, got, ok)
		}
	}
}

func TestToolAccess(t *testing.T) {
	for _, c := range []struct{ server, tool, want string }{
		{"arti", "read_artifact", "read"},
		{"arti", "add_comment", "write"},
		{"arti-self", "update_artifact", "write"},
		{"arti", "map_snapshot", "write"},
		{"arti", "map_get", "read"},
		{"llm", "complete", "llm"},
		{"snowflake", "run_query", "read"},
		{"front", "search_conversations", "read"},
		{"front", "mark_message_seen", "write"},
		{"slack", "send_message", "write"},
		{"gong", "get-call", "read"},
		{"salesforce", "delete_record", "write"},
		{"x", "frobnicate", "write"},
		{"x", "list_and_update", "write"},
	} {
		if got := toolAccess(c.server, c.tool); got != c.want {
			t.Errorf("%s/%s = %s, want %s", c.server, c.tool, got, c.want)
		}
	}
}

// The consent page stands in for the app, so it must be framable wherever the
// app is: the global X-Frame-Options must go and frame-ancestors must be set,
// with no sandbox so its form can post.
func TestWriteAppConsentPageFramingHeaders(t *testing.T) {
	s := consentTestService()
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Frame-Options", "SAMEORIGIN")
	s.writeAppConsentPage(rec, httptest.NewRequest(http.MethodGet, "/app/demo-app", nil), appRow(t, `{}`))
	if csp := rec.Header().Get("Content-Security-Policy"); strings.Contains(csp, "sandbox") || !strings.HasPrefix(csp, "frame-ancestors ") {
		t.Fatalf("CSP = %q", csp)
	}
	if xfo := rec.Header().Get("X-Frame-Options"); xfo != "" {
		t.Fatalf("X-Frame-Options must be dropped, got %q", xfo)
	}
}
