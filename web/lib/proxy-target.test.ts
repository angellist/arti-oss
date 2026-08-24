import { describe, it, expect } from "vitest";
import nextConfig from "../next.config";
import {
  DEV_API_URL,
  FE_PROXY_MARKER_HEADER,
  apiOrigin,
  forwardHeaders,
  isFromFEProxy,
  isProxiedPath,
  proxyDestination,
} from "./proxy-target";

describe("isProxiedPath", () => {
  it("claims every path arti-server owns", () => {
    for (const p of [
      "/api",
      "/api/artifacts",
      "/api/graphql",
      "/app/my-app/index.html",
      "/auth/login",
      "/auth/embed/mint",
      "/mcp",
      "/.well-known/oauth-protected-resource",
      "/healthz",
      "/openapi.yaml",
    ]) {
      expect(isProxiedPath(p), `${p} must go to arti-server`).toBe(true);
    }
  });

  it("leaves Next.js pages alone", () => {
    for (const p of [
      "/",
      "/browse",
      "/login",
      "/s/my-doc",
      "/s/v1.2.3/4",
      "/settings/keys",
      "/help/getting-started",
    ]) {
      expect(isProxiedPath(p), `${p} must stay on Next.js`).toBe(false);
    }
  });

  // Prefix matching has to be segment-aware. A naive startsWith("/app") would
  // swallow any future /appearance or /apps page and hand it to a server that
  // has no such route.
  it("matches on path segments, not on string prefixes", () => {
    for (const p of ["/apikeys", "/apps", "/appearance", "/authors", "/mcparty", "/healthzz"]) {
      expect(isProxiedPath(p), `${p} must not be captured`).toBe(false);
    }
  });
});

describe("apiOrigin", () => {
  // The whole point of the fix: the value is read from the environment the
  // process is running in, not from the environment the image was built in.
  it("uses ARTI_API_URL when set", () => {
    expect(apiOrigin({ ARTI_API_URL: "http://arti-server.arti-pr-42.svc.cluster.local" }))
      .toBe("http://arti-server.arti-pr-42.svc.cluster.local");
  });

  it("falls back to the local-dev target when unset or blank", () => {
    expect(apiOrigin({})).toBe(DEV_API_URL);
    expect(apiOrigin({ ARTI_API_URL: "   " })).toBe(DEV_API_URL);
  });

  it("tolerates a trailing slash so the joined path never doubles up", () => {
    expect(apiOrigin({ ARTI_API_URL: "http://arti-server.arti.svc.cluster.local/" }))
      .toBe("http://arti-server.arti.svc.cluster.local");
  });
});

