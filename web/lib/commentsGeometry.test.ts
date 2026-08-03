import { describe, it, expect } from "vitest";
import {
  computeShift,
  CARD_FOOTPRINT,
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
