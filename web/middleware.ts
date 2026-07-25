import { NextRequest, NextResponse } from "next/server";

// Redirect to /login when no session cookie is present. Validity is
// not checked here — Go is canonical; this just keeps the FE from
// rendering an "unauthorized" error wall on every page load.
//
// When the server is running with ARTI_AUTH_DISABLED=true, set
// NEXT_PUBLIC_ARTI_AUTH_DISABLED=true so the FE skips the redirect.
const AUTH_DISABLED = process.env.NEXT_PUBLIC_ARTI_AUTH_DISABLED === "true";

export function middleware(req: NextRequest) {
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
  // Skip middleware on Next internals and the API/auth proxy paths
  // (the Go binary owns those when arti-web is run behind it; in local
  // dev we hit Go directly anyway).
  // comments-embed.js must stay public — a sandboxed served-HTML page loads
  // it as a subresource and can't send the session cookie.
  matcher: ["/((?!_next/|favicon|api/|auth/|login|comments-embed).*)"],
};