describe("proxyDestination", () => {
  const env = { ARTI_API_URL: "http://arti-server.arti.svc.cluster.local" };

  it("keeps the path and query string verbatim", () => {
    expect(proxyDestination("/api/artifacts", "?type=TEXT&limit=50", env).toString())
      .toBe("http://arti-server.arti.svc.cluster.local/api/artifacts?type=TEXT&limit=50");
  });

  it("handles a slash-terminated ARTI_API_URL", () => {
    expect(proxyDestination("/api/graphql", "", { ARTI_API_URL: "http://arti-server/" }).toString())
      .toBe("http://arti-server/api/graphql");
  });

  // The reported failure: with no ARTI_API_URL the destination is the local-dev
  // address, which in a split-pod deployment is nothing at all. It must at
  // least be the documented dev fallback rather than a malformed URL.
  it("falls back to the dev target with no env", () => {
    expect(proxyDestination("/api/graphql", "", {}).toString())
      .toBe(`${DEV_API_URL}/api/graphql`);
  });
});
describe("forwardHeaders", () => {
  // arti-server's IngressLoginHandler mints a signed arti_session from
  // X-Auth-Request-Email/-Groups without re-verifying them; its safety rests
  // entirely on those headers being unforgeable *because* oauth2-proxy
  // overwrote them at the ingress. A request that arrived at arti-web instead
  // (port-forward, depot rig, Service-to-Service, or an ingress fall-through)
  // never passed through that subrequest, so relaying its identity claims
  // would let any caller mint a session for any allowlisted email.
  it("drops the identity assertions arti-server trusts by position", () => {
    const out = forwardHeaders(new Headers({
      "x-auth-request-email": "ceo@example.com",
      "x-auth-request-groups": "engineers,admins",
      "x-auth-request-user": "ceo",
      "x-auth-request-preferred-username": "ceo",
    }));
    expect(out.get("x-auth-request-email")).toBeNull();
    expect(out.get("x-auth-request-groups")).toBeNull();
    expect(out.get("x-auth-request-user")).toBeNull();
    expect(out.get("x-auth-request-preferred-username")).toBeNull();
  });

  // Header names are case-insensitive in the Headers API, so the spoof cannot
  // be smuggled back in with different casing.
  it("drops them whatever the casing", () => {
    const out = forwardHeaders(new Headers({ "X-Auth-Request-Email": "ceo@example.com" }));
    expect(out.get("X-Auth-Request-Email")).toBeNull();
  });

  // Everything a legitimate caller needs must survive the hop — including
  // X-Auth-Request-Access-Token, which arti-server treats as a token source
  // and then verifies cryptographically (internal/auth/middleware.go), so it
  // is not spoofable and must not be stripped.
  it("relays credentials that are verified rather than trusted", () => {
    const out = forwardHeaders(new Headers({
      cookie: "arti_session=abc",
      authorization: "Bearer tok",
      "x-auth-request-access-token": "jwt",
      "content-type": "application/json",
    }));
    expect(out.get("cookie")).toBe("arti_session=abc");
    expect(out.get("authorization")).toBe("Bearer tok");
    expect(out.get("x-auth-request-access-token")).toBe("jwt");
    expect(out.get("content-type")).toBe("application/json");
  });

  it("does not mutate the incoming headers", () => {
    const incoming = new Headers({ "x-auth-request-email": "ceo@example.com" });
    forwardHeaders(incoming);
    expect(incoming.get("x-auth-request-email")).toBe("ceo@example.com");
  });
});
// arti-server and arti-web proxy to each other by design: arti-server owns the
// API and reverse-proxies everything else here, and arti-web forwards the
// arti-server-owned prefixes back. That composes only while each side declines
// the other's paths — and arti-server's `/*` catch-all does not, by default,
// decline /api/graphql, /auth/typo or /app/a/b/c. Those used to reach arti-web
// and die on the baked localhost target (which is exactly the 500 the depot rig
// smoke test reported); with the forward fixed, an unguarded fall-through would
// be an infinite round trip instead. arti-server now 404s them at the source
// (cmd/arti-server/feproxy.go) and marks whatever it does proxy, so arti-web has
// an independent way to break the cycle during a version-skewed rollout.
describe("isFromFEProxy", () => {
  it("recognizes a request arti-server's FE proxy forwarded", () => {
    expect(isFromFEProxy(new Headers({ [FE_PROXY_MARKER_HEADER]: "1" }))).toBe(true);
  });

  it("is case-insensitive, like every other header lookup", () => {
    expect(isFromFEProxy(new Headers({ "X-Arti-Fe-Proxy": "1" }))).toBe(true);
  });

  it("leaves a direct request alone", () => {
    expect(isFromFEProxy(new Headers({ cookie: "arti_session=abc" }))).toBe(false);
    expect(isFromFEProxy(new Headers())).toBe(false);
  });

  // The marker must survive the hop when arti-web does forward: arti-server
  // overwrites it on the way out, so relaying it costs nothing and keeps the
  // hop visible in logs.
  it("is not one of the headers stripped on forward", () => {
    const out = forwardHeaders(new Headers({ [FE_PROXY_MARKER_HEADER]: "1" }));
    expect(out.get(FE_PROXY_MARKER_HEADER)).toBe("1");
  });
});

// Ratchet. Anything put back into next.config.ts `rewrites()` is serialized
// into .next/routes-manifest.json at `next build` and read straight off disk by
// `next start`, so any env var in its destination silently becomes a build-time
// constant. ARTI_API_URL is unset on the build machine (web/Dockerfile), so
// that constant was `http://localhost:8095` — which resolves to nothing in a
// deployment where arti-web and arti-server are separate pods.
describe("next.config.ts", () => {
  it("declares no rewrites, so no forward target can be frozen at build time", () => {
    expect(nextConfig.rewrites).toBeUndefined();
  });
});
