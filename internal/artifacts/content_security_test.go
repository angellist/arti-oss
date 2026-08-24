package artifacts

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// H3: scriptable non-HTML artifact bytes (image/svg+xml, xml, …) must be served
// under a script-less `sandbox` CSP so an uploaded SVG/XML can't run JS on the
// arti origin (stored XSS). text/html keeps its allow-scripts opaque sandbox;
// raster images / PDF / audio / video render normally (no in-page script).
func TestSetContentSecurity_H3(t *testing.T) {
	cases := []struct {
		ct      string
		wantCSP string
	}{
		{"text/html; charset=utf-8", "sandbox allow-scripts allow-top-navigation-by-user-activation allow-downloads"},
		{"image/svg+xml", "sandbox"},
		{"application/xml", "sandbox"},
		{"text/xml", "sandbox"},
		{"application/json", "sandbox"},
		{"text/plain; charset=utf-8", "sandbox"},
		{"application/octet-stream", "sandbox"},
		{"image/png", ""},
		{"image/jpeg", ""},
		{"image/webp", ""},
		{"application/pdf", ""},
		{"video/mp4", ""},
		{"audio/mpeg", ""},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		setContentSecurity(w, c.ct)
		if got := w.Header().Get("Content-Security-Policy"); got != c.wantCSP {
			t.Errorf("setContentSecurity(%q): CSP = %q, want %q", c.ct, got, c.wantCSP)
		}
		if w.Header().Get("Content-Type") != c.ct {
			t.Errorf("setContentSecurity(%q): Content-Type not set", c.ct)
		}
	}
}

// isScriptSafeMedia must treat image/svg+xml as UNSAFE (it's a scriptable
// document despite the image/* type), while raster images / PDF / A-V are safe.
func TestIsScriptSafeMedia(t *testing.T) {
	safe := []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf", "audio/mpeg", "video/mp4", "image/png; charset=binary"}
	unsafe := []string{"image/svg+xml", "application/xml", "text/xml", "text/html", "application/json", "text/plain", "application/octet-stream", "application/xhtml+xml"}
	for _, ct := range safe {
		if !isScriptSafeMedia(ct) {
			t.Errorf("isScriptSafeMedia(%q) = false, want true", ct)
		}
	}
	for _, ct := range unsafe {
		if isScriptSafeMedia(ct) {
			t.Errorf("isScriptSafeMedia(%q) = true, want false", ct)
		}
	}
}

// H3 (embed surfaces): scriptable non-HTML gets the script-less sandbox plus
// frame-ancestors; safe media keeps frame-ancestors only.
func TestSetContentSecurityEmbed_H3Scriptable(t *testing.T) {
	const fa = "'self' https://app.frontapp.com"
	cases := []struct {
		ct      string
		wantCSP string
	}{
		{"image/svg+xml", "sandbox; frame-ancestors " + fa},
		{"application/xml", "sandbox; frame-ancestors " + fa},
		{"image/png", "frame-ancestors " + fa},
		{"application/pdf", "frame-ancestors " + fa},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		setContentSecurityEmbed(w, c.ct, fa)
		if got := w.Header().Get("Content-Security-Policy"); got != c.wantCSP {
			t.Errorf("setContentSecurityEmbed(%q): CSP = %q, want %q", c.ct, got, c.wantCSP)
		}
		if w.Header().Get("X-Frame-Options") != "" {
			t.Errorf("setContentSecurityEmbed(%q): X-Frame-Options should be dropped", c.ct)
		}
	}
}

