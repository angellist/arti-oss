import { describe, it, expect } from "vitest";
import { rewriteHelpLinks } from "./help-links";

// docDir = the section the doc lives in, e.g. "guides".
describe("rewriteHelpLinks", () => {
  it("rewrites cross-section .md links relative to the doc dir", () => {
    const html = `<a href="../reference/concepts.md">Concepts</a>`;
    expect(rewriteHelpLinks(html, "guides")).toContain(`href="/help/reference/concepts"`);
  });
  it("rewrites same-section .md links keeping the section", () => {
    const html = `<a href="sharing.md">Sharing</a>`;
    expect(rewriteHelpLinks(html, "guides")).toContain(`href="/help/guides/sharing"`);
  });
  it("keeps the anchor fragment on .md links", () => {
    const html = `<a href="concepts.md#slugs">Slugs</a>`;
    expect(rewriteHelpLinks(html, "reference")).toContain(`href="/help/reference/concepts#slugs"`);
  });
  it("rewrites relative svg images to /help-diagrams mirroring the doc dir", () => {
    const html = `<img src="./getting-started.svg">`;
    expect(rewriteHelpLinks(html, "guides")).toContain(`src="/help-diagrams/guides/getting-started.svg"`);
  });
  it("leaves absolute and external links untouched", () => {
    const html = `<a href="https://x.com">x</a><a href="/s/foo">f</a><a href="#top">t</a>`;
    const out = rewriteHelpLinks(html, "guides");
    expect(out).toContain(`href="https://x.com"`);
    expect(out).toContain(`href="/s/foo"`);
    expect(out).toContain(`href="#top"`);
  });
});
