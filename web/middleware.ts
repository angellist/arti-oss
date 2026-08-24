import { NextRequest, NextResponse } from "next/server";
import {
  forwardHeaders,
  isFromFEProxy,
  isProxiedPath,
  proxyDestination,
} from "./lib/proxy-target";

// Redirect to /login when no session cookie is present. Validity is
// not checked here — Go is canonical; this just keeps the FE from
// rendering an "unauthorized" error wall on every page load.
//
// When the server is running with ARTI_AUTH_DISABLED=true, set
// NEXT_PUBLIC_ARTI_AUTH_DISABLED=true so the FE skips the redirect.
const AUTH_DISABLED = process.env.NEXT_PUBLIC_ARTI_AUTH_DISABLED === "true";

export function middleware(req: NextRequest) {
  // arti-server owns /api, /auth, /app, /mcp, /healthz, /openapi.yaml and
  // /.well-known. Forward them, resolving ARTI_API_URL per request so the
  // target is a runtime value (see lib/proxy-target.ts for why it cannot be a
  // next.config.ts rewrite).
  //
  // This must come before the session check: these requests are authenticated
  // by arti-server itself — Bearer tokens, OAuth callbacks, MCP clients — and
  // bouncing a cookie-less one to /auth/login would break them.
  //
  // forwardHeaders strips the oauth2-proxy identity assertions on the way
  // through: arti-server trusts those by position, and a request that reached
  // arti-web did not come through the auth subrequest that makes them
  // trustworthy.
  //
  if (isProxiedPath(req.nextUrl.pathname)) {
    // ...unless arti-server's own FE proxy is what sent it here. arti-server
    // reverse-proxies unmatched paths to arti-web, so a marked request is one
    // it has already declined; sending it back bounces the two pods until
    // something times out. arti-server's route guards normally stop this
    // upstream (cmd/arti-server/feproxy.go) — this covers a rollout where the
    // two Deployments are briefly on different versions.
    if (isFromFEProxy(req.headers)) {
      return new NextResponse(null, { status: 404 });
    }
    return NextResponse.rewrite(
      proxyDestination(req.nextUrl.pathname, req.nextUrl.search),
      { request: { headers: forwardHeaders(req.headers) } },
    );
  }

  if (AUTH_DISABLED) return NextResponse.next();

  const session = req.cookies.get("arti_session");
  if (session) return NextResponse.next();
  if (req.nextUrl.pathname.startsWith("/login")) return NextResponse.next();

  // Skip the intermediate /login page — by the time a request reaches
  // arti-web behind ProtectedIngress, oauth2-proxy has already verified
  // the user. arti-server's /auth/login handler mints arti_session
  // straight from the X-Auth-Request-* headers, so we just bounce there.
  const url = req.nextUrl.clone();
  url.pathname = "/auth/login";
  url.searchParams.set("return_to", req.nextUrl.pathname + req.nextUrl.search);
  return NextResponse.redirect(url);
}

export const config = {
  // Skip middleware on Next internals and public assets. Everything else
  // runs it, including the arti-server paths (/api, /auth, …) — middleware
  // is what forwards those now, so excluding them would 404 them in any
  // deployment where a request reaches arti-web directly.
  // comments-embed.js must stay public — a sandboxed served-HTML page loads
  // it as a subresource and can't send the session cookie.
  // logo.png likewise: it is the login page's own logo, so it is always
  // fetched by someone with no session, and next/image re-fetches it
  // server-side (cookie-less) to optimize it. Redirecting it made the
  // optimizer receive a 307 instead of an image and return 400, leaving the
  // logo broken on the one page guaranteed to be viewed unauthenticated.
  matcher: ["/((?!_next/|favicon|logo\\.png|login|comments-embed).*)"],
};
