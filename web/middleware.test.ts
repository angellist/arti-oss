import { describe, it, expect } from "vitest";
import { config } from "./middleware";

// The matcher decides which paths get the "no session -> bounce to
// /auth/login" treatment. Getting it wrong is invisible in tests that only
// exercise pages: /logo.png was being redirected, so the login page's own
// logo 404'd for exactly the users who see that page, and next/image's
// cookie-less server-side re-fetch got a 307 and returned 400.
const matches = (pathname: string) => {
  const pattern = config.matcher[0];
  return new RegExp(`^${pattern}$`).test(pathname);
};

describe("middleware matcher", () => {
  it("guards app routes, so an unauthenticated visitor gets sent to login", () => {
    for (const p of ["/", "/browse", "/archived", "/roles", "/settings", "/settings/keys", "/s/my-doc"]) {
      expect(matches(p), `${p} must be guarded`).toBe(true);
    }
  });

  // A slug may contain dots, so the assets exemption must not be written as
  // a blanket "anything with a file extension is public" rule — that would
  // silently stop guarding artifact pages.
  it("still guards artifact paths whose slug contains dots", () => {
    for (const p of ["/s/v1.2.3", "/s/release.notes", "/s/v1.2.3/4"]) {
      expect(matches(p), `${p} must be guarded`).toBe(true);
    }
  });

  it("exempts assets that are fetched without a session cookie", () => {
    // The login logo: requested by an unauthenticated browser AND re-fetched
    // server-side by the image optimizer, neither of which sends the cookie.
    expect(matches("/logo.png"), "/logo.png must stay public").toBe(false);
    // A sandboxed served-HTML page loads this as a subresource.
    expect(matches("/comments-embed.js"), "comments-embed must stay public").toBe(false);
  });

  it("exempts Next internals, public assets and the login page", () => {
    for (const p of ["/_next/static/chunk.js", "/_next/image", "/favicon.ico", "/login"]) {
      expect(matches(p), `${p} must be exempt`).toBe(false);
    }
  });

  // These used to be excluded from the matcher, back when next.config.ts
  // rewrote them. It doesn't any more (that rewrite baked ARTI_API_URL at
  // build time and 500'd in every split-pod deployment), so middleware is the
  // only thing forwarding them — excluding them again would 404 the whole API
  // surface for anyone who reaches arti-web directly.
  it("runs on the arti-server paths, because middleware is what forwards them", () => {
    for (const p of ["/api/artifacts", "/api/graphql", "/auth/login", "/mcp", "/healthz", "/openapi.yaml", "/.well-known/oauth-protected-resource"]) {
      expect(matches(p), `${p} must reach middleware`).toBe(true);
    }
  });
});
