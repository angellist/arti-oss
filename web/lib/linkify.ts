// linkify.ts — turns bare URLs typed into a comment body into anchors.
//
// Only http and https are linkified: the overlay mounts into sandboxed served
// pages, where an anchor built from someone else's text is an execution vector.

const URL_RE = /\bhttps?:\/\/[^\s<>"'`]+/g;

// Punctuation a sentence puts AFTER a URL rather than inside it.
const TRAILING = ".,;:!?'\"";

const count = (s: string, ch: string) => s.split(ch).length - 1;

function trimTrailing(url: string): string {
  let end = url.length;
  while (end > 0) {
    const ch = url[end - 1];
    if (TRAILING.includes(ch)) {
      end--;
      continue;
    }
    // A closing bracket the URL itself opened belongs to the path
    // ("…/Foo_(bar)"); an unmatched one is the prose's, closing around the link.
    const open = ch === ")" ? "(" : ch === "]" ? "[" : ch === "}" ? "{" : "";
    if (!open) break;
    const head = url.slice(0, end);
    if (count(head, open) >= count(head, ch)) break;
    end--;
  }
  return url.slice(0, end);
}

// linkify renders `text` as escaped HTML with its URLs wrapped in anchors. It
// escapes the URL for the href as well as the link text, so a quote inside a
// match cannot break out of the attribute.
//
// `renderText` renders the runs BETWEEN links and may emit markup of its own
// (mention spans). Running it here rather than around linkify is what makes a
// URL win over anything that could match inside it: "https://host/@bob@x.com"
// is one link, not a truncated link followed by a mention.
export function linkify(text: string, escape: (s: string) => string, renderText: (s: string) => string = escape): string {
  let out = "";
  let last = 0;
  URL_RE.lastIndex = 0;
  for (let m = URL_RE.exec(text); m; m = URL_RE.exec(text)) {
    const url = trimTrailing(m[0]);
    if (!/^https?:\/\/[^/]/.test(url)) continue;
    const href = escape(url);
    out += renderText(text.slice(last, m.index)) + `<a class="ac-url" href="${href}" target="_blank" rel="noopener noreferrer">${href}</a>`;
    last = m.index + url.length;
    URL_RE.lastIndex = last;
  }
  return out + renderText(text.slice(last));
}
