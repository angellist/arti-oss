package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
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

// fakeConsents is an in-memory app_consents table.
type fakeConsents map[string]time.Time

func (f fakeConsents) has(_ context.Context, email string, subjects ...string) (bool, error) {
	for _, sub := range subjects {
		if until, ok := f[strings.ToLower(email)+"|"+sub]; ok && until.After(time.Now()) {
			return true, nil
		}
	}
	return false, nil
}

func (f fakeConsents) grant(_ context.Context, email, subject string, until time.Time) error {
	f[strings.ToLower(email)+"|"+subject] = until
	return nil
}

// consentTestService wires a fake minter and an in-memory consent store, so
// the gate's decisions can be tested without a DB.
func consentTestService() (*Service, fakeConsents) {
	s := NewService(nil, "http://x", nil, nil)
	s.SetAppTokenFn(func(email, id string) (string, error) { return "BRIDGE-" + id, nil })
	s.SetAppTokenVerifyFn(func(tok string) (string, string, error) { return "", "", nil })
	f := fakeConsents{}
	s.SetAppConsentFns(f.has, f.grant)
	return s, f
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
	s, f := consentTestService()
	row := appRow(t, `{"tools":[{"server":"slack","tool":"send"}]}`)
	req := httptest.NewRequest(http.MethodGet, "/app/demo-app", nil)
	if s.appConsented(req, "alice@example.com", row) {
		t.Fatal("no record must mean no consent")
	}
	f["alice@example.com|"+consentTestApp] = time.Now().Add(time.Hour)
	if !s.appConsented(req, "Alice@Example.com", row) {
		t.Fatal("a record for this viewer+version must count")
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
	f["alice@example.com|"+consentTestApp] = time.Now().Add(-time.Second)
	if s.appConsented(req, "alice@example.com", row) {
		t.Fatal("an expired record must not count")
	}

	// A store error denies.
	broken := NewService(nil, "http://x", nil, nil)
	broken.SetAppTokenFn(func(string, string) (string, error) { return "t", nil })
	broken.SetAppConsentFns(func(context.Context, string, ...string) (bool, error) { return true, errBadRequest("db down") }, nil)
	if broken.appConsented(req, "alice@example.com", row) {
		t.Fatal("a store error must refuse")
	}
	// Minter wired, store missing: fail closed.
	closed := NewService(nil, "http://x", nil, nil)
	closed.SetAppTokenFn(func(string, string) (string, error) { return "t", nil })
	if closed.appConsented(req, "alice@example.com", row) {
		t.Fatal("a minter without a consent store must refuse")
	}
	// No minter at all: nothing to protect.
	if !NewService(nil, "http://x", nil, nil).appConsented(req, "alice@example.com", row) {
		t.Fatal("with no bridge minter the gate is moot")
	}
}

// A slug-wide record unlocks later versions only while the publisher and the
// declared tools are unchanged; either changing asks again.
func TestAppConsentedForAllVersions(t *testing.T) {
	s, f := consentTestService()
	v3 := appRow(t, `{"tools":[{"server":"slack","tool":"send"}]}`)
	if err := s.grantAppConsent(httptest.NewRequest(http.MethodPost, "/app/demo-app/consent", nil), v3, "alice@example.com", true); err != nil {
		t.Fatal(err)
	}
	if len(f) != 2 {
		t.Fatalf("want version + slug records, got %v", f)
	}
	req := httptest.NewRequest(http.MethodGet, "/app/demo-app", nil)
	newID := func(r *sqlc.Artifact) {
		r.ArtifactID = pgtype.UUID{Bytes: uuid.MustParse("22222222-2222-4333-8444-555555555555"), Valid: true}
		v := int32(4)
		r.Version = &v
	}
	v4 := appRow(t, `{"tools":[{"server":"slack","tool":"send"}]}`, newID)
	if !s.appConsented(req, "alice@example.com", v4) {
		t.Fatal("same publisher, same tools: the slug record must unlock v4")
	}
	if s.appConsented(req, "bob@example.com", v4) {
		t.Fatal("another viewer must not inherit the slug record")
	}
	widened := appRow(t, `{"tools":[{"server":"slack","tool":"send"},{"server":"snowflake","tool":"run_query"}]}`, newID)
	if s.appConsented(req, "alice@example.com", widened) {
		t.Fatal("a version that declares different tools must ask again")
	}
	otherPublisher := appRow(t, `{"tools":[{"server":"slack","tool":"send"}]}`, newID, func(r *sqlc.Artifact) { r.Creator = "mallory@example.com" })
	if s.appConsented(req, "alice@example.com", otherPublisher) {
		t.Fatal("a version from a different publisher must ask again")
	}
	otherSlug := appRow(t, `{"tools":[{"server":"slack","tool":"send"}]}`, newID, func(r *sqlc.Artifact) { sl := "other-app"; r.NamedSlug = &sl })
	if s.appConsented(req, "alice@example.com", otherSlug) {
		t.Fatal("a slug record must not unlock another slug")
	}
}

func TestWriteAppConsentPage(t *testing.T) {
	s, _ := consentTestService()
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
	// An app:couch-scoped row is flagged even without written_via.
	scoped := appRow(t, `{}`, func(r *sqlc.Artifact) { r.Scopes = []string{pgstore.ScopeAppCouch} })
	rec3 := httptest.NewRecorder()
	s.writeAppConsentPage(rec3, httptest.NewRequest(http.MethodGet, "/app/demo-app", nil), scoped)
	if !strings.Contains(rec3.Body.String(), "automated process") {
		t.Fatal("app:couch row must be flagged as automated")
	}
}

func TestGrantAppConsentLastsThirtyDays(t *testing.T) {
	s, f := consentTestService()
	if err := s.grantAppConsent(httptest.NewRequest(http.MethodPost, "/x", nil), appRow(t, `{}`), "alice@example.com", false); err != nil {
		t.Fatal(err)
	}
	if until := f["alice@example.com|"+consentTestApp]; until.Before(time.Now().Add(29 * 24 * time.Hour)) {
		t.Fatalf("want a ~30-day record, got %v", until)
	}
}

// Once a version is consented the frame shows the app, so a further consent
// POST is the app's code talking through the relay; it must not grant slug scope.
func TestGrantAppConsentIgnoresAnAlreadyConsentedViewer(t *testing.T) {
	s, f := consentTestService()
	row := appRow(t, `{}`)
	f["alice@example.com|"+consentTestApp] = time.Now().Add(time.Hour)
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	if err := s.grantAppConsent(req, row, "alice@example.com", true); err != nil {
		t.Fatal(err)
	}
	if len(f) != 1 {
		t.Fatalf("an already-consented viewer must not widen to the slug, got %v", f)
	}
	if err := s.grantAppConsent(req, row, "bob@example.com", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := f["bob@example.com|"+consentTestApp]; !ok || len(f) != 2 {
		t.Fatalf("want one new record for bob, got %v", f)
	}
}

func TestGrantAppConsentRefusesAnonymous(t *testing.T) {
	s, f := consentTestService()
	if err := s.grantAppConsent(httptest.NewRequest(http.MethodPost, "/x", nil), appRow(t, `{}`), "", true); err == nil || len(f) != 0 {
		t.Fatalf("an anonymous grant must fail and record nothing: err=%v rows=%v", err, f)
	}
}

func TestExpireAppConsentCookies(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/s/x", nil)
	req.AddCookie(&http.Cookie{Name: AppConsentCookiePrefix + consentTestApp, Value: "v"})
	req.AddCookie(&http.Cookie{Name: AppConsentCookiePrefix + "sabc", Value: "v"})
	req.AddCookie(&http.Cookie{Name: "arti_session", Value: "keep"})
	rec := httptest.NewRecorder()
	ExpireAppConsentCookies(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)
	cs := rec.Result().Cookies()
	if len(cs) != 2 {
		t.Fatalf("want both consent cookies expired and nothing else touched, got %v", cs)
	}
	for _, c := range cs {
		if !strings.HasPrefix(c.Name, AppConsentCookiePrefix) || c.MaxAge >= 0 || c.Path != "/" {
			t.Fatalf("cookie not expired at Path=/: %+v", c)
		}
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
	s, _ := consentTestService()
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
