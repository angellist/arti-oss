// Chip and pill classes shared by every listing that renders artifact
// metadata — the catalog table, its mobile cards, the apps portal — so a
// label chip is the same object everywhere rather than several that merely
// started out alike.
export const TYPE_PILL =
  "inline-block rounded bg-neutral-100 px-1.5 py-0.5 text-[11px] text-neutral-600";
export const SCOPE_CHIP =
  "inline-block rounded-full bg-purple-50 px-2 py-0.5 text-[11px] text-purple-800 ring-1 ring-purple-200 transition hover:bg-purple-100";
export const LABEL_CHIP =
  "inline-block rounded-full bg-neutral-100 px-2 py-0.5 text-[11px] text-neutral-700 ring-1 ring-neutral-200 transition hover:bg-neutral-200";