// setContentSecurityMaybeFullPage grants popups ONLY for HTML in the full-page
// viewer context (?ctx=fullpage), so a report's target="_blank" / window.open
// links open on a normal click. Two invariants matter for security: it must
// NEVER add allow-same-origin (that would expose arti_session to uploaded HTML),
// and — the F7 guard — a forged ?ctx=fullpage on any non-HTML type (especially a
// scriptable SVG/XML) must fall through to the normal script-safe CSP, never
// gaining scripts or popups. Non-full-page HTML differs only in the popups.
//
// Every HTML body also carries `frame-ancestors` (the APP allowlist) and drops
// X-Frame-Options, so the nested content frame inside the full-page viewer
// survives being embedded by a trusted internal origin — XFO is checked against
// the whole ancestor chain and would block it. Non-HTML keeps XFO.
func TestSetContentSecurityMaybeFullPage(t *testing.T) {
	const fa = "'self' https://*.internal.example.com"
	const fullPageHTML = "sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox allow-top-navigation-by-user-activation allow-downloads; frame-ancestors " + fa
	const normalHTML = "sandbox allow-scripts allow-top-navigation-by-user-activation allow-downloads; frame-ancestors " + fa
	s := &Service{}
	s.SetAppFrameAncestors(fa)
	cases := []struct {
		ct       string
		fullPage bool
		wantCSP  string
	}{
		// Full-page HTML: popups granted.
		{"text/html; charset=utf-8", true, fullPageHTML},
		{"text/html", true, fullPageHTML},
		// Non-full-page HTML: no popups, same framing rules.
		{"text/html; charset=utf-8", false, normalHTML},
		// F7 guard: a forged ?ctx=fullpage on a scriptable non-HTML type must NOT
		// widen the sandbox — SVG/XML stay script-less, never script-or-popup-enabled.
		{"image/svg+xml", true, "sandbox"},
		{"application/xml", true, "sandbox"},
		{"application/json", true, "sandbox"},
		{"text/plain; charset=utf-8", true, "sandbox"},
		// Raster/PDF: no CSP either way.
		{"image/png", true, ""},
		{"application/pdf", true, ""},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		w.Header().Set("X-Frame-Options", "SAMEORIGIN") // as the global middleware would
		s.setContentSecurityMaybeFullPage(w, c.ct, c.fullPage)
		got := w.Header().Get("Content-Security-Policy")
		if got != c.wantCSP {
			t.Errorf("setContentSecurityMaybeFullPage(%q, fullPage=%v): CSP = %q, want %q", c.ct, c.fullPage, got, c.wantCSP)
		}
		if strings.Contains(got, "allow-same-origin") {
			t.Errorf("setContentSecurityMaybeFullPage(%q): must NEVER grant allow-same-origin, got %q", c.ct, got)
		}
		if w.Header().Get("Content-Type") != c.ct {
			t.Errorf("setContentSecurityMaybeFullPage(%q): Content-Type not set", c.ct)
		}
		// HTML must hand framing to frame-ancestors; everything else keeps XFO.
		isHTML := strings.HasPrefix(c.ct, "text/html")
		if xfo := w.Header().Get("X-Frame-Options"); (xfo == "") != isHTML {
			t.Errorf("setContentSecurityMaybeFullPage(%q): X-Frame-Options = %q (isHTML=%v)", c.ct, xfo, isHTML)
		}
	}
}

// With no APP allowlist configured (the OSS/default deployment), artifact HTML
// falls back to same-origin-only framing — the pre-existing X-Frame-Options
// behavior, now expressed as frame-ancestors.
func TestArtifactFrameAncestors_DefaultsToSelf(t *testing.T) {
	s := &Service{}
	w := httptest.NewRecorder()
	s.setContentSecurityMaybeFullPage(w, "text/html; charset=utf-8", true)
	if got := w.Header().Get("Content-Security-Policy"); !strings.HasSuffix(got, "; frame-ancestors 'self'") {
		t.Errorf("CSP = %q, want it to end with frame-ancestors 'self'", got)
	}
}

// The three CSP writers that serve a viewer-reachable HTML body — the entry
// document, an APP, and the files-token route a PACKAGE's in-content links
// navigate to via the injected <base> — must resolve the same default
// allowlist. A hardcoded 'self' on any one of them re-breaks embedding at the
// next click (the files-token route did exactly that).
func TestHTMLBodyFrameAncestors_ShareOneDefault(t *testing.T) {
	const fa = "'self' https://*.internal.example.com"
	s := &Service{}
	s.SetAppFrameAncestors(fa)
	writers := map[string]func(w *httptest.ResponseRecorder){
		"entry document": func(w *httptest.ResponseRecorder) {
			s.setContentSecurityMaybeFullPage(w, "text/html; charset=utf-8", true)
		},
		"APP": func(w *httptest.ResponseRecorder) {
			s.setContentSecurityApp(w, "text/html; charset=utf-8")
		},
		"files-token (in-content navigation)": func(w *httptest.ResponseRecorder) {
			setContentSecurityEmbed(w, "text/html; charset=utf-8", s.artifactFrameAncestors())
		},
	}
	for name, write := range writers {
		w := httptest.NewRecorder()
		w.Header().Set("X-Frame-Options", "SAMEORIGIN") // as the global middleware would
		write(w)
		if got := w.Header().Get("Content-Security-Policy"); !strings.HasSuffix(got, "; frame-ancestors "+fa) {
			t.Errorf("%s: CSP = %q, want it to end with frame-ancestors %s", name, got, fa)
		}
		if got := w.Header().Get("X-Frame-Options"); got != "" {
			t.Errorf("%s: X-Frame-Options = %q, want it dropped so frame-ancestors is authoritative", name, got)
		}
	}
}
