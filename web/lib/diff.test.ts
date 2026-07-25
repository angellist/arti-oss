import { describe, it, expect } from "vitest";
import { computeInlineSegments, computeLineDiff, isComparableArtifact, rowKinds, MAX_DIFF_CELLS } from "./diff";

const base = {
  artifact_type: "TEXT" as const,
  content_type: "text/markdown",
  named_slug: "my-doc",
  deleted_at: null,
};

describe("isComparableArtifact", () => {
  it("allows a live TEXT artifact with a textual content_type and a slug", () => {
    expect(isComparableArtifact(base)).toBe(true);
  });

  it("rejects a slugless artifact — there is no version history to walk", () => {
    expect(isComparableArtifact({ ...base, named_slug: null })).toBe(false);
    expect(isComparableArtifact({ ...base, named_slug: "" })).toBe(false);
  });

  it("rejects non-TEXT artifacts", () => {
    expect(isComparableArtifact({ ...base, artifact_type: "PACKAGE" })).toBe(false);
    expect(isComparableArtifact({ ...base, artifact_type: "APP" })).toBe(false);
    expect(isComparableArtifact({ ...base, artifact_type: "ATTACHMENT" })).toBe(false);
  });

  it("rejects a non-textual content_type (pdf/image/binary)", () => {
    expect(isComparableArtifact({ ...base, content_type: "application/pdf" })).toBe(false);
    expect(isComparableArtifact({ ...base, content_type: "image/png" })).toBe(false);
  });

  it("rejects an archived artifact", () => {
    expect(isComparableArtifact({ ...base, deleted_at: "2026-01-01T00:00:00Z" })).toBe(false);
  });
});

describe("computeLineDiff", () => {
  it("reports two identical texts as all-equal rows, zero adds/dels", () => {
    const d = computeLineDiff("a\nb\nc", "a\nb\nc");
    expect(d.adds).toBe(0);
    expect(d.dels).toBe(0);
    expect(d.truncated).toBe(false);
    expect(d.rows).toHaveLength(3);
    // Every row has both sides, with matching text and per-side numbering.
    d.rows.forEach((r, i) => {
      expect(r.left).toEqual({ num: i + 1, text: ["a", "b", "c"][i] });
      expect(r.right).toEqual({ num: i + 1, text: ["a", "b", "c"][i] });
    });
  });

  it("treats two empty strings as zero lines (identical, no rows)", () => {
    const d = computeLineDiff("", "");
    expect(d.rows).toHaveLength(0);
    expect(d.adds).toBe(0);
    expect(d.dels).toBe(0);
  });

  it("shows a pure addition as right-only rows", () => {
    const d = computeLineDiff("a\nb", "a\nx\nb");
    expect(d.adds).toBe(1);
    expect(d.dels).toBe(0);
    const added = d.rows.filter((r) => r.left === null);
    expect(added).toHaveLength(1);
    expect(added[0].right).toEqual({ num: 2, text: "x" });
  });

  it("shows a pure deletion as left-only rows", () => {
    const d = computeLineDiff("a\nx\nb", "a\nb");
    expect(d.adds).toBe(0);
    expect(d.dels).toBe(1);
    const removed = d.rows.filter((r) => r.right === null);
    expect(removed).toHaveLength(1);
    expect(removed[0].left).toEqual({ num: 2, text: "x" });
  });

  it("aligns a changed line onto ONE row with old|new side by side (not del-then-add)", () => {
    const d = computeLineDiff("a\nb\nc", "a\nx\nc");
    expect(d.adds).toBe(1);
    expect(d.dels).toBe(1);
    // equal(a), CHANGED(b|x) on a single row, equal(c) — so a small edit reads
    // as one modified row instead of a removed block above an added block.
    expect(d.rows).toHaveLength(3);
    expect(d.rows.map((r) => [r.left?.text ?? null, r.right?.text ?? null])).toEqual([
      ["a", "a"],
      ["b", "x"],
      ["c", "c"],
    ]);
    // The changed row keeps each side's own line number.
    expect(d.rows[1].left?.num).toBe(2);
    expect(d.rows[1].right?.num).toBe(2);
  });

  it("pairs a changed block row-by-row, leaving surplus deletions single-sided", () => {
    // 2 removed vs 1 added between equal anchors: first pair aligns, the extra
    // deletion stays a left-only row.
    const d = computeLineDiff("a\nb1\nb2\nc", "a\nx1\nc");
    expect(d.dels).toBe(2);
    expect(d.adds).toBe(1);
    expect(d.rows.map((r) => [r.left?.text ?? null, r.right?.text ?? null])).toEqual([
      ["a", "a"],
      ["b1", "x1"],
      ["b2", null],
      ["c", "c"],
    ]);
  });

  it("pairs a changed block row-by-row, leaving surplus additions single-sided", () => {
    const d = computeLineDiff("a\nb1\nc", "a\nx1\nx2\nc");
    expect(d.dels).toBe(1);
    expect(d.adds).toBe(2);
    expect(d.rows.map((r) => [r.left?.text ?? null, r.right?.text ?? null])).toEqual([
      ["a", "a"],
      ["b1", "x1"],
      [null, "x2"],
      ["c", "c"],
    ]);
  });

  it("does NOT pair a deletion and addition separated by unchanged lines", () => {
    // Only an adjacent removed-run→added-run is a 'change'; unrelated edits far
    // apart must stay their own rows.
    const d = computeLineDiff("x\na\nb", "a\nb\ny");
    expect(d.rows.map((r) => [r.left?.text ?? null, r.right?.text ?? null])).toEqual([
      ["x", null],
      ["a", "a"],
      ["b", "b"],
      [null, "y"],
    ]);
  });

  it("attaches inline word segments to a paired changed row, but not to equal or single-sided rows", () => {
    const d = computeLineDiff("the quick brown fox\nsame", "the slow brown fox\nsame\nextra");
    const changed = d.rows[0];
    expect(changed.left?.segments?.filter((s) => s.changed).map((s) => s.text)).toEqual(["quick"]);
    expect(changed.right?.segments?.filter((s) => s.changed).map((s) => s.text)).toEqual(["slow"]);
    // Unchanged row and pure-addition row carry no inline segmentation.
    const equalRow = d.rows.find((r) => r.left?.text === "same");
    expect(equalRow?.left?.segments).toBeUndefined();
    const addRow = d.rows.find((r) => r.left === null && r.right?.text === "extra");
    expect(addRow?.right?.segments).toBeUndefined();
  });

  it("diffs an empty version against a non-empty one as all additions", () => {
    const d = computeLineDiff("", "a\nb");
    expect(d.dels).toBe(0);
    expect(d.adds).toBe(2);
    expect(d.rows.every((r) => r.left === null)).toBe(true);
  });

  it("normalizes CRLF so a line-ending-only difference is not a diff", () => {
    const d = computeLineDiff("a\r\nb\r\nc", "a\nb\nc");
    expect(d.adds).toBe(0);
    expect(d.dels).toBe(0);
  });

  it("treats a trailing newline as a trailing empty line (faithful to raw bytes)", () => {
    const withNl = computeLineDiff("a", "a\n");
    // "a" -> ["a"]; "a\n" -> ["a", ""] — the extra empty line is an addition.
    expect(withNl.adds).toBe(1);
    expect(withNl.dels).toBe(0);
  });

  it("falls back to an unaligned positional dump above the cell cap", () => {
    // Build two texts whose line-count product exceeds the cap so the O(n·m)
    // DP is skipped. Keep them cheap to construct.
    const n = Math.ceil(Math.sqrt(MAX_DIFF_CELLS)) + 1; // n*n > cap
    const a = Array.from({ length: n }, (_, i) => `L${i}`).join("\n");
    const b = Array.from({ length: n }, (_, i) => `R${i}`).join("\n");
    const d = computeLineDiff(a, b);
    expect(d.truncated).toBe(true);
    expect(d.rows).toHaveLength(n);
    // Positional pairing: row i carries left line i and right line i, unaligned.
    expect(d.rows[0].left?.text).toBe("L0");
    expect(d.rows[0].right?.text).toBe("R0");
  });
});

