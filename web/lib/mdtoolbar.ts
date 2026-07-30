// Pure text transforms behind the markdown editor's toolbar. Each action takes
// the textarea's current (text, selection) and returns the next one — no DOM,
// no React — so the fiddly parts (toggling off, multi-line prefixes, where the
// caret lands) are unit-testable. See mdtoolbar.test.ts.
//
// Every action returns a selection, not just text: a toolbar that leaves the
// caret somewhere arbitrary makes people re-aim after each click.

export interface EditState {
  text: string;
  selStart: number;
  selEnd: number;
}

export type MarkdownAction =
  | "bold"
  | "italic"
  | "code"
  | "h1"
  | "h2"
  | "h3"
  | "bullet"
  | "ordered"
  | "quote"
  | "link"
  | "codeblock"
  | "table"
  | "hr";

// Inline marker pairs. Longest-first matters when detecting an existing wrap:
// "**" has to be tested before "*", or bold would read as italic-plus-a-stray.
const INLINE: Record<"bold" | "italic" | "code", string> = {
  bold: "**",
  italic: "*",
  code: "`",
};

const HEADING: Record<"h1" | "h2" | "h3", string> = {
  h1: "#",
  h2: "##",
  h3: "###",
};

// A URL-ish selection, so the link action can tell "[sel](…)" from "[text](sel)".
const URLISH = /^(https?:\/\/|mailto:|\/)\S*$/i;

// toggleInline wraps or unwraps the selection. Unwrapping handles both the
// markers being INSIDE the selection ("|**x**|") and just outside it
// ("**|x|**") — users produce both, and only stripping one shape leaves the
// other accumulating markers on every click.
function toggleInline(s: EditState, marker: string): EditState {
  const sel = s.text.slice(s.selStart, s.selEnd);
  const before = s.text.slice(0, s.selStart);
  const after = s.text.slice(s.selEnd);
  const n = marker.length;

  // For italic ("*"), a "**bold**" selection must NOT be read as wrapped:
  // its markers are a bold pair, so italic should nest, not strip.
  const isExactPair = (v: string) =>
    v.length >= n * 2 &&
    v.startsWith(marker) &&
    v.endsWith(marker) &&
    // Reject a longer run of the same char (e.g. "**" when marker is "*").
    v[n] !== marker[0] &&
    v[v.length - n - 1] !== marker[0];

  if (isExactPair(sel)) {
    const inner = sel.slice(n, -n);
    return { text: before + inner + after, selStart: s.selStart, selEnd: s.selStart + inner.length };
  }

  const outerWrapped =
    before.endsWith(marker) &&
    after.startsWith(marker) &&
    !before.slice(0, -n).endsWith(marker[0]) &&
    !after.slice(n).startsWith(marker[0]);
  if (outerWrapped) {
    const nb = before.slice(0, -n);
    const na = after.slice(n);
    return { text: nb + sel + na, selStart: nb.length, selEnd: nb.length + sel.length };
  }

  // Nothing selected: drop the markers in and park the caret between them.
  if (sel === "") {
    const text = before + marker + marker + after;
    const caret = s.selStart + n;
    return { text, selStart: caret, selEnd: caret };
  }

  const text = before + marker + sel + marker + after;
  return { text, selStart: s.selStart + n, selEnd: s.selStart + n + sel.length };
}

// lineRange expands the selection to whole lines, which is what every
// prefix-based action operates on.
function lineRange(text: string, selStart: number, selEnd: number): { from: number; to: number } {
  const from = text.lastIndexOf("\n", selStart - 1) + 1;
  const nextNL = text.indexOf("\n", selEnd);
  // A selection ending exactly at a newline shouldn't drag in the next line.
  const to = nextNL === -1 ? text.length : nextNL;
  return { from, to };
}

// mapLines rewrites each line in the selection (expanded to line bounds) and
// re-selects the rewritten block, so a follow-up click acts on the same lines.
function mapLines(s: EditState, fn: (line: string, i: number) => string): EditState {
  const { from, to } = lineRange(s.text, s.selStart, s.selEnd);
  const block = s.text.slice(from, to);
  const next = block.split("\n").map(fn).join("\n");
  const text = s.text.slice(0, from) + next + s.text.slice(to);
  // Caret-only invocations keep a caret, offset by what was added to its line.
  if (s.selStart === s.selEnd) {
    const lineStart = s.text.lastIndexOf("\n", s.selStart - 1) + 1;
    const idx = block.split("\n").findIndex((_, i) => {
      const offsets = block.split("\n").slice(0, i).reduce((a, l) => a + l.length + 1, 0);
      return from + offsets === lineStart;
    });
    const lines = block.split("\n");
    const delta = idx >= 0 ? fn(lines[idx], idx).length - lines[idx].length : 0;
    const caret = s.selStart + delta;
    return { text, selStart: caret, selEnd: caret };
  }
  return { text, selStart: from, selEnd: from + next.length };
}

