import { describe, expect, it } from "vitest";
import { linkify } from "./linkify";
import { mentionHTML } from "./mentionMenu";

const AV = (s: string) => s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c] as string));
const render = (s: string) => linkify(s, AV);
// How commentsOverlay renders a comment body: links first, mentions in the rest.
const body = (s: string) => linkify(s, AV, (t) => mentionHTML(t, AV));

describe("linkify", () => {
  it("wraps an http(s) URL in an anchor and leaves the rest alone", () => {
    expect(render("see https://example.com/a now")).toBe(
      'see <a class="ac-url" href="https://example.com/a" target="_blank" rel="noopener noreferrer">https://example.com/a</a> now',
    );
    expect(render("no links here")).toBe("no links here");
  });

  it("does not linkify a non-http scheme", () => {
    for (const s of ["javascript:alert(1)", "data:text/html,<b>", "ftp://host/f", "mailto:a@b.com"]) {
      expect(render(s)).toBe(AV(s));
    }
  });

  it("cannot break out of the attribute or inject markup", () => {
    // The match stops at the quote, and everything on both sides is escaped.
    const out = render('https://example.com/?a=1&b="x"><script>alert(1)</script>');
    expect(out).toContain('href="https://example.com/?a=1&amp;b="');
    expect(out).not.toContain("<script>");
    expect(out.match(/<a /g)).toHaveLength(1);
  });

  it("leaves sentence punctuation outside the link", () => {
    expect(render("see https://example.com/a.")).toContain(">https://example.com/a</a>.");
    expect(render("(see https://example.com/a)")).toContain(">https://example.com/a</a>)");
  });

  it("keeps a closing bracket the path itself opened", () => {
    expect(render("https://en.wikipedia.org/wiki/Foo_(bar)")).toContain(">https://en.wikipedia.org/wiki/Foo_(bar)</a>");
  });

  it("links every URL in a body, including a very long one", () => {
    const long = "https://angellist.slack.com/archives/C0C0A3SEK4P/p1790099123456789";
    const out = render(`a ${long} b https://x.dev c`);
    expect(out.match(/<a /g)).toHaveLength(2);
    expect(out).toContain(`>${long}</a>`);
  });

  it("composes with mention rendering: both marked up in one body", () => {
    const out = body("@bob@x.com see https://example.com/a");
    expect(out).toContain('<span class="ac-mention">@bob@x.com</span>');
    expect(out).toContain('<a class="ac-url" href="https://example.com/a"');
  });

  it("keeps an address inside a URL part of the link", () => {
    const url = "https://mastodon.social/@bob@x.com/12345";
    const out = body(`profile: ${url}`);
    expect(out).toBe(`profile: <a class="ac-url" href="${url}" target="_blank" rel="noopener noreferrer">${url}</a>`);
    expect(out).not.toContain("ac-mention");
  });
});
