package artifacts

import (
	"strings"
	"testing"
)

func TestInjectComments(t *testing.T) {
	s := NewService(nil, "http://x", nil, nil)
	s.SetEmbedTokenFn(func(email, artifactID, name, picture string) (string, error) { return "tok-" + artifactID, nil })

	html := []byte("<html><body><h1>hi</h1></body></html>")

	// injects into text/html, before </body>, with the bundle + config
	out := string(s.injectComments(html, "text/html; charset=utf-8", "aaa", "me@x.com", "Me Test", "https://pic.example/me.jpg"))
	if !strings.Contains(out, `src="/comments-embed.js"`) {
		t.Fatalf("expected bundle script, got %q", out)
	}
	if !strings.Contains(out, `"token":"tok-aaa"`) || !strings.Contains(out, `"artifactId":"aaa"`) || !strings.Contains(out, `"email":"me@x.com"`) {
		t.Fatalf("config blob wrong: %q", out)
	}
	if strings.Index(out, "comments-embed.js") > strings.Index(out, "</body>") {
		t.Fatalf("overlay must be injected before </body>")
	}
	if !strings.Contains(out, `"picture":"https://pic.example/me.jpg"`) {
		t.Fatalf("expected picture in me blob, got %q", out)
	}

	// no-ops for non-HTML
	if got := string(s.injectComments([]byte("body"), "text/plain", "aaa", "me@x.com", "", "")); got != "body" {
		t.Fatalf("non-html should be untouched, got %q", got)
	}
	// no-ops for unauthenticated
	if got := string(s.injectComments(html, "text/html", "aaa", "", "", "")); string(html) != got {
		t.Fatalf("no email should be untouched")
	}
	// no-ops when injection disabled
	s2 := NewService(nil, "http://x", nil, nil)
	if got := string(s2.injectComments(html, "text/html", "aaa", "me@x.com", "", "")); string(html) != got {
		t.Fatalf("nil embedToken should be untouched")
	}
}

// TestFilesBaseFor pins that a token-scoped path is only ever returned when
// BOTH the minter (appToken) and the verifier (appTokenVerify) are wired —
// minting a files-token URL that the verifier-less route would 404 is worse
// than the cookie-gated fallback it's supposed to replace.
func TestFilesBaseFor(t *testing.T) {
	const cookiePath = "/api/artifacts/aaa/files/"

	mint := func(email, artifactID string) (string, error) { return "tok-" + artifactID, nil }
	verify := func(tok string) (email, artifactID string, err error) { return "me@x.com", "aaa", nil }

	s := NewService(nil, "http://x", nil, nil)
	if got := s.filesBaseFor("me@x.com", "aaa", "index.html"); got != cookiePath {
		t.Fatalf("neither wired: want cookie path, got %q", got)
	}

	s.SetAppTokenFn(mint)
	if got := s.filesBaseFor("me@x.com", "aaa", "index.html"); got != cookiePath {
		t.Fatalf("minter only (no verifier): want cookie path (verifier-less route would 404), got %q", got)
	}

	s2 := NewService(nil, "http://x", nil, nil)
	s2.SetAppTokenVerifyFn(verify)
	if got := s2.filesBaseFor("me@x.com", "aaa", "index.html"); got != cookiePath {
		t.Fatalf("verifier only (no minter): want cookie path, got %q", got)
	}

	s.SetAppTokenVerifyFn(verify)
	if got, want := s.filesBaseFor("me@x.com", "aaa", "index.html"), "/api/artifacts/aaa/files-token/tok-aaa/"; got != want {
		t.Fatalf("both wired, root-level file: want %q, got %q", want, got)
	}

	// A nested file's <base> must point at ITS OWN directory, not the package
	// root, or a document-relative sibling ref resolves to the wrong path —
	// caught by Cursor Bugbot on the initial version of this fix.
	if got, want := s.filesBaseFor("me@x.com", "aaa", "docs/sub/page.html"), "/api/artifacts/aaa/files-token/tok-aaa/docs/sub/"; got != want {
		t.Fatalf("both wired, nested file: want %q, got %q", want, got)
	}
	if got, want := s2.filesBaseFor("me@x.com", "aaa", "docs/page.html"), "/api/artifacts/aaa/files/docs/"; got != want {
		t.Fatalf("cookie fallback, nested file: want %q, got %q", want, got)
	}

	if got := s.filesBaseFor("", "aaa", "index.html"); got != cookiePath {
		t.Fatalf("no caller: want cookie path, got %q", got)
	}

	s3 := NewService(nil, "http://x", nil, nil)
	s3.SetAppTokenFn(func(email, artifactID string) (string, error) { return "", errBadRequest("nope") })
	s3.SetAppTokenVerifyFn(verify)
	if got := s3.filesBaseFor("me@x.com", "aaa", "index.html"); got != cookiePath {
		t.Fatalf("minter error: want cookie path, got %q", got)
	}
}

func TestDirOf(t *testing.T) {
	cases := map[string]string{
		"index.html":      "",
		"/index.html":     "",
		"docs/page.html":  "docs/",
		"docs/sub/x.html": "docs/sub/",
		"/docs/page.html": "docs/",
		"":                "",
	}
	for in, want := range cases {
		if got := dirOf(in); got != want {
			t.Errorf("dirOf(%q) = %q, want %q", in, got, want)
		}
	}
}
