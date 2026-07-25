import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import FullPageView from "./FullPageView";

// Regression guard for the bug where the chrome-less full-page view (?v=full)
// of an image attachment dumped raw PNG bytes into a <pre> instead of rendering
// the image. The embedder (couch's artifact side panel) iframes this view, so
// the user saw a wall of "?PNG …" noise. The fix: non-text content renders
// from the raw same-origin bytes URL, never from `body`.
const ID = "921bad68-9612-420a-8ce9-e8609ff17950";

describe("FullPageView", () => {
  it("renders an image as <img> from the raw bytes URL — never the bytes as text", () => {
    const html = renderToStaticMarkup(
      <FullPageView body="\x89PNG\r\n bogus-png-bytes" contentType="image/png" title="screenshot" artifactID={ID} />,
    );
    expect(html).toContain("<img");
    expect(html).toContain(`/api/artifacts/${ID}`);
    expect(html).not.toContain("bogus-png-bytes");
    expect(html).not.toContain("<pre");
  });

  it("renders a PDF in a full-bleed frame from the raw bytes URL", () => {
    const html = renderToStaticMarkup(
      <FullPageView body="" contentType="application/pdf" title="doc" artifactID={ID} />,
    );
    expect(html).toContain("<iframe");
    expect(html).toContain(`/api/artifacts/${ID}`);
  });

  it("offers a download for opaque binary instead of dumping bytes", () => {
    const html = renderToStaticMarkup(
      <FullPageView body="PK\x03\x04 zip-bytes" contentType="application/zip" title="bundle" artifactID={ID} />,
    );
    expect(html).toContain(`/api/artifacts/${ID}?download=1`);
    expect(html).not.toContain("zip-bytes");
    expect(html).not.toContain("<pre");
  });

  it("renders textual JSON as <pre> body, pretty-printed with a 2-space indent", () => {
    const html = renderToStaticMarkup(
      <FullPageView body={'{"k":1}'} contentType="application/json" title="data" artifactID={ID} />,
    );
    expect(html).toContain("<pre");
    // Compact input has no space after the colon; pretty-printed does —
    // a reliable signal the body was reformatted, not passed through raw.
    expect(html).toContain("&quot;k&quot;: 1");
  });

  it("falls back to the raw body when JSON doesn't parse, rather than erroring", () => {
    const html = renderToStaticMarkup(
      <FullPageView body="{not valid json" contentType="application/json" title="data" artifactID={ID} />,
    );
    expect(html).toContain("{not valid json");
  });

  // Full-page view unification: a package HTML file must iframe its OWN per-file
  // URL (not `/api/artifacts/<id>`, which for a PACKAGE is the zip), and the
  // entry document carries `?ctx=fullpage` so arti-server serves it under the
  // popups-enabled CSP — the server half of making `target="_blank"` links
  // clickable on a normal click.
  it("renders a package HTML file from its per-file URL with the full-page CSP hint", () => {
    const html = renderToStaticMarkup(
      <FullPageView body="" contentType="text/html" title="report" artifactID={ID} filePath="docs/report.html" />,
    );
    expect(html).toContain("<iframe");
    expect(html).toContain(`/api/artifacts/${ID}/files/docs/report.html?ctx=fullpage`);
    // popups let target=_blank / window.open open on a normal click…
    expect(html).toContain("allow-popups");
    // …but NEVER allow-same-origin (would expose arti_session to uploaded HTML).
    expect(html).not.toContain("allow-same-origin");
  });

  it("renders a single-file HTML artifact from its own URL with the full-page CSP hint", () => {
    const html = renderToStaticMarkup(
      <FullPageView body="<h1>hi</h1>" contentType="text/html" title="dash" artifactID={ID} />,
    );
    expect(html).toContain(`/api/artifacts/${ID}?ctx=fullpage`);
    expect(html).toContain("allow-popups");
    expect(html).not.toContain("allow-same-origin");
  });

  it("renders a package image from its per-file URL, never the zip", () => {
    const html = renderToStaticMarkup(
      <FullPageView body="" contentType="image/png" title="chart" artifactID={ID} filePath="assets/chart.png" />,
    );
    expect(html).toContain("<img");
    expect(html).toContain(`/api/artifacts/${ID}/files/assets/chart.png`);
  });

  it("percent-encodes each path segment of a nested file, keeping the slashes", () => {
    const html = renderToStaticMarkup(
      <FullPageView body="" contentType="text/html" title="p" artifactID={ID} filePath="a dir/a b.html" />,
    );
    expect(html).toContain(`/api/artifacts/${ID}/files/a%20dir/a%20b.html?ctx=fullpage`);
  });
});
