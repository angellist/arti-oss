package artifacts

import (
	"context"
	"strings"
	"testing"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

// The /s viewer shows a "newer version" strip when the URL pins an older
// version; /app is chrome-less, so the equivalent has to be injected into the
// app document itself. These pin the injector's contract.
func TestInjectStaleAppBanner(t *testing.T) {
	html := []byte(`<html><body><h1>app</h1></body></html>`)
	notice := &staleNotice{Current: 2, Latest: 5, LatestURL: "/app/my-app"}

	out := string(injectStaleAppBanner(html, "text/html; charset=utf-8", notice))
	if !strings.Contains(out, `window.__ARTI_STALE__=`) {
		t.Fatalf("config blob not injected: %s", out)
	}
	if !strings.Contains(out, `"cur":2`) || !strings.Contains(out, `"latest":5`) || !strings.Contains(out, `"href":"/app/my-app"`) {
		t.Fatalf("config blob wrong: %s", out)
	}
	if !strings.Contains(out, "arti-stale-strip") {
		t.Fatalf("strip builder not injected: %s", out)
	}
	if strings.Index(out, "__ARTI_STALE__") > strings.Index(out, "</body>") {
		t.Fatal("strip must be injected before </body>")
	}
	if !strings.Contains(out, "<h1>app</h1>") {
		t.Fatal("app body must be preserved")
	}

	// Nothing to warn about → byte-identical page.
	if got := string(injectStaleAppBanner(html, "text/html", nil)); got != string(html) {
		t.Fatalf("nil notice must be a no-op, got %q", got)
	}
	// Non-HTML entries (a JSON/asset entry point) are untouched.
	if got := string(injectStaleAppBanner([]byte(`{"a":1}`), "application/json", notice)); got != `{"a":1}` {
		t.Fatalf("non-html must be untouched, got %q", got)
	}
	// No </body> (fragment-style entry) → appended at the end rather than dropped.
	frag := string(injectStaleAppBanner([]byte(`<div>app</div>`), "text/html", notice))
	if !strings.HasPrefix(frag, `<div>app</div>`) || !strings.Contains(frag, "__ARTI_STALE__") {
		t.Fatalf("bodyless html should get the strip appended, got %q", frag)
	}
}

// staleAppNotice must answer without touching the store when there is nothing
// to compare against — a slugless artifact has no "latest" to point at. The nil
// store is the assertion: a lookup here would panic.
func TestStaleAppNoticeNeedsSlugAndVersion(t *testing.T) {
	s := NewService(nil, "http://x", nil, nil)
	v := int32(2)
	slug := "my-app"
	empty := ""

	for name, row := range map[string]sqlc.Artifact{
		"no slug":    {Version: &v},
		"empty slug": {NamedSlug: &empty, Version: &v},
		"no version": {NamedSlug: &slug},
	} {
		if got := s.staleAppNotice(context.Background(), row, "me@x.com"); got != nil {
			t.Errorf("%s: want nil notice, got %+v", name, got)
		}
	}
}

// The strip's runtime has to survive an app that re-renders over document.body
// and must not leave its ResizeObserver running once dismissed — both are
// silent failures in a browser, so pin the wiring here.
func TestStaleBannerRuntimeWiring(t *testing.T) {
	for _, want := range []string{
		"MutationObserver",                // re-mount after an SPA clobbers body
		"function stopRO()",               // observer teardown exists
		"function stopMO()",               // re-mount watcher teardown exists
		"stopRO(); stopMO(); clearVar();", // dismiss tears both down BEFORE clearing the var
		"font-family:inherit",             // longhands: the `font` shorthand needs a family term
		"--arti-top-strip",                // the inset contract apps can read
	} {
		if !strings.Contains(staleBannerJS, want) {
			t.Errorf("staleBannerJS missing %q", want)
		}
	}
	if strings.Contains(staleBannerJS, "innerHTML") {
		t.Error("strip must be built with DOM APIs, never innerHTML")
	}
}

// A slug is user-supplied, so it must not be able to break out of the
// <script> that carries the config. json.Marshal's default escaping is the
// mechanism — this pins it.
func TestInjectStaleAppBannerEscapesHref(t *testing.T) {
	notice := &staleNotice{Current: 1, Latest: 2, LatestURL: `/app/x</script><script>alert(1)</script>`}
	out := string(injectStaleAppBanner([]byte("<html><body></body></html>"), "text/html", notice))
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Fatalf("href escaped out of the config script: %s", out)
	}
	if !strings.Contains(out, `</script>`) {
		t.Fatalf("expected escaped </script> in the injected config: %s", out)
	}
}

// The served-vs-latest comparison is the whole feature: invert it and every app
// page grows a bogus strip (or a stale one never gets it). It lives in a pure
// function precisely so it can be pinned without a store.
func TestStaleNoticeFrom(t *testing.T) {
	slug, empty := "my-app", ""
	v := func(n int32) *int32 { return &n }
	row := func(s *string, n *int32) sqlc.Artifact { return sqlc.Artifact{NamedSlug: s, Version: n} }

	for name, tc := range map[string]struct {
		row, latest sqlc.Artifact
		want        *staleNotice
	}{
		"older than latest": {row(&slug, v(2)), row(&slug, v(7)),
			&staleNotice{Current: 2, Latest: 7, LatestURL: "/app/my-app"}},
		"one behind":         {row(&slug, v(6)), row(&slug, v(7)), &staleNotice{Current: 6, Latest: 7, LatestURL: "/app/my-app"}},
		"is the latest":      {row(&slug, v(7)), row(&slug, v(7)), nil},
		"ahead of latest":    {row(&slug, v(9)), row(&slug, v(7)), nil}, // pinned read of a since-archived head
		"latest unversioned": {row(&slug, v(2)), row(&slug, nil), nil},
		"row unversioned":    {row(&slug, nil), row(&slug, v(7)), nil},
		"slugless":           {row(nil, v(2)), row(&slug, v(7)), nil},
		"empty slug":         {row(&empty, v(2)), row(&slug, v(7)), nil},
	} {
		got := staleNoticeFrom(tc.row, tc.latest)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%s: want nil, got %+v", name, got)
		case tc.want != nil && got == nil:
			t.Errorf("%s: want %+v, got nil", name, tc.want)
		case tc.want != nil && *got != *tc.want:
			t.Errorf("%s: want %+v, got %+v", name, tc.want, got)
		}
	}
}

// The slug is percent-escaped into the href so it can't traverse out of /app/.
func TestStaleNoticeFromEscapesSlug(t *testing.T) {
	slug := "../a b/c"
	got := staleNoticeFrom(sqlc.Artifact{NamedSlug: &slug, Version: func() *int32 { n := int32(1); return &n }()},
		sqlc.Artifact{NamedSlug: &slug, Version: func() *int32 { n := int32(2); return &n }()})
	if got == nil || strings.Contains(got.LatestURL, " ") || !strings.HasPrefix(got.LatestURL, "/app/") {
		t.Fatalf("unescaped slug in href: %+v", got)
	}
}
