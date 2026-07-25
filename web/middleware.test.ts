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

  it("exempts Next internals and the paths the Go binary owns", () => {
    for (const p of ["/_next/static/chunk.js", "/_next/image", "/favicon.ico", "/api/artifacts", "/auth/login", "/login"]) {
      expect(matches(p), `${p} must be exempt`).toBe(false);
    }
  });
});
