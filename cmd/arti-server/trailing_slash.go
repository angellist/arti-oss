package main

import (
	"net/http"
	"net/url"
	"strings"
)

// trailingSlashRedirect canonicalizes `/path/` to `/path`. It backs the prefix
// guards in feproxy.go, so it answers only paths that matched no real route.
//
// That placement is load-bearing, not incidental. A trailing slash is a
// meaningful empty wildcard segment on the package-file routes:
// `/api/artifacts/{id}/files/` resolves to the package's index.html, and that
// URL is also the injected `<base href>`. Those requests match their own route
// and so never reach a guard. Redirecting them from a root middleware would
// send `/files/` to the JSON listing at `/files`, and `/share/{token}/_files/`
// to a path with no route at all.
func trailingSlashRedirect(w http.ResponseWriter, r *http.Request) {
	// Backslashes normalized and leading slashes collapsed, so a crafted path
	// can't make the Location protocol-relative (`//evil.com`). The guards sit
	// outside the auth group, so this is reachable unauthenticated.
	clean := "/" + strings.Trim(strings.ReplaceAll(r.URL.Path, `\`, "/"), "/")
	if clean == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	target := url.URL{Path: clean, RawQuery: r.URL.RawQuery}

	// 308 off the GET/HEAD path: a 301 lets a client turn a POST into a GET.
	code := http.StatusMovedPermanently
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		code = http.StatusPermanentRedirect
	}
	http.Redirect(w, r, target.String(), code)
}
