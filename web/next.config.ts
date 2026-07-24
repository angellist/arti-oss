import type { NextConfig } from "next";
import { DEFAULT_CATALOG_FRAME_ANCESTORS } from "./tenant-defaults";

// In production, arti-server is the front door and reverse-proxies
// non-API paths to this Next.js process. In local dev `npm run dev`
// runs Next.js directly on :3030 and we want browser fetches like
// `/api/artifacts` to hit the Go server on :8095. Rewrite them.
// Baseline CSP for the catalog UI. Next.js inlines bootstrap scripts
// and Tailwind injects inline styles, so we have to permit
// 'unsafe-inline' for both. The important parts are:
//   - default-src 'self'   blocks cross-origin script/data loads
//   - frame-ancestors      blocks third-party framing of arti
//   - frame-src             lets arti iframe its own /api/.../files/*
//                          (for the PACKAGE viewer) AND srcdoc iframes
//                          (FullPageView for standalone HTML artifacts).
//                          Chrome assigns `about:srcdoc` to <iframe srcdoc>;
//                          without the `about:` scheme-source allowance
//                          here, frame-src 'self' silently refuses to
//                          load srcdoc iframes and the FullPageView is
//                          blank.
// Uploaded HTML responses get their own per-response
// `Content-Security-Policy: sandbox …` from arti-server, so this
// catalog-app CSP doesn't have to constrain them further.
//
// frame-ancestors: who may iframe arti's catalog/viewer pages. Defaults to
// arti itself plus any internal AngelList subdomain (prod + staging) so
// first-party internal tools — couch's artifact side panel, Flowdash — can
// embed the focused viewer (e.g. /s/<slug>?v=full). The framed page is still
// cookie-authed and renders as the logged-in user, so this only relaxes
// clickjacking protection to trusted internal origins; it does NOT bypass
// auth. Truly external third parties stay blocked.
//
// BUILD-TIME, not runtime: Next bakes headers() into routes-manifest.json at
// `next build`, so ARTI_CATALOG_FRAME_ANCESTORS is read when the arti-web image
// is built — setting it on the running pod has no effect (unlike
// ARTI_APP_FRAME_ANCESTORS, which arti-server reads at runtime). Override it as
// a build arg if a stack needs a different value; the default already covers
// prod + staging, so no override is needed in either. The two defaults are kept
// in sync (see cmd/arti-server/cmd_serve.go AppFrameAncestors).
// NOTE: arti-server also sets a global `X-Frame-Options: SAMEORIGIN`; it is
// dropped for these proxied catalog responses (see cmd_serve.go) so this
// frame-ancestors directive is authoritative (XFO can't express an allowlist).
const FRAME_ANCESTORS =
  process.env.ARTI_CATALOG_FRAME_ANCESTORS ?? DEFAULT_CATALOG_FRAME_ANCESTORS;

const CSP_CATALOG = [
  "default-src 'self'",
  // React in dev mode (Turbopack HMR / callstack reconstruction) uses eval();
  // include 'unsafe-eval' only in development — production builds never eval.
  "script-src 'self' 'unsafe-inline'" +
    (process.env.NODE_ENV !== "production" ? " 'unsafe-eval'" : ""),
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data:",
  "font-src 'self' data:",
  "connect-src 'self'",
  "frame-src 'self' about: data: blob:",
  "frame-ancestors " + FRAME_ANCESTORS,
  "object-src 'none'",
  "base-uri 'self'",
  "form-action 'self'",
].join("; ");

const nextConfig: NextConfig = {
  async rewrites() {
    const target = process.env.ARTI_API_URL ?? "http://localhost:8095";
    return [
      { source: "/api/:path*",        destination: `${target}/api/:path*` },
      { source: "/app/:path*",        destination: `${target}/app/:path*` },
      { source: "/auth/:path*",       destination: `${target}/auth/:path*` },
      { source: "/.well-known/:path*", destination: `${target}/.well-known/:path*` },
      { source: "/healthz",           destination: `${target}/healthz` },
      { source: "/mcp",               destination: `${target}/mcp` },
      { source: "/openapi.yaml",      destination: `${target}/openapi.yaml` },
    ];
  },
  async headers() {
    return [
      {
        // CSP only on catalog pages — the Next.js side. arti-server
        // sets the more restrictive `sandbox` CSP on uploaded-HTML
        // bodies; we don't want the two layers to fight, so scope this
        // to actual app routes by excluding /api, /mcp, /auth, /.well-known.
        source: "/((?!api|app|mcp|auth|\\.well-known|healthz|openapi\\.yaml).*)",
        headers: [
          { key: "Content-Security-Policy", value: CSP_CATALOG },
        ],
      },
    ];
  },
};

export default nextConfig;