// BULLET/ORDERED/QUOTE prefixes, for stripping as well as adding.
const BULLET_RE = /^(\s*)[-*+] +/;
const ORDERED_RE = /^(\s*)\d+\. +/;
const QUOTE_RE = /^(\s*)> ?/;

// togglePrefix adds a line prefix, or strips it when every non-blank line in
// range already has it. Blank lines are skipped: "- " alone renders as an empty
// list item and splits the list in two.
function togglePrefix(
  s: EditState,
  re: RegExp,
  add: (line: string, i: number) => string,
): EditState {
  const { from, to } = lineRange(s.text, s.selStart, s.selEnd);
  const lines = s.text.slice(from, to).split("\n");
  const meaningful = lines.filter((l) => l.trim() !== "");
  const allPrefixed = meaningful.length > 0 && meaningful.every((l) => re.test(l));
  return mapLines(s, (line, i) => {
    if (line.trim() === "") return line;
    return allPrefixed ? line.replace(re, "$1") : add(line, i);
  });
}

// toggleHeading swaps the heading level on the caret's line. Pressing the same
// level again clears it; a different level replaces it rather than stacking
// hashes (which is what naive prefixing produces).
function toggleHeading(s: EditState, hashes: string): EditState {
  return mapLines(s, (line) => {
    const m = /^(#{1,6}) +/.exec(line);
    if (m) {
      const body = line.slice(m[0].length);
      return m[1] === hashes ? body : `${hashes} ${body}`;
    }
    return `${hashes} ${line}`;
  });
}

function makeLink(s: EditState): EditState {
  const sel = s.text.slice(s.selStart, s.selEnd);
  const before = s.text.slice(0, s.selStart);
  const after = s.text.slice(s.selEnd);
  // A selected URL is the target; anything else is the link text. This makes
  // both natural orders work — select words then click, or paste a URL,
  // select it, then click.
  if (URLISH.test(sel)) {
    const placeholder = "text";
    const text = `${before}[${placeholder}](${sel})${after}`;
    const start = before.length + 1;
    return { text, selStart: start, selEnd: start + placeholder.length };
  }
  const text = `${before}[${sel}]()${after}`;
  // With text selected, the label is already written — so aim at the URL.
  // With nothing selected, the label is what you'd type first, so aim between
  // the brackets instead.
  const caret = sel === "" ? before.length + 1 : before.length + sel.length + 3;
  return { text, selStart: caret, selEnd: caret };
}

function makeCodeBlock(s: EditState): EditState {
  const { from, to } = lineRange(s.text, s.selStart, s.selEnd);
  const block = s.text.slice(from, to);
  const fenced = "```\n" + block + "\n```";
  const text = s.text.slice(0, from) + fenced + s.text.slice(to);
  // Select the fenced body so a language can be typed over nothing, or the
  // content re-edited immediately.
  const start = from + 4;
  return { text, selStart: start, selEnd: start + block.length };
}

const TABLE = ["| Column | Column |", "| --- | --- |", "|  |  |"].join("\n");

function insertTable(s: EditState): EditState {
  const before = s.text.slice(0, s.selStart);
  const after = s.text.slice(s.selEnd);
  // A table must start on its own line or it renders as part of the paragraph
  // above it.
  const lead = before === "" || before.endsWith("\n") ? "" : "\n\n";
  const text = before + lead + TABLE + "\n" + after;
  const firstCell = before.length + lead.length + 2;
  return { text, selStart: firstCell, selEnd: firstCell + "Column".length };
}

function insertHr(s: EditState): EditState {
  const before = s.text.slice(0, s.selStart);
  const after = s.text.slice(s.selEnd);
  // Blank lines on both sides: "---" directly under text makes that text a
  // setext H2 instead of drawing a rule.
  const lead = before === "" || before.endsWith("\n\n") ? "" : before.endsWith("\n") ? "\n" : "\n\n";
  const text = before + lead + "---\n\n" + after;
  const caret = text.length - after.length;
  return { text, selStart: caret, selEnd: caret };
}

export function applyMarkdownAction(s: EditState, action: MarkdownAction): EditState {
  switch (action) {
    case "bold":
    case "italic":
    case "code":
      return toggleInline(s, INLINE[action]);
    case "h1":
    case "h2":
    case "h3":
      return toggleHeading(s, HEADING[action]);
    case "bullet":
      return togglePrefix(s, BULLET_RE, (line) => `- ${line}`);
    case "ordered":
      return togglePrefix(s, ORDERED_RE, (line, i) => `${i + 1}. ${line}`);
    case "quote":
      return togglePrefix(s, QUOTE_RE, (line) => `> ${line}`);
    case "link":
      return makeLink(s);
    case "codeblock":
      return makeCodeBlock(s);
    case "table":
      return insertTable(s);
    case "hr":
      return insertHr(s);
  }
}
