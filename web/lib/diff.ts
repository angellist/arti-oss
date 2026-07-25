// Pure logic behind the viewer's "Compare versions" action (TEXT artifacts
// only): which artifacts may be compared, and a line-level side-by-side diff
// of two versions' raw text. Kept framework-free so the diff invariants are
// unit-testable (see diff.test.ts); the React surface lives in
// components/ArtifactCompare.tsx.

import type { ArtifactInfo } from "./types";
import { isTextualContentType } from "./viewer";

// isComparableArtifact gates the ⋯-menu "Compare versions" item. Same
// eligibility as Edit (TEXT + textual content_type + a slug + not archived):
// comparison walks a slug's version history, so a slugless or non-text
// artifact has nothing to compare. NOT version-count gated — the count isn't
// known without a fetch, so ArtifactCompare reports "only one version" on open
// rather than pre-checking (and disabling) the menu item.
export function isComparableArtifact(
  info: Pick<ArtifactInfo, "artifact_type" | "content_type" | "named_slug" | "deleted_at">,
): boolean {
  return (
    !info.deleted_at &&
    info.artifact_type === "TEXT" &&
    isTextualContentType(info.content_type) &&
    !!info.named_slug
  );
}

// One run of a line's inline (word-level) diff: a stretch of text that is
// either shared with the paired opposite line (changed: false) or unique to
// this side (changed: true). Adjacent runs of the same kind are merged.
export interface DiffSeg {
  text: string;
  changed: boolean;
}

// One line on one side of the diff: its 1-based line number and text.
export interface DiffCell {
  num: number;
  text: string;
  // Inline word-level diff of this line against its paired opposite, present
  // ONLY on a paired changed row (see alignChanges). The `changed: true` runs
  // are the words that actually differ, so the renderer can highlight just
  // those instead of tinting the whole line.
  segments?: DiffSeg[];
}

// One aligned row of the split diff. A row is exactly one of:
//   - equal:    both left and right present (same text)
//   - deletion: left present, right null
//   - addition: left null, right present
export interface DiffRow {
  left: DiffCell | null;
  right: DiffCell | null;
}

export interface DiffResult {
  rows: DiffRow[];
  adds: number; // right-only rows
  dels: number; // left-only rows
  // true when the inputs were too large for the O(n·m) LCS and we fell back to
  // an unaligned positional pairing (see MAX_DIFF_CELLS).
  truncated: boolean;
}

// Product-of-line-counts ceiling above which we skip the O(n·m) LCS DP. ~2000×2000.
// Keeps a pathologically large artifact from hanging the tab / exhausting memory;
// real text artifacts are far below this.
export const MAX_DIFF_CELLS = 4_000_000;

// splitLines splits raw text into lines for diffing. CRLF is normalized to LF
// so a line-ending-only change isn't reported as a diff. An empty string is
// zero lines (not one empty line) so an empty version diffs cleanly against a
// non-empty one. A trailing newline yields a trailing empty line, faithful to
// the raw bytes.
function splitLines(s: string): string[] {
  if (s === "") return [];
  return s.replace(/\r\n/g, "\n").split("\n");
}

// computeLineDiff produces an aligned side-by-side line diff of two raw texts
// via a classic longest-common-subsequence walk. Above MAX_DIFF_CELLS it falls
// back to an unaligned positional dump (truncated: true).
export function computeLineDiff(a: string, b: string): DiffResult {
  const A = splitLines(a);
  const B = splitLines(b);
  const n = A.length;
  const m = B.length;

  if (n * m > MAX_DIFF_CELLS) {
    const rows: DiffRow[] = [];
    const len = Math.max(n, m);
    for (let i = 0; i < len; i++) {
      rows.push({
        left: i < n ? { num: i + 1, text: A[i] } : null,
        right: i < m ? { num: i + 1, text: B[i] } : null,
      });
    }
    return { rows, adds: 0, dels: 0, truncated: true };
  }

  // dp[i][j] = LCS length of A[i:] and B[j:]. Int32Array rows bound memory
  // (line counts fit comfortably in int32).
  const dp: Int32Array[] = Array.from({ length: n + 1 }, () => new Int32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) {
    const row = dp[i];
    const next = dp[i + 1];
    for (let j = m - 1; j >= 0; j--) {
      row[j] = A[i] === B[j] ? next[j + 1] + 1 : Math.max(next[j], row[j + 1]);
    }
  }

  const rows: DiffRow[] = [];
  let adds = 0;
  let dels = 0;
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (A[i] === B[j]) {
      rows.push({ left: { num: i + 1, text: A[i] }, right: { num: j + 1, text: B[j] } });
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      // Prefer showing the deletion first on a tie so a changed line reads as
      // remove-then-add, matching the left→right (old→new) orientation.
      rows.push({ left: { num: i + 1, text: A[i] }, right: null });
      dels++;
      i++;
    } else {
      rows.push({ left: null, right: { num: j + 1, text: B[j] } });
      adds++;
      j++;
    }
  }
  while (i < n) {
    rows.push({ left: { num: i + 1, text: A[i] }, right: null });
    dels++;
    i++;
  }
  while (j < m) {
    rows.push({ left: null, right: { num: j + 1, text: B[j] } });
    adds++;
    j++;
  }

  return { rows: alignChanges(rows), adds, dels, truncated: false };
}

