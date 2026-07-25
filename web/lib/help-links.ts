import { posix } from "node:path";

// Rewrites links/images inside already-rendered help-doc HTML, resolving
// doc-relative paths against the doc's own directory (so `./x.svg` and
// `../reference/y.md` behave the way an author expects). Relative `.md`
// links → in-app `/help/<slug>` routes; relative diagram images (committed
// SVG/PNG under web/public/help-diagrams, mirroring the doc's subpath) →
// the served `/help-diagrams/<dir>/<file>` path. Absolute, external, and
// anchor refs are left alone. Operates on HTML strings via regex (no DOM):
// inputs come from our own trusted, sanitized markdown.

function isRelative(u: string): boolean {
  return !/^(?:[a-z]+:|\/|#)/i.test(u);
}

// resolveRel("guides", "../reference/concepts.md") -> "reference/concepts.md"
// resolveRel("guides", "./getting-started.svg")    -> "guides/getting-started.svg"
function resolveRel(docDir: string, rel: string): string {
  return posix.normalize(posix.join(docDir, rel));
}

// docDir is the directory the doc lives in (its section), e.g. "guides".
export function rewriteHelpLinks(html: string, docDir: string): string {
  const withLinks = html.replace(/href="([^"]+)"/g, (m, href: string) => {
    if (!isRelative(href) || !/\.md(?:#|$)/.test(href)) return m;
    const [path, frag] = href.split("#", 2);
    const slug = resolveRel(docDir, path).replace(/\.md$/, "");
    return `href="/help/${slug}${frag ? `#${frag}` : ""}"`;
  });
  return withLinks.replace(/src="([^"]+)"/g, (m, src: string) => {
    if (!isRelative(src) || !/\.(?:svg|png)$/i.test(src)) return m;
    return `src="/help-diagrams/${resolveRel(docDir, src)}"`;
  });
}