describe("computeInlineSegments", () => {
  it("marks only the differing token, leaving the shared prefix/suffix unchanged", () => {
    const { left, right } = computeInlineSegments("the quick brown fox", "the slow brown fox");
    // Segments must reconstruct each side exactly (no dropped/duplicated chars).
    expect(left.map((s) => s.text).join("")).toBe("the quick brown fox");
    expect(right.map((s) => s.text).join("")).toBe("the slow brown fox");
    expect(left.filter((s) => s.changed).map((s) => s.text)).toEqual(["quick"]);
    expect(right.filter((s) => s.changed).map((s) => s.text)).toEqual(["slow"]);
  });

  it("returns a single unchanged segment for identical text", () => {
    expect(computeInlineSegments("same here", "same here")).toEqual({
      left: [{ text: "same here", changed: false }],
      right: [{ text: "same here", changed: false }],
    });
  });

  it("marks the whole of each side changed when nothing is shared", () => {
    expect(computeInlineSegments("aaa", "bbb")).toEqual({
      left: [{ text: "aaa", changed: true }],
      right: [{ text: "bbb", changed: true }],
    });
  });

  it("degrades to a whole-line change above the token cap (no O(n^2) blowup)", () => {
    const a = "x ".repeat(2000);
    const b = "y ".repeat(2000);
    expect(computeInlineSegments(a, b)).toEqual({
      left: [{ text: a, changed: true }],
      right: [{ text: b, changed: true }],
    });
  });
});

describe("rowKinds", () => {
  it("classifies an unchanged (both-present, same text) row as equal/equal", () => {
    expect(rowKinds({ left: { num: 1, text: "x" }, right: { num: 1, text: "x" } })).toEqual({
      left: "equal",
      right: "equal",
    });
  });

  it("classifies a both-present row with DIFFERING text as del/add (the truncated-fallback case)", () => {
    // This is exactly the row shape the truncated fallback emits: both sides
    // present but different — it must read as a change, not neutral 'equal'.
    expect(rowKinds({ left: { num: 3, text: "old" }, right: { num: 3, text: "new" } })).toEqual({
      left: "del",
      right: "add",
    });
  });

  it("classifies a left-only row as del/filler and a right-only row as filler/add", () => {
    expect(rowKinds({ left: { num: 2, text: "gone" }, right: null })).toEqual({
      left: "del",
      right: "filler",
    });
    expect(rowKinds({ left: null, right: { num: 2, text: "new" } })).toEqual({
      left: "filler",
      right: "add",
    });
  });

  it("tints differing lines in a truncated diff instead of showing them as unchanged", () => {
    // End to end: a truncated diff pairs differing lines positionally; rowKinds
    // must surface them as del/add so they don't render neutral under the
    // 'too large to align' banner.
    const n = Math.ceil(Math.sqrt(MAX_DIFF_CELLS)) + 1;
    const a = Array.from({ length: n }, (_, i) => `L${i}`).join("\n");
    const b = Array.from({ length: n }, (_, i) => `R${i}`).join("\n");
    const d = computeLineDiff(a, b);
    expect(d.truncated).toBe(true);
    expect(rowKinds(d.rows[0])).toEqual({ left: "del", right: "add" });
  });
});
