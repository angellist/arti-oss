package main

import (
	"net/http"
	"net/http/httputil"

	"github.com/go-chi/chi/v5"
)

// FEProxyMarkerHeader is set by arti-server on every request it hands to the
// Next.js FE reverse proxy, and is arti-web's signal to stop.
//
// The two pods proxy to each other by design: arti-server owns the API and
// reverse-proxies everything else to arti-web (ARTI_WEB_URL), while arti-web
// forwards the arti-server-owned prefixes back (ARTI_API_URL, see
// web/lib/proxy-target.ts). Those two rules only compose because each side
// declines the other's paths. If arti-server ever hands arti-web a path
// arti-web considers arti-server's, the request ping-pongs forever.
//
// feProxyGuards below is what prevents that. This header is the second line:
// a request carrying it has already been declined by arti-server, so arti-web
// renders its own 404 instead of forwarding it back — which keeps a
// version-skewed rollout (new arti-web, old arti-server, or the reverse) from
// looping while the two Deployments converge.
const FEProxyMarkerHeader = "X-Arti-Fe-Proxy"

// feProxyGuardPrefixes are the path prefixes arti-server owns outright. An
// unmatched path under one of them must 404 here rather than fall through to
// the FE reverse proxy.
//
// chi's `/*` catch-all is greedy about exactly the paths you don't think about:
// `/api/graphql`, `/auth/typo`, `/app/a/b/c` match no route, so before these
// guards they were all handed to arti-web. That is how the depot rig smoke test
// saw `POST /api/graphql -> 500 Failed to proxy http://localhost:8095/...` —
// the 500 came from arti-web, one hop further along than it looked, and the
// only reason it terminated at all was that arti-web's forward was broken.
// Fixing the forward without these guards would have converted that 500 into an
// infinite loop between the two pods.
//
// `/mcp` is guarded here too, replacing the standalone `/mcp/*` rule this list
// generalizes: an MCP client probing `/mcp/.well-known/openid-configuration`
// used to fall into the FE's SSO redirect chain and die with "too many
// redirects".
//
// Not listed, because they cannot loop:
//   - `/healthz`, `/openapi.yaml` — arti-web forwards these as exact paths
//     only, so `/healthz/x` reaching the FE is a plain Next.js 404.
//   - `/.well-known` — chi Mounts a subrouter, which answers its own 404
//     before the catch-all is ever consulted.
//
// `/share` is listed UNCONDITIONALLY, independent of ARTI_SHARE_ENABLED. The
// ingress routes /share to arti-server whatever the flag says, so with the
// feature off the request still arrives here; without this guard it would fall
// through to the FE reverse proxy and come back as an SSO redirect instead of
// a 404. The backout story depends on that difference.
var feProxyGuardPrefixes = []string{"/api", "/app", "/auth", "/mcp", "/share"}

// feProxyBareGuardPrefixes are the prefixes whose BARE form is also unrouted,
// and so needs its own guard: `/api/*` does not match `/api`.
//
// This matters because arti-web treats a bare prefix as arti-server's too
// (isProxiedPath matches `pathname === prefix`), so bare `/app` was the one
// path that still round-tripped after the wildcard guards went in.
//
// `/mcp` is deliberately absent: the bare path IS the MCP endpoint, and
// re-registering it here would replace the real handler with a 404.
var feProxyBareGuardPrefixes = []string{"/api", "/app", "/auth", "/share"}

// mountFEProxyGuards registers a 404 catch-all under each prefix arti-server
// owns. Must be called before the `/*` FE proxy route.
//
// These do not shadow the real routes: chi resolves static segments before
// path params and path params before a wildcard, so `/api/artifacts` and
// `/app/{ident}` still win — feproxy_test.go pins that against the actual
// route table.
func mountFEProxyGuards(root chi.Router) {
	for _, prefix := range feProxyGuardPrefixes {
		root.Handle(prefix+"/*", http.NotFoundHandler())
	}
	for _, prefix := range feProxyBareGuardPrefixes {
		root.Handle(prefix, http.NotFoundHandler())
	}
}

// newFEProxy builds the reverse proxy to arti-web, stamping every outbound
// request with FEProxyMarkerHeader.
func newFEProxy(proxy *httputil.ReverseProxy) *httputil.ReverseProxy {
	inner := proxy.Director
	proxy.Director = func(r *http.Request) {
		inner(r)
		r.Header.Set(FEProxyMarkerHeader, "1")
	}
	return proxy
}
