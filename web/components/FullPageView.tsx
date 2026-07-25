import CommentsLayer from "./CommentsLayer";
import FullPageHtmlFrame from "./FullPageHtmlFrame";
import type { Me } from "@/lib/types";
import { Frontmatter, renderMarkdown, PROSE_CLASSNAME } from "@/lib/markdown";
import { fullPageKind, isJSONContentType, prettyPrintJSON } from "@/lib/viewer";
import { encodeFilePath } from "@/lib/arti";

// Standalone "no-chrome" view of an artifact body. Used when the URL
// carries `?v=full` (or the legacy `?view=fullpage`).
//
// For HTML payloads we need three things to work end-to-end:
//   - scripts run (slide decks, anchor-scroll listeners, etc.)
//   - same-document anchor links (`<a href="#section">`) navigate
//   - relative URLs (e.g. `<img src="foo.png">`) resolve against the
//     artifact's API path so embedded assets actually load
//
// Sandboxing: `allow-scripts allow-popups allow-popups-to-escape-sandbox
// allow-top-navigation-by-user-activation allow-downloads` keeps the iframe in a
// unique origin (no access to the catalog session cookie) while letting
// user-driven anchor clicks, `target="_blank"` / `window.open` links, in-page JS,
// and user-initiated downloads (e.g. a report exporting its own results) work. Popups
// take effect only because the served entry document carries the matching
// full-page CSP (requested via `?ctx=fullpage`); the iframe-attribute sandbox
// and the response CSP `sandbox` intersect, so both must grant it. We do NOT add
// `allow-same-origin` — the iframe is same-site as arti-server, so granting it
// would expose `arti_session` to whatever HTML someone uploaded.
export default function FullPageView({
  body,
  contentType,
  title,
  artifactID,
  filePath,
  entries,
  entryPoint,
  me,
  textScale = 1,
}: {
  body: string;
  contentType: string;
  title: string;
  artifactID?: string;
  filePath?: string;
  // PACKAGE/APP only: the manifest entries + entry point, so the HTML frame can
  // keep ?file= in sync as in-content links navigate between files.
  entries?: Array<{ path: string; content_type: string }>;
  entryPoint?: string | null;
  me?: Me | null;
  // Text scale multiplier — honoured on text/markdown and text/plain views
  // by setting --arti-text-scale on the container. Mirrors ArtifactViewer's
  // text-size control; the caller passes it from the ?ts= URL param so the
  // couch side-panel can sync it to the user's chat font-size preference.
  textScale?: number;
}) {
  // The comments overlay mounts to <body>, so it floats above the
  // full-bleed content. For markdown it anchors into the rendered prose
  // (data-arti-doc). For HTML it's suppressed by CommentsLayer — the
  // arti-served iframe gets the overlay INJECTED in-page instead (full
  // text-select + pin), which the outer overlay can't do across the
  // sandbox boundary.
  const comments = artifactID ? <CommentsLayer artifactId={artifactID} me={me} contentType={contentType} fullPage /> : null;
  const kind = fullPageKind(contentType);
  // Non-text kinds (image/pdf/binary) render from the raw same-origin bytes
  // URL — never by coercing `body` (which for these is raw bytes-as-string)
  // into the <pre> fallback. arti and the embedder are same-site, so the
  // browser carries the user's session cookie to this URL. When `filePath` is
  // set the payload is a file INSIDE a PACKAGE, so target the per-file route
  // (`/files/<path>`) rather than the artifact's own bytes (which for a PACKAGE
  // is the zip).
  const rawSrc = artifactID
    ? filePath
      ? `/api/artifacts/${artifactID}/files/${encodeFilePath(filePath)}`
      : `/api/artifacts/${artifactID}`
    : undefined;
  // Cover the whole viewport — including the sidebar from the root
  // layout. `fixed inset-0 z-50 bg-white` puts the content on its own
  // top-layer plane so the rail underneath isn't visible (and isn't
  // accidentally interactable behind the rendered body).
  if (kind === "html") {
    // Load via `src` from arti-server (not srcDoc) so the document has a real
    // URL — relative refs resolve without a <base> hack, and arti-server
    // injects the comments overlay into the served page (text-select + pin +
    // doc-level), the same in-page commenting a PACKAGE file gets. The outer
    // CommentsLayer (`comments`) is suppressed for HTML; the injected overlay
    // handles it. `?ctx=fullpage` asks arti-server to serve this entry document
    // under the popups-enabled full-page CSP so its `target="_blank"` /
    // `window.open` links open on a normal click. Falls back to srcDoc only if
    // we somehow have no artifactID.
    const src = rawSrc ? `${rawSrc}?ctx=fullpage` : undefined;
    return (
      <>
        {/* The iframe lives in FullPageHtmlFrame (a client component) so it can
          listen for the injected page's file-nav reports and keep ?file= in
          sync. Dimensions: an absolutely-positioned replaced element with no
          explicit size uses its intrinsic 300×150 (CSS 2.1 § 10.3.8), so
          `w-full h-full` stretches it to the fixed containing block (the
          viewport) — avoiding the iOS-Safari 100vw/100vh overshoot. */}
        <FullPageHtmlFrame
          src={src}
          srcDoc={src ? undefined : body}
          title={title}
          entries={entries}
          entryPoint={entryPoint}
          initialFile={filePath}
        />
        {comments}
      </>
    );
  }
  // Markdown gets the same prose treatment as the in-viewer body,
  // just stretched full-viewport with no chrome.
  if (kind === "markdown") {
    // Sanitize: marked may emit raw <script> / event-handler attrs
    // from the markdown source. renderMarkdown splits frontmatter, parses,
    // and DOMPurify-sanitizes — shared with the /help docs viewer.
    const { frontmatter, html } = renderMarkdown(body);
    return (
      // overflow-y-auto + overflow-x-hidden (NOT overflow-auto): on
      // mobile a vertical finger-scroll can pick up sub-pixel horizontal
      // drift, which a generic `overflow-auto` honors as a real X-axis
      // pan. Pinning X-overflow keeps reading steady.
      <div
        className="fixed inset-0 z-50 overflow-y-auto overflow-x-hidden bg-white"
        style={{ "--arti-text-scale": textScale } as React.CSSProperties}
      >
        {comments}
        <article data-arti-doc className={PROSE_CLASSNAME}>
          {frontmatter !== null ? <Frontmatter raw={frontmatter} /> : null}
          <div dangerouslySetInnerHTML={{ __html: html }} />
        </article>
      </div>
    );
  }
  // Image attachment: render the bytes via <img>, centered and contained in
  // the viewport (mirrors NonTextBody in the in-viewer body).
  if (kind === "image" && rawSrc) {
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center overflow-auto bg-white p-4">
        {comments}
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img src={rawSrc} alt={title} className="max-h-full max-w-full object-contain" />
      </div>
    );
  }
  // PDF: hand the bytes to the browser's native viewer in a full-bleed frame.
  if (kind === "pdf" && rawSrc) {
    return (
      <>
        <iframe src={rawSrc} title={title} className="fixed inset-0 z-50 block h-full w-full border-0 bg-white" />
        {comments}
      </>
    );
  }
  // Opaque binary (zip, octet-stream, …): offer a download rather than
  // dumping raw bytes into a <pre>.
  if (kind === "binary" && rawSrc) {
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-white p-4">
        {comments}
        <a
          href={`${rawSrc}?download=1`}
          download
          className="rounded-md border border-blue-300 bg-white px-4 py-2 text-[13px] font-medium text-blue-700 hover:bg-blue-50"
        >
          ↓ Download {title}
        </a>
      </div>
    );
  }
  // Fallback for textual non-markdown/html (plain text, JSON, YAML, JS) — and
  // the unreachable case of a non-text kind with no artifactID — clean
  // monospace covering the whole viewport. JSON pretty-prints (2-space
  // indent); there's no Raw Source toggle on this chrome-less view.
  const displayBody = isJSONContentType(contentType) ? prettyPrintJSON(body) : body;
  return (
    <div
      className="fixed inset-0 z-50 overflow-y-auto overflow-x-hidden bg-white p-4 sm:p-6"
      style={{ "--arti-text-scale": textScale } as React.CSSProperties}
    >
      {comments}
      <pre data-arti-doc className="whitespace-pre-wrap break-words font-mono text-sm">{displayBody}</pre>
    </div>
  );
}
