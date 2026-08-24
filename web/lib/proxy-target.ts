// Where arti-web forwards the paths arti-server owns, and how that target is
// resolved.
//
// WHY THIS IS NOT IN next.config.ts
// ---------------------------------
// These forwards used to be `rewrites()` entries in next.config.ts, with the
// destination read from `process.env.ARTI_API_URL`. That silently produced a
// build-time constant: Next serializes rewrites into
// `.next/routes-manifest.json` during `next build`, and `next start` loads that
// file off disk (NextNodeServer.getRoutesManifest) rather than re-evaluating
// the config. ARTI_API_URL is not set on the build machine (see
// web/Dockerfile), so every published arti-web image shipped with the
// local-dev target `http://localhost:8095` baked in, and setting ARTI_API_URL
// on the running pod did nothing.
//
// That is harmless in a deployment where the ingress sends /api/* straight to
// arti-server and nothing reaches arti-web on those paths — which is why
// staging and prod never noticed. It is fatal anywhere arti-web is addressed
// directly (a depot rig or preview namespace, a port-forward, a Service-to-
// Service call): arti-web and arti-server are separate Deployments, so nothing
// listens on the web pod's own localhost and every proxied request died with
// `ECONNREFUSED ::1:8095 / 127.0.0.1:8095` -> HTTP 500.
//
// Middleware runs per request, so reading the env var here resolves it at
// runtime in the pod. Keep the forwarding rules here — putting any of them
// back into next.config.ts reintroduces the build-time freeze.

/** Local-dev fallback: `npm run dev` serves Next on :3030 and the Go binary listens on :8095. */
export const DEV_API_URL = "http://localhost:8095";

/**
 * Path prefixes arti-server owns. A prefix matches the exact path or anything
 * below it — `/api` and `/api/artifacts`, but never `/apikeys`.
 */
export const PROXY_PREFIXES = ["/api", "/app", "/auth", "/mcp", "/.well-known", "/share"] as const;

/** Single-path routes arti-server owns. */
export const PROXY_PATHS = ["/healthz", "/openapi.yaml"] as const;

/**
 * Header arti-server stamps on every request it hands to its FE reverse proxy
 * (cmd/arti-server/feproxy.go). Its presence means "arti-server already looked
 * at this path and declined it", so forwarding it back would be a loop.
 *
 * The two services proxy to each other on purpose: arti-server owns the API and
 * reverse-proxies everything else here (ARTI_WEB_URL); arti-web forwards the
 * arti-server-owned prefixes back (ARTI_API_URL). That composes only while each
 * side declines the other's paths. arti-server's route guards are the primary
 * defence; this header covers the window where the two Deployments are running
 * different versions mid-rollout.
 */
export const FE_PROXY_MARKER_HEADER = "x-arti-fe-proxy";

/** isProxiedPath reports whether arti-server, not Next.js, answers this path. */
export function isProxiedPath(pathname: string): boolean {
  if ((PROXY_PATHS as readonly string[]).includes(pathname)) return true;
  return PROXY_PREFIXES.some((p) => pathname === p || pathname.startsWith(`${p}/`));
}

/** The slice of the environment this module reads. */
export type ProxyEnv = Record<string, string | undefined>;

/**
 * apiOrigin resolves the arti-server base URL for the current process. Reads
 * the environment on every call — do not hoist the result into a module-level
 * const, or the value freezes at import time in ways that are easy to miss.
 */
export function apiOrigin(env: ProxyEnv = process.env): string {
  const configured = env.ARTI_API_URL?.trim();
  return configured ? configured.replace(/\/+$/, "") : DEV_API_URL;
}

/**
 * Identity assertions arti-server trusts *positionally* — that is, purely
 * because of where they arrived from. `IngressLoginHandler` mints a signed
 * `arti_session` for whatever `X-Auth-Request-Email`/`-Groups` say, and its
 * safety argument is topological: those headers only reach the pod through
 * nginx's oauth2-proxy auth-subrequest, which overwrites any client-supplied
 * value (internal/auth/ingress_login.go). Note the contrast with
 * `X-Auth-Request-Access-Token`, which arti-server treats as a mere token
 * *source* and then verifies cryptographically — that one is safe to relay.
 *
 * arti-web must not become a second door into that positional trust. In this
 * deployment no legitimate /auth/* request reaches arti-server *via* arti-web
 * — the ingress sends every one of them straight to the Go pod — so anything
 * arriving here carrying these headers is either a direct caller (port-forward,
 * rig, Service-to-Service) or a fall-through, and in neither case has
 * oauth2-proxy vouched for it. Drop them on the way through.
 */
export const SPOOFABLE_IDENTITY_HEADERS = [
  "x-auth-request-email",
  "x-auth-request-groups",
  "x-auth-request-user",
  "x-auth-request-preferred-username",
] as const;

/**
 * forwardHeaders copies the request headers minus the positionally-trusted
 * identity assertions above. Everything else — cookies, Authorization,
 * content-type — is relayed untouched.
 */
/**
 * isFromFEProxy reports whether arti-server's FE reverse proxy sent us this
 * request — meaning arti-server has already declined the path, so forwarding it
 * back would bounce the request between the two pods until something times out.
 */
export function isFromFEProxy(headers: Headers): boolean {
  return headers.has(FE_PROXY_MARKER_HEADER);
}

export function forwardHeaders(incoming: Headers): Headers {
  const out = new Headers(incoming);
  for (const h of SPOOFABLE_IDENTITY_HEADERS) out.delete(h);
  return out;
}

/**
 * proxyDestination builds the absolute arti-server URL for a proxied request,
 * preserving the path and query string verbatim.
 */
export function proxyDestination(
  pathname: string,
  search = "",
  env: ProxyEnv = process.env,
): URL {
  return new URL(`${pathname}${search}`, `${apiOrigin(env)}/`);
}
