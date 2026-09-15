import { describe, it, expect } from "vitest";
import {
  computeShift,
  columnLeft,
  railTop,
  CARD_FOOTPRINT,
  CARD_RIGHT,
  CARD_WIDTH,
  RAIL_FOOTPRINT,
  SHIFT_GAP,
  SHIFT_LMIN,
  type ShiftInput,
} from "./commentsGeometry";

// A doc laid out inside a <main> that starts at x=260 (the rail), untranslated.
const base = (over: Partial<ShiftInput> = {}): ShiftInput => ({
  viewportWidth: 1440,
  mainLeft: 260,
  docLeft: 400,
  docRight: 1200,
  toolbarInnerLeft: 400,
  docShift: 0,
  tbShift: 0,
  ...over,
});

describe("computeShift", () => {
  it("does not move a doc that already clears the card column", () => {
    // Cards start at 1440-422=1018; with the 20px gap the doc may reach 998.
    const { doc, tb } = computeShift(base({ docRight: 900 }));
    expect(doc).toBe(0);
    expect(tb).toBe(0);
  });

  it("shifts by exactly the overlap when there is room to the left", () => {
    // docRight 1200 vs allowed 998 → needs 202; slack is 400-260-24 = 116.
    // So it shifts by the slack, not the full need.
    const { doc } = computeShift(base());
    expect(doc).toBe(116);
  });

  it("shifts by the full overlap when that is less than the available slack", () => {
    // Wide left margin: slack = 700-260-24 = 416. Need = 1050-998 = 52.
    const { doc } = computeShift(base({ docLeft: 700, docRight: 1050 }));
    expect(doc).toBe(52);
  });

  it("never pushes the doc past the main's left margin", () => {
    const m = base({ docLeft: 280, docRight: 1400 });
    const { doc } = computeShift(m);
    // Slack is only 280-260-24 → negative → clamped to 0.
    expect(doc).toBe(0);
    // And the doc's left edge would never cross mainLeft + SHIFT_LMIN.
    expect(m.docLeft - doc).toBeGreaterThanOrEqual(m.mainLeft);
  });

  it("is idempotent: re-measuring an already-shifted layout does not creep", () => {
    const first = computeShift(base());
    // A real second call measures the TRANSLATED rects, and passes the current
    // shift so the natural edges can be recovered. If the un-translation were
    // dropped, the shift would compound on every place() — scroll, resize, or
    // any re-render.
    const second = computeShift(
      base({
        docLeft: 400 - first.doc,
        docRight: 1200 - first.doc,
        toolbarInnerLeft: 400 - first.tb,
        docShift: first.doc,
        tbShift: first.tb,
      }),
    );
    expect(second).toEqual(first);
  });

  // The #137 regression. The toolbar is always full-width while the doc narrows
  // with the width control, so the toolbar's inner element starts further left
  // and has LESS slack. When both shared one cap, the toolbar's smaller slack
  // silently zeroed the doc's shift and cards clipped the text again — only on
  // windows narrow enough for the toolbar to be the binding constraint.
  describe("doc and toolbar shifts are decoupled", () => {
    const narrow = base({
      viewportWidth: 1500,
      mainLeft: 260,
      docLeft: 450,
      docRight: 1250,
      // Full-width toolbar: its inner edge sits at the main's own padding, so it
      // has no slack to give.
      toolbarInnerLeft: 284,
    });

    it("still shifts the doc when the toolbar cannot move at all", () => {
      const { doc, tb } = computeShift(narrow);
      expect(tb).toBe(0); // toolbar is pinned: 284-260-24 = 0
      expect(doc).toBeGreaterThan(0); // ...and the doc must move regardless
    });

    it("caps the toolbar at the doc's shift, never beyond it", () => {
      const { doc, tb } = computeShift(base({ toolbarInnerLeft: 900 }));
      expect(tb).toBeLessThanOrEqual(doc);
    });
  });

  it("keeps the doc clear of the cards once shifted, whenever slack allows", () => {
    const m = base({ docLeft: 700, docRight: 1050 });
    const { doc } = computeShift(m);
    const cardLeft = m.viewportWidth - CARD_FOOTPRINT;
    expect(m.docRight - doc).toBeLessThanOrEqual(cardLeft - SHIFT_GAP);
  });

  it("leaves at least SHIFT_LMIN of left margin", () => {
    const m = base({ docLeft: 500, docRight: 1400 });
    const { doc } = computeShift(m);
    expect(m.docLeft - doc - m.mainLeft).toBeGreaterThanOrEqual(SHIFT_LMIN);
  });
});

describe("columnLeft", () => {
  const col = { viewportWidth: 1440 };

  it("hugs the document's right edge when there is room", () => {
    expect(columnLeft({ ...col, containerRight: 900 })).toBe(920);
  });

  it("keeps a whole card on screen and clear of the rail on a full-width doc", () => {
    // The doc runs the whole viewport (a served page): hugging it would put the
    // column at 1460 — off-screen, and through the rail on the way out.
    const left = columnLeft({ ...col, containerRight: 1440 });
    expect(left + CARD_WIDTH).toBe(col.viewportWidth - CARD_RIGHT);
    expect(left + CARD_WIDTH).toBeLessThanOrEqual(col.viewportWidth - RAIL_FOOTPRINT);
  });

  it("never goes negative on a viewport narrower than the gutter itself", () => {
    expect(columnLeft({ containerRight: 100, viewportWidth: 100 })).toBe(8);
  });

  // The whole point of sharing SHIFT_GAP with computeShift: a doc that had to
  // move to make room lands with its right edge exactly one gap left of the
  // card column, so the hug and the cap agree instead of the column sitting a
  // few px short of where the shift was computed for.
  it("lands exactly on the card column once the doc has shifted for it", () => {
    const m = base({ docLeft: 700, docRight: 1050 });
    const { doc } = computeShift(m);
    expect(columnLeft({ viewportWidth: m.viewportWidth, containerRight: m.docRight - doc })).toBe(
      m.viewportWidth - CARD_FOOTPRINT,
    );
  });
});

describe("railTop", () => {
  const rail = { viewportHeight: 900, railHeight: 100, minTop: 60 };

  it("centers the capsule on the remembered fraction", () => {
    expect(railTop({ ...rail, fraction: 0.5 })).toBe(400); // 450 - 50
  });

  it("keeps the capsule below the sticky header", () => {
    expect(railTop({ ...rail, fraction: 0.01 })).toBe(60);
  });

  it("keeps the capsule inside the bottom edge", () => {
    expect(railTop({ ...rail, fraction: 0.99 })).toBe(792); // 900 - 100 - 8
  });

  it("prefers the header over the bottom edge when the viewport is too short", () => {
    expect(railTop({ viewportHeight: 140, railHeight: 100, minTop: 60, fraction: 0.9 })).toBe(60);
  });
});
