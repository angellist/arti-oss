package main

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/go-chi/chi/v5"
)

// routerUnderTest mirrors the shape of runServe's router for the routes that
// matter to catch-all resolution: the real artifact/app mounts (so the test
// tracks the actual route table rather than a copy of it), representative
// auth/mcp routes, the guards, and finally the FE proxy catch-all.
func routerUnderTest() chi.Router {
	r := chi.NewRouter()

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	r.Get("/openapi.yaml", func(http.ResponseWriter, *http.Request) {})
	r.Mount("/.well-known", chi.NewRouter())
	r.Get("/auth/login", func(http.ResponseWriter, *http.Request) {})
	r.Post("/auth/cli/exchange", func(http.ResponseWriter, *http.Request) {})
	r.Get("/oauth/authorize", func(http.ResponseWriter, *http.Request) {})
	r.Handle("/mcp", http.NotFoundHandler())

	// nil service: Match resolves the route without invoking the handler.
	artifacts.Mount(r, nil)
	artifacts.MountApp(r, nil)

	mountFEProxyGuards(r)
	r.Handle("/*", http.NotFoundHandler())
	return r
}

func matchedPattern(t *testing.T, r chi.Router, method, path string) string {
	t.Helper()
	rctx := chi.NewRouteContext()
	mux, ok := r.(*chi.Mux)
	if !ok {
		t.Fatalf("router is %T, not *chi.Mux", r)
	}
	if !mux.Match(rctx, method, path) {
		return ""
	}
	return rctx.RoutePattern()
}

// The loop this guards against: arti-server reverse-proxies anything unmatched
// to arti-web, and arti-web forwards the arti-server-owned prefixes back. Every
// path below matches no real route, so before the guards each one was handed to
// arti-web — which is why the rig smoke test's POST /api/graphql surfaced as a
// 500 from arti-web's own proxy layer. Once arti-web's forward works, an
// unguarded fall-through is an infinite round trip instead of a 500.
func TestFEProxyGuardsClaimUnmatchedServerPaths(t *testing.T) {
	r := routerUnderTest()
	for _, tc := range []struct{ path, want string }{
		{"/api/graphql", "/api/*"},
		{"/api/gql", "/api/*"},
		{"/api", "/api"},
		{"/auth", "/auth"},
		{"/app", "/app"},
		{"/app/a/b/c", "/app/*"},
		{"/auth/typo", "/auth/*"},
		{"/auth/embed/mint", "/auth/*"},
		{"/mcp/.well-known/openid-configuration", "/mcp/*"},
	} {
		if got := matchedPattern(t, r, http.MethodGet, tc.path); got != tc.want {
			t.Errorf("GET %s matched %q, want %q (a %q match is a proxy loop)", tc.path, got, tc.want, "/*")
		}
	}
}

