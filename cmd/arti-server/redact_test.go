package main

import "testing"

func TestRedactLogPath(t *testing.T) {
	cases := []struct{ in, want string }{
		// embed sibling-file paths: the token segment is masked, rest preserved.
		{"/embed/front/_files/eyJhbGci.payload.sig/css/app.css", "/embed/front/_files/<redacted>/css/app.css"},
		{"/embed/front/_files/tok123/style.css", "/embed/front/_files/<redacted>/style.css"},
		{"/embed/front/_files/tok123/a/b/c.js", "/embed/front/_files/<redacted>/a/b/c.js"},
		// token with no trailing path segment still masked.
		{"/embed/front/_files/tok123", "/embed/front/_files/<redacted>"},
		{"/embed/front/_files/tok123/", "/embed/front/_files/<redacted>/"},
		// in-catalog sibling-file paths (filesBaseFor): same masking behavior.
		{"/api/artifacts/123/files-token/eyJhbGci.payload.sig/styles.css", "/api/artifacts/123/files-token/<redacted>/styles.css"},
		{"/api/artifacts/123/files-token/tok123", "/api/artifacts/123/files-token/<redacted>"},
		// unrelated paths untouched — including the doc route (secret is query-only).
		{"/embed/front?slug=x", "/embed/front?slug=x"},
		{"/embed/front/shell", "/embed/front/shell"},
		{"/api/artifacts/123/files/app.css", "/api/artifacts/123/files/app.css"},
		{"/healthz", "/healthz"},
	}
	for _, c := range cases {
		if got := redactLogPath(c.in); got != c.want {
			t.Errorf("redactLogPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
