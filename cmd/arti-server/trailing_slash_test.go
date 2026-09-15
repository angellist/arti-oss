package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/go-chi/chi/v5"
)

func serveGuard(t *testing.T, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	http.HandlerFunc(trailingSlashRedirect).ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// The bug this fixes: /app/{ident} is an exact chi route, so a trailing slash
// fell to the /app/* guard and 404'd — the same URL worked without the slash.
func TestTrailingSlashRedirect(t *testing.T) {
	for _, tc := range []struct {
		method, path, want string
		code               int
	}{
		{http.MethodGet, "/app/my-app/", "/app/my-app", http.StatusMovedPermanently},
		{http.MethodGet, "/app/my-app/3/", "/app/my-app/3", http.StatusMovedPermanently},
		{http.MethodGet, "/api/artifacts/by-slug/my-doc/", "/api/artifacts/by-slug/my-doc", http.StatusMovedPermanently},
		{http.MethodHead, "/app/my-app/", "/app/my-app", http.StatusMovedPermanently},
		// A 301 lets a client re-issue a POST as a GET; 308 does not.
		{http.MethodPost, "/api/artifacts/", "/api/artifacts", http.StatusPermanentRedirect},
		{http.MethodDelete, "/api/artifacts/abc/", "/api/artifacts/abc", http.StatusPermanentRedirect},
	} {
		rec := serveGuard(t, tc.method, tc.path)
		if rec.Code != tc.code || rec.Header().Get("Location") != tc.want {
			t.Errorf("%s %s -> %d %q, want %d %q",
				tc.method, tc.path, rec.Code, rec.Header().Get("Location"), tc.code, tc.want)
		}
	}
}

func TestTrailingSlashRedirectPreservesQuery(t *testing.T) {
	rec := serveGuard(t, http.MethodGet, "/app/my-app/?v=full&x=1")
	if got := rec.Header().Get("Location"); got != "/app/my-app?v=full&x=1" {
		t.Errorf("Location = %q, want %q", got, "/app/my-app?v=full&x=1")
	}
}

// A path the guard claims for any other reason must still 404 — the guards are
// what stop an unrouted /api or /app path reaching the FE proxy and looping.
func TestTrailingSlashRedirectStill404sEverythingElse(t *testing.T) {
	for _, path := range []string{"/app", "/app/a/b/c", "/api/graphql", "/auth/typo"} {
		if rec := serveGuard(t, http.MethodGet, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s -> %d %q, want 404", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// An open redirect here would be reachable unauthenticated: the guards are
// registered outside the auth group. Every Location must stay same-origin, and
// the redirect must terminate rather than bounce.
func TestTrailingSlashRedirectStaysSameOrigin(t *testing.T) {
	for _, path := range []string{"//evil.com/", `/\evil.com/`, `//\/evil.com/`, "///"} {
		rec := serveGuard(t, http.MethodGet, path)
		loc := rec.Header().Get("Location")
		if len(loc) > 1 && (loc[1] == '/' || loc[1] == '\\') {
			t.Errorf("GET %s -> Location %q is protocol-relative", path, loc)
		}
		if loc == path {
			t.Errorf("GET %s redirects to itself", path)
		}
	}
}

// End to end against the real route table: the redirect target must resolve to
// the app route, not back to the /app/* guard it used to hit.
func TestTrailingSlashRedirectLandsOnTheAppRoute(t *testing.T) {
	r := routerUnderTest()
	if got := matchedPattern(t, r, http.MethodGet, "/app/my-app/"); got != "/app/*" {
		t.Fatalf("/app/my-app/ matched %q, want the guard %q", got, "/app/*")
	}
	loc := serveGuard(t, http.MethodGet, "/app/my-app/").Header().Get("Location")
	if got := matchedPattern(t, r, http.MethodGet, loc); got != "/app/{ident}" {
		t.Errorf("redirect target %q matched %q, want %q", loc, got, "/app/{ident}")
	}
}

// A trailing slash is a meaningful empty wildcard segment on the package-file
// routes: pkgzip.resolveCandidates("") is index.html, and these directory URLs
// are the injected <base href>. They must match their own route, so the guard —
// and the redirect behind it — never sees them. Redirecting `/files/` would
// serve the JSON listing at `/files` instead of the package index, and
// `/share/{token}/_files/` would 404 outright.
func TestPackageDirectoryRootsNeverReachTheGuard(t *testing.T) {
	r := chi.NewRouter()
	var svc *artifacts.Service // nil: Match resolves the route without calling the handler
	artifacts.Mount(r, svc)
	svc.MountFileToken(r)
	svc.MountShare(r)
	mountFEProxyGuards(r)
	r.Handle("/*", http.NotFoundHandler())

	for _, tc := range []struct{ path, want string }{
		{"/api/artifacts/abc123/files/", "/api/artifacts/{id}/files/*"},
		{"/api/artifacts/by-slug/my-doc/files/", "/api/artifacts/by-slug/{slug}/files/*"},
		{"/api/artifacts/abc123/files-token/tok/", "/api/artifacts/{id}/files-token/{token}/*"},
		{"/share/tok/_files/", "/share/{token}/_files/*"},
		// Nested directories resolve the same way, one level down.
		{"/api/artifacts/abc123/files/guide/", "/api/artifacts/{id}/files/*"},
	} {
		if got := matchedPattern(t, r, http.MethodGet, tc.path); got != tc.want {
			t.Errorf("GET %s matched %q, want %q (a guard match redirects away the package index)",
				tc.path, got, tc.want)
		}
	}

	// Served, not just matched: reaching a route has to beat the redirect, so
	// hoisting it back to a root middleware fails here. The nil service gets as
	// far as the id parse and answers 400 — any 3xx means the request never
	// reached the handler at all.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/artifacts/abc123/files/", nil))
	if rec.Code >= 300 && rec.Code < 400 {
		t.Errorf("GET /api/artifacts/abc123/files/ -> %d %q, want the file route, not a redirect",
			rec.Code, rec.Header().Get("Location"))
	}
}