// The guards are wildcards registered at the same prefix as real routes, so the
// thing that would make them a catastrophe rather than a fix is shadowing. chi
// resolves static segments before path params and path params before a
// wildcard; this pins that, against the real route table, so a future chi bump
// or route addition can't silently 404 the API.
func TestFEProxyGuardsDoNotShadowRealRoutes(t *testing.T) {
	r := routerUnderTest()
	for _, tc := range []struct{ method, path, want string }{
		{http.MethodGet, "/api/artifacts", "/api/artifacts"},
		{http.MethodPost, "/api/artifacts", "/api/artifacts"},
		{http.MethodGet, "/api/artifacts/search", "/api/artifacts/search"},
		{http.MethodGet, "/api/artifacts/aggregates/browse", "/api/artifacts/aggregates/browse"},
		{http.MethodGet, "/api/artifacts/by-slug/my-doc", "/api/artifacts/by-slug/{slug}"},
		{http.MethodGet, "/api/artifacts/by-slug/my-doc/files/a/b.css", "/api/artifacts/by-slug/{slug}/files/*"},
		{http.MethodGet, "/api/artifacts/abc123", "/api/artifacts/{id}"},
		{http.MethodPatch, "/api/artifacts/abc123", "/api/artifacts/{id}"},
		{http.MethodGet, "/api/artifacts/abc123/files/app.css", "/api/artifacts/{id}/files/*"},
		{http.MethodGet, "/api/me", "/api/me"},
		{http.MethodGet, "/app/my-app", "/app/{ident}"},
		{http.MethodGet, "/app/my-app/3", "/app/{ident}/{version}"},
		{http.MethodGet, "/auth/login", "/auth/login"},
		{http.MethodPost, "/auth/cli/exchange", "/auth/cli/exchange"},
		{http.MethodGet, "/mcp", "/mcp"},
	} {
		if got := matchedPattern(t, r, tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s matched %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

// Page routes — and anything else arti-web owns — must still reach the FE.
// A guard list that over-reaches would blank the catalog.
func TestFEProxyStillServesFrontendPaths(t *testing.T) {
	r := routerUnderTest()
	for _, path := range []string{
		"/", "/browse", "/login", "/s/my-doc", "/s/v1.2.3/4",
		"/settings/keys", "/comments-embed.js", "/logo.png",
		// Exact-path server routes: arti-web forwards /healthz and
		// /openapi.yaml only as exact paths, so anything below them is the
		// FE's 404 to give and cannot bounce back.
		"/healthz/x", "/openapi.yaml/x",
	} {
		if got := matchedPattern(t, r, http.MethodGet, path); got != "/*" {
			t.Errorf("GET %s matched %q, want %q", path, got, "/*")
		}
	}
}

// The second line of defence: even if a guard is missed or the two Deployments
// are mid-rollout at different versions, arti-web can see that arti-server is
// the one asking and refuse to forward it back.
func TestFEProxyStampsLoopMarker(t *testing.T) {
	var got string
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(FEProxyMarkerHeader)
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newFEProxy(httputil.NewSingleHostReverseProxy(target))
	proxy.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/s/my-doc", nil))

	if got != "1" {
		t.Errorf("%s = %q on the proxied request, want %q", FEProxyMarkerHeader, got, "1")
	}
}

// A client must not be able to pre-set the marker and make arti-web decline a
// page it should render: the proxy sets, not appends.
func TestFEProxyMarkerOverwritesClientValue(t *testing.T) {
	var got []string
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Header.Values(FEProxyMarkerHeader)
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newFEProxy(httputil.NewSingleHostReverseProxy(target))
	req := httptest.NewRequest(http.MethodGet, "/s/my-doc", nil)
	req.Header.Add(FEProxyMarkerHeader, "spoof")
	proxy.ServeHTTP(httptest.NewRecorder(), req)

	if len(got) != 1 || got[0] != "1" {
		t.Errorf("%s = %v, want exactly [1]", FEProxyMarkerHeader, got)
	}
}

// With ARTI_SHARE_ENABLED off the share routes are not mounted, but the
// ingress still sends /share to arti-server. The guard has to claim those
// paths, or they fall to the FE proxy and an unauthenticated browser gets an
// SSO redirect instead of a 404 — which would break the feature's whole
// backout story (turn the flag off, links go dead).
//
// routerUnderTest deliberately does not call MountShare, so it *is* the
// flag-off router.
func TestShareGuardClaimsPathsWhenTheFeatureIsOff(t *testing.T) {
	r := routerUnderTest()
	for _, tc := range []struct{ path, want string }{
		{"/share/sometoken", "/share/*"},
		{"/share/sometoken/download", "/share/*"},
		{"/share/sometoken/_files/css/app.css", "/share/*"},
		{"/share", "/share"}, // bare form needs its own guard; /share/* misses it
	} {
		if got := matchedPattern(t, r, "GET", tc.path); got != tc.want {
			t.Errorf("GET %s matched %q, want %q (falling through to the FE proxy means a 302, not a 404)",
				tc.path, got, tc.want)
		}
	}
}

// With the feature on, the real routes must win over the guard — chi resolves
// static and param segments before a wildcard, but that only holds if the
// routes are registered on the same mux.
func TestShareRoutesBeatTheGuardWhenMounted(t *testing.T) {
	r := chi.NewRouter()
	var svc *artifacts.Service // nil: Match resolves the route without calling the handler
	svc.MountShare(r)
	mountFEProxyGuards(r)
	r.Handle("/*", http.NotFoundHandler())

	for _, tc := range []struct{ path, want string }{
		{"/share/tok", "/share/{token}"},
		{"/share/tok/download", "/share/{token}/download"},
		{"/share/tok/_files/css/app.css", "/share/{token}/_files/*"},
	} {
		if got := matchedPattern(t, r, "GET", tc.path); got != tc.want {
			t.Errorf("GET %s matched %q, want %q", tc.path, got, tc.want)
		}
	}
}
