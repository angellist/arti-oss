import { isDiagramContentType } from "./diagram";

// Reads the optional ?v= query param. `full` renders the artifact body
// in a full-viewport iframe with no chrome. The legacy `?view=fullpage`
// form is still accepted so any URLs already shared keep working.
export function isFullPageView(
  sp: Record<string, string | string[] | undefined>,
): boolean {
  const pick = (k: string): string | undefined => {
    const v = sp[k];
    return Array.isArray(v) ? v[0] : v;
  };
  return pick("v") === "full" || pick("view") === "fullpage";
}

// isTextualContentType reports whether a content_type holds text the viewer
// renders as text (markdown/html/code) — and therefore the page must fetch the
// body as a string. Mirrors the server's isTextualContentType. Non-text
// (pdf, image, zip, binary) is shown inline or as a download card and needs no
// text body. Keeping the viewer's render decision and the page's body-fetch
// decision on this one predicate stops them drifting (e.g. a JSON attachment
// rendering empty). Charset/params are ignored.
export function isTextualContentType(ct: string): boolean {
  const base = ct.split(";")[0].trim().toLowerCase();
  return (
    base.startsWith("text/") ||
    // Structured-suffix JSON types (RFC 6839) — application/vnd.foo+json — are
    // text like plain JSON is. This is what lets a diagram
    // (application/vnd.arti.diagram+json) be a TEXT artifact.
    base.endsWith("+json") ||
    base === "application/json" ||
    base === "application/yaml" ||
    base === "application/javascript"
  );
}

// isJSONContentType reports whether a content_type is JSON (ignoring
// charset/params) — used to decide whether the rendered (non-raw) view
// pretty-prints the body via prettyPrintJSON.
export function isJSONContentType(ct: string): boolean {
  return ct.split(";")[0].trim().toLowerCase() === "application/json";
}

// prettyPrintJSON reformats a JSON body with a 2-space indent for the
// rendered view. "Raw Source" bypasses this and shows the stored body
// verbatim. Invalid JSON falls back to the original text unchanged rather
// than showing an error — a malformed artifact should still be readable.
export function prettyPrintJSON(body: string): string {
  try {
    return JSON.stringify(JSON.parse(body), null, 2);
  } catch {
    return body;
  }
}

// How the chrome-less full-page view (`?v=full`) should render a content type —
// and, conversely, whether the page route even needs to fetch the body as a
// string. Non-text kinds (image/pdf/binary) render from the raw same-origin
// bytes URL (`/api/artifacts/<id>`); coercing those bytes into a string and
// dropping them in a <pre> is exactly the bug this split fixes (an image
// attachment rendered as a wall of PNG noise). Mirrors NonTextBody's split in
// the in-viewer body so the two render decisions can't drift.
export type FullPageKind = "html" | "markdown" | "image" | "pdf" | "diagram" | "text" | "binary";

export function fullPageKind(ct: string): FullPageKind {
  const base = ct.split(";")[0].trim().toLowerCase();
  if (base.startsWith("text/html")) return "html";
  if (base.startsWith("text/markdown")) return "markdown";
  // Diagrams are JSON on the wire but a picture on screen — full-page must
  // render the canvas, not the source.
  if (isDiagramContentType(base)) return "diagram";
  if (base.startsWith("image/")) return "image";
  if (base === "application/pdf") return "pdf";
  // Remaining textual types (plain text, json, yaml, js) render as text;
  // everything else is opaque bytes shown as a download.
  return isTextualContentType(base) ? "text" : "binary";
}

// fileParamSearch builds the query string (with a leading "?", or "" when
// empty) that reflects the currently-shown PACKAGE file, PRESERVING every other
// param. It drops `?file=` entirely when the path is empty/null or equals the
// package entry point, so the entry file keeps a clean base URL. Shared by the
// normal-view sidebar (rail-context) and the full-page in-content listener
// (FullPageHtmlFrame) so both produce the SAME URL for a given file.
export function fileParamSearch(
  currentSearch: string,
  path: string | null,
  entryPoint: string | null,
): string {
  // URLSearchParams strips a leading "?" itself, so window.location.search is
  // fine as-is.
  const params = new URLSearchParams(currentSearch);
  // Truthy check (not `!= null`): an empty-string path should also drop the
  // param, never write `?file=`.
  if (path && path !== entryPoint) params.set("file", path);
  else params.delete("file");
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

// resolvePackageEntry maps a requested `?file=` path to the concrete PACKAGE
// entry the server would serve, MIRRORING the server's pkgzip.resolveCandidates
// (internal/pkgzip/zip.go): a directory / extensionless / empty path resolves to
// `<path>`, then `<path>.html`, then `<path>/index.html` (and "" / "dir/" →
// index.html). This matters because the served content type is the RESOLVED
// file's — so an exact `path === filePath` lookup would mislabel a shared/
// hand-written `?file=docs/guide` (entry `docs/guide.html`) as octet-stream and
// offer a download instead of rendering the HTML. Returns the matched entry
// (canonical path + content_type), or undefined when nothing matches.
export function resolvePackageEntry(
  entries: Array<{ path: string; content_type: string }>,
  filePath: string | undefined,
): { path: string; content_type: string } | undefined {
  const p = (filePath ?? "").replace(/^\/+/, "");
  const candidates =
    p === "" ? ["index.html"] : p.endsWith("/") ? [p + "index.html"] : [p, p + ".html", p + "/index.html"];
  for (const c of candidates) {
    const e = entries.find((x) => x.path === c);
    if (e) return e;
  }
  return undefined;
}

// TEXT_SCALE_PARAM maps the `?ts=` URL param to a body text-scale multiplier.
// `sm`/`md`/`lg` mirror ViewerToolbar's TEXT_SCALE (the in-viewer control), so a
// link carrying the reader's own preference renders identically. `xs` is a
// param-only extra step below the toolbar's smallest, for EMBEDDERS: couch's
// artifact side panel is a ~420px column, where the toolbar's `md` baseline
// (tuned for a full page) reads a step too large. Keep the shared keys in sync
// with TEXT_SCALE.
export const TEXT_SCALE_PARAM: Record<string, number> = {
  xs: 0.72,
  sm: 0.85,
  md: 1,
  lg: 1.15,
};

// textScaleFromParam reads `?ts=` off a page's searchParams and returns the
// multiplier to publish as --arti-text-scale. Unknown/absent → 1 (today's
// sizes), so a stale or hand-typed link never renders at a surprise size.
export function textScaleFromParam(
  sp: Record<string, string | string[] | undefined>,
): number {
  const v = Array.isArray(sp.ts) ? sp.ts[0] : sp.ts;
  return (v && TEXT_SCALE_PARAM[v]) || 1;
}