// INLINE_TOKEN_PRODUCT_CAP bounds the O(n·m) token-LCS below. Past it (very long
// lines) inline word-diffing isn't worth the cost, so we degrade to a whole-line
// change. ~500×500 tokens; real lines sit far below.
export const INLINE_TOKEN_PRODUCT_CAP = 250_000;

// tokenize splits a line into diffable tokens: runs of whitespace, runs of word
// characters, or a single other char (so `one.` vs `one,` differ only in the
// punctuation). An empty string yields no tokens.
function tokenize(s: string): string[] {
  return s.match(/\s+|\w+|[^\s\w]/g) ?? [];
}

// computeInlineSegments word-level-diffs two lines via the same LCS walk as
// computeLineDiff, one token at a time, splitting each side into shared
// (changed: false) and unique (changed: true) runs so a modified line can
// highlight just the words that changed. Above the token-product cap it degrades
// to a single whole-line changed segment (empty string → no segments).
export function computeInlineSegments(a: string, b: string): { left: DiffSeg[]; right: DiffSeg[] } {
  const A = tokenize(a);
  const B = tokenize(b);
  const n = A.length;
  const m = B.length;
  if (n * m > INLINE_TOKEN_PRODUCT_CAP) {
    return {
      left: a === "" ? [] : [{ text: a, changed: true }],
      right: b === "" ? [] : [{ text: b, changed: true }],
    };
  }

  const dp: Int32Array[] = Array.from({ length: n + 1 }, () => new Int32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) {
    const row = dp[i];
    const next = dp[i + 1];
    for (let j = m - 1; j >= 0; j--) {
      row[j] = A[i] === B[j] ? next[j + 1] + 1 : Math.max(next[j], row[j + 1]);
    }
  }

  const left: DiffSeg[] = [];
  const right: DiffSeg[] = [];
  // push appends a token to a side, coalescing with the previous run when it
  // shares the same changed flag (so each segment is a maximal run).
  const push = (segs: DiffSeg[], text: string, changed: boolean) => {
    const last = segs[segs.length - 1];
    if (last && last.changed === changed) last.text += text;
    else segs.push({ text, changed });
  };
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (A[i] === B[j]) {
      push(left, A[i], false);
      push(right, B[j], false);
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      push(left, A[i], true);
      i++;
    } else {
      push(right, B[j], true);
      j++;
    }
  }
  while (i < n) push(left, A[i++], true);
  while (j < m) push(right, B[j++], true);
  return { left, right };
}

// alignChanges post-processes computeLineDiff's raw LCS rows — which emit a
// changed line as a removed block followed by an added block — into a
// GitHub-style split view: each adjacent removed-run→added-run is zipped into
// paired change rows (old on the left, new on the right, with inline word
// segments), and any surplus removals/additions stay single-sided. Equal rows
// and lone insertions/deletions are untouched. Counts are unaffected — a paired
// row is still one deletion plus one addition.
function alignChanges(rows: DiffRow[]): DiffRow[] {
  const out: DiffRow[] = [];
  let i = 0;
  while (i < rows.length) {
    // A change hunk is a maximal run of pure deletions immediately followed by
    // a maximal run of pure additions.
    if (rows[i].left && !rows[i].right) {
      let d = i;
      while (d < rows.length && rows[d].left && !rows[d].right) d++;
      let a = d;
      while (a < rows.length && !rows[a].left && rows[a].right) a++;
      if (a > d) {
        const dels = rows.slice(i, d);
        const adds = rows.slice(d, a);
        const paired = Math.min(dels.length, adds.length);
        for (let x = 0; x < paired; x++) {
          const l = dels[x].left!;
          const r = adds[x].right!;
          const seg = computeInlineSegments(l.text, r.text);
          out.push({
            left: { ...l, segments: seg.left.length ? seg.left : undefined },
            right: { ...r, segments: seg.right.length ? seg.right : undefined },
          });
        }
        for (let x = paired; x < dels.length; x++) out.push(dels[x]);
        for (let x = paired; x < adds.length; x++) out.push(adds[x]);
        i = a;
        continue;
      }
      // Pure deletion run (no additions follow): leave the rows as-is.
      for (; i < d; i++) out.push(rows[i]);
      continue;
    }
    out.push(rows[i]);
    i++;
  }
  return out;
}

// How one side of a diff row should be styled: an unchanged line, a removed
// line (left), an added line (right), or an empty filler opposite a change.
export type SideKind = "equal" | "del" | "add" | "filler";

// rowKinds classifies both sides of a diff row for rendering. A row with both
// sides present but DIFFERING text is a change (del on the left, add on the
// right), NOT an equal row. This only arises in the truncated fallback (which
// pairs lines positionally without LCS alignment) — styling those neutral would
// make differing lines look unchanged, contradicting the "too large to align"
// banner. In the normal LCS path a both-present row always has identical text,
// so it stays "equal" there and this changes nothing.
export function rowKinds(row: DiffRow): { left: SideKind; right: SideKind } {
  if (row.left && row.right) {
    return row.left.text === row.right.text
      ? { left: "equal", right: "equal" }
      : { left: "del", right: "add" };
  }
  if (row.left) return { left: "del", right: "filler" };
  if (row.right) return { left: "filler", right: "add" };
  return { left: "filler", right: "filler" };
}
