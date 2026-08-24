// MenuRow — the shared geometry for rows in the viewer's ⋮ overflow menu.
//
// One rule: every row is [14px icon slot][8px gap][label]. Rows render the
// slot even when they have no glyph, so the label column starts at the same x
// down the whole menu. Before this existed each row hand-rolled its own
// padding — Edit/Compare had a 12px <svg>, Download carried a "↓" inside the
// label string, Comments led with a 6px dot and Archive had nothing at all,
// so four rows started their text at four different offsets.
//
// It lives in its own module (not in ArtifactViewer) because CommentsMenuItem
// also needs it, and ArtifactViewer imports CommentsMenuItem — sharing it from
// the viewer would make that import cycle.

export const MENU_ROW =
  "flex w-full items-center gap-2 px-3 py-1.5 text-left text-[12px] text-neutral-700";

// Shared stroke geometry, so a glyph in one row has the same weight and
// optical size as the next.
export const MENU_SVG = {
  width: 12,
  height: 12,
  viewBox: "0 0 24 24",
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 2,
  strokeLinecap: "round",
  strokeLinejoin: "round",
} as const;

export function MenuIcon({ children }: { children?: React.ReactNode }) {
  return (
    <span
      className="flex h-3.5 w-3.5 shrink-0 items-center justify-center text-neutral-400"
      aria-hidden="true"
    >
      {children}
    </span>
  );
}
