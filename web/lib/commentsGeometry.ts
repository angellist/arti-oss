// Horizontal geometry for the comments overlay: how far left to slide the
// document (and the toolbar) so margin cards stop clipping the text.
//
// Extracted from mountCommentsOverlay() as pure arithmetic over measurements
// because it is the part of the overlay that has broken twice in review — first
// cards clipping the text, then the shift silently collapsing to 0 on windows
// under ~1700px because the doc and the toolbar shared one cap. Inside the
// closure none of it was reachable from a test; the DOM reads stay there, the
// arithmetic lives here.

// The fixed card column, as three values that MUST agree: the shift is computed
// from the footprint, the cards are placed with the offset, and the CSS sizes
// them with the width. They were three independent literals — `78` in place(),
// `344` in the .ac-card/.ac-panel rules and again as an offsetWidth fallback,
// and their sum here — so widening a card in CSS would have left the shift
// under-compensating and cards clipping the text again, which is bug #135
// returning silently. A test cannot catch that: it would read this constant and
// move with it. So the CSS and place() now derive from these instead.
export const CARD_RIGHT = 78;
export const CARD_WIDTH = 344;
export const CARD_FOOTPRINT = CARD_RIGHT + CARD_WIDTH;
// Gap between the doc's right edge and the card column.
export const SHIFT_GAP = 20;
// Keep at least this much left margin (≈ px-6).
export const SHIFT_LMIN = 24;

// The comment rail (the segmented capsule pinned to the right edge) as CSS
// sees it, so anything else living in the right gutter can keep clear of it.
export const RAIL_RIGHT = 8;
export const RAIL_WIDTH = 26;
// Gap the rail wants around itself. RAIL_FOOTPRINT is the total keep-out strip
// measured from the viewport's right edge.
export const RAIL_GAP = 8;
export const RAIL_FOOTPRINT = RAIL_RIGHT + RAIL_WIDTH + RAIL_GAP;

/**
 * Left edge (px) of a collapsed-thread marker ("comment chip").
 *
 * The chip hugs the text — it parks just past the document's right edge, which
 * is the LEFT side of the comment gutter, so it reads as belonging to the
 * paragraph it annotates. On a wide document (a full-width served page) that
 * position runs off the viewport and lands under the rail, which is bug-shaped:
 * the chip was clamped only to `viewportWidth - width - 8`, i.e. exactly the
 * strip the rail occupies. So the hug is capped at the CARD column's right edge
 * instead: chips and cards share one stack, and sharing a right edge both keeps
 * them visually aligned and leaves the rail's footprint free (CARD_RIGHT=78 >
 * RAIL_FOOTPRINT=42).
 */
export function minMarkerLeft(m: {
  /** container.getBoundingClientRect().right, after the doc shift */
  containerRight: number;
  viewportWidth: number;
  markerWidth: number;
  /** gap between the doc's right edge and the chip */
  gap?: number;
}): number {
  const hug = m.containerRight + (m.gap ?? 8);
  const rightLimit = m.viewportWidth - CARD_RIGHT - m.markerWidth;
  return Math.round(Math.max(8, Math.min(hug, rightLimit)));
}

/**
 * Top (px) of the rail for a remembered vertical position.
 *
 * `fraction` is the rail's CENTER as a share of viewport height (0–1), which is
 * what survives a resize or a move to another screen — a raw px top would drift
 * off-screen. The result is clamped so the whole capsule stays between the
 * sticky header (`minTop`) and the bottom edge, and `minTop` wins when the
 * viewport is too short for both.
 */
export function railTop(m: {
  viewportHeight: number;
  railHeight: number;
  minTop: number;
  fraction: number;
  /** gap kept at the bottom edge */
  gap?: number;
}): number {
  const gap = m.gap ?? 8;
  const maxTop = m.viewportHeight - m.railHeight - gap;
  const want = m.fraction * m.viewportHeight - m.railHeight / 2;
  return Math.round(Math.max(m.minTop, Math.min(want, Math.max(m.minTop, maxTop))));
}

export interface ShiftInput {
  /** window.innerWidth */
  viewportWidth: number;
  /** left edge of the <main> the doc sits in — the shift may not cross it */
  mainLeft: number;
  /** container.getBoundingClientRect().left, AS MEASURED (still translated) */
  docLeft: number;
  /** container.getBoundingClientRect().right, AS MEASURED (still translated) */
  docRight: number;
  /** left edge of the toolbar's inner element, AS MEASURED; falls back to docLeft */
  toolbarInnerLeft: number;
  /** px the doc is currently translated left by */
  docShift: number;
  /** px the toolbar is currently translated left by */
  tbShift: number;
}

export interface Shift {
  doc: number;
  tb: number;
}

/**
 * How far left the doc and toolbar should be translated.
 *
 * The inputs are raw measurements of an already-translated layout, so the
 * current shifts are added back first to recover the natural edges — otherwise
 * each call would measure its own previous output and the shift would creep.
 *
 * `tb` is computed from the toolbar's OWN slack and then capped at `doc`. The
 * two are deliberately not one number: the toolbar is always full-width while
 * the doc narrows with the width control, so a shared cap let the toolbar's
 * smaller slack zero out the doc's shift on narrower windows.
 */
export function computeShift(m: ShiftInput): Shift {
  // Undo the current translate → natural edges.
  const docLeft = m.docLeft + m.docShift;
  const docRight = m.docRight + m.docShift;

  const cardLeft = m.viewportWidth - CARD_FOOTPRINT;
  const needed = docRight - (cardLeft - SHIFT_GAP);
  const docSlack = Math.max(0, docLeft - m.mainLeft - SHIFT_LMIN);
  const doc = Math.max(0, Math.min(needed, docSlack));

  // Toolbar follows the doc, but never past its own left margin.
  const innerLeft = m.toolbarInnerLeft + m.tbShift;
  const tbSlack = Math.max(0, innerLeft - m.mainLeft - SHIFT_LMIN);
  return { doc, tb: Math.min(doc, tbSlack) };
}
