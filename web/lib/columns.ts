// The catalog table's column model: which columns exist, how wide they are by
// default, and how a user's per-browser customization (order, visibility,
// widths) survives a reload.
//
// Two rules shape everything here:
//
//  1. **The registry is the only place a column is declared.** CatalogTable
//     renders whatever `visibleColumns()` returns, in that order — it never
//     hard-codes a `<th>` list. Adding a column is a registry entry plus a
//     `case` in the cell renderer.
//
//  2. **Stored prefs are advisory, never authoritative.** They come from a
//     cookie the user's browser has held for up to a year, written by an older
//     build that may have had different columns. So every read goes through
//     `normalizeColumnPrefs`, which drops keys that no longer exist, appends
//     keys that didn't exist when the cookie was written (in their default
//     position), and clamps widths. A stale cookie can therefore never hide a
//     new column, blank the table, or wedge a column at 4px.

import type { SortField } from "./arti";

export type ColumnKey =
  | "title"
  | "slug"
  | "version"
  | "creator"
  | "scope"
  | "type"
  | "content_type"
  | "description"
  | "size"
  | "comments"
  | "access"
  | "modified"
  | "created"
  | "archived"
  | "id";

export interface ColumnDef {
  key: ColumnKey;
  /** Header text. */
  label: string;
  /** API sort field, or null when the column can't be sorted server-side. */
  sort: SortField | null;
  /** Default width in px. */
  width: number;
  /** Floor for a drag-resize — below this a column is unreadable, not compact. */
  minWidth: number;
  /** In the default view? Everything else is opt-in via the column menu. */
  defaultVisible: boolean;
  /** Right-align the cell (numeric columns). */
  numeric?: boolean;
  /** One-line explanation, shown in the column menu. */
  help: string;
}

// Declaration order == default column order.
//
// The default widths of the always-on columns are deliberately modest: they
// sum to just under a typical ~1200px content area, so the default catalog
// fills the width without a horizontal scrollbar and title — the column people
// actually read — keeps the largest share, as it had under the old auto
// layout. Every pixel handed to a metadata column comes out of the title.
export const COLUMNS: ColumnDef[] = [
  {
    key: "title",
    label: "title",
    sort: "title",
    width: 340,
    minWidth: 160,
    defaultVisible: true,
    help: "artifact title, with search snippets",
  },
  {
    key: "slug",
    label: "slug",
    sort: "slug",
    width: 150,
    minWidth: 80,
    defaultVisible: true,
    help: "named address; click to see every version",
  },
  {
    key: "version",
    label: "v",
    sort: "version",
    width: 56,
    minWidth: 48,
    defaultVisible: true,
    help: "version number of the row",
  },
  {
    key: "creator",
    label: "creator",
    sort: "creator",
    width: 120,
    minWidth: 72,
    defaultVisible: true,
    help: "who uploaded this version",
  },
  {
    key: "scope",
    label: "scope · labels",
    sort: "scope",
    width: 280,
    minWidth: 120,
    defaultVisible: true,
    help: "organizational tags (sorts by first scope)",
  },
  {
    key: "type",
    label: "type",
    sort: "type",
    width: 100,
    minWidth: 72,
    defaultVisible: true,
    help: "TEXT / PACKAGE / APP / ATTACHMENT",
  },
  {
    key: "created",
    label: "created",
    sort: "created",
    width: 116,
    minWidth: 88,
    defaultVisible: true,
    help: "when this version was uploaded",
  },
  // ── opt-in below ────────────────────────────────────────────────────────
  {
    key: "comments",
    label: "comments",
    sort: null,
    width: 104,
    minWidth: 72,
    defaultVisible: false,
    numeric: true,
    help: "comments on this version; ● marks unresolved threads",
  },
  {
    key: "description",
    label: "description",
    sort: null,
    width: 280,
    minWidth: 120,
    defaultVisible: false,
    help: "the artifact's description, if it has one",
  },
  {
    key: "content_type",
    label: "content type",
    sort: null,
    width: 160,
    minWidth: 96,
    defaultVisible: false,
    help: "MIME type (also shown under `type`)",
  },
  {
    key: "size",
    label: "size",
    sort: null,
    width: 96,
    minWidth: 64,
    defaultVisible: false,
    numeric: true,
    help: "stored byte size of the content",
  },
  {
    key: "access",
    label: "access",
    sort: null,
    width: 176,
    minWidth: 96,
    defaultVisible: false,
    help: "who can read it: everyone, a group/glob list, or private",
  },
  {
    key: "modified",
    label: "modified",
    sort: null,
    width: 144,
    minWidth: 88,
    defaultVisible: false,
    help: "last metadata/content change to this version",
  },
  {
    key: "archived",
    label: "archived",
    sort: "archived",
    width: 144,
    minWidth: 88,
    defaultVisible: false,
    help: "when it was archived (blank for live rows)",
  },
  {
    key: "id",
    label: "id",
    sort: null,
    width: 128,
    minWidth: 88,
    defaultVisible: false,
    help: "artifact UUID (this exact version)",
  },
];

const BY_KEY = new Map<ColumnKey, ColumnDef>(COLUMNS.map((c) => [c.key, c]));

export function columnDef(key: ColumnKey): ColumnDef | undefined {
  return BY_KEY.get(key);
}

export const DEFAULT_ORDER: ColumnKey[] = COLUMNS.map((c) => c.key);
const DEFAULT_HIDDEN: ColumnKey[] = COLUMNS.filter((c) => !c.defaultVisible).map((c) => c.key);

/** A column can't be dragged narrower than its minWidth or wider than this. */
export const MAX_COLUMN_WIDTH = 900;

export interface ColumnPrefs {
  /** Every known column, in display order (hidden ones included). */
  order: ColumnKey[];
  /** Keys the user has unchecked. */
  hidden: ColumnKey[];
  /** Per-column overrides; absent == the registry default. */
  widths: Partial<Record<ColumnKey, number>>;
}

export function defaultColumnPrefs(): ColumnPrefs {
  return { order: [...DEFAULT_ORDER], hidden: [...DEFAULT_HIDDEN], widths: {} };
}

/** True when prefs are indistinguishable from a fresh browser's. */
export function isDefaultPrefs(p: ColumnPrefs): boolean {
  const d = defaultColumnPrefs();
  return (
    p.order.join(",") === d.order.join(",") &&
    [...p.hidden].sort().join(",") === [...d.hidden].sort().join(",") &&
    Object.keys(p.widths).length === 0
  );
}

/**
 * Repair an arbitrary parsed value into usable prefs.
 *
 * The important case is a cookie written by an older build: its `order` lists
 * only the columns that existed then. Simply trusting it would make every
 * column added later invisible *and* unlistable — the user would have no way
 * to get it back short of clearing cookies. So missing keys are spliced back in
 * at their default neighbours' position, and inherit their default visibility.
 */
export function normalizeColumnPrefs(raw: unknown): ColumnPrefs {
  const out = defaultColumnPrefs();
  if (!raw || typeof raw !== "object") return out;
  const r = raw as Partial<Record<keyof ColumnPrefs, unknown>>;

  const known = (v: unknown): v is ColumnKey => typeof v === "string" && BY_KEY.has(v as ColumnKey);

  // Order: keep the stored sequence (deduped, unknowns dropped), then insert
  // any column the cookie predates. Insertion uses the default order as the
  // reference so a new column lands next to where it was designed to sit, not
  // dumped at the end.
  const stored: ColumnKey[] = [];
  const seen = new Set<ColumnKey>();
  if (Array.isArray(r.order)) {
    for (const k of r.order) {
      if (known(k) && !seen.has(k)) {
        seen.add(k);
        stored.push(k);
      }
    }
  }
  if (stored.length === 0) {
    out.order = [...DEFAULT_ORDER];
  } else {
    const order = [...stored];
    for (const k of DEFAULT_ORDER) {
      if (seen.has(k)) continue;
      // Anchor on the nearest preceding default column that IS in the stored
      // order; fall back to the front when there is none.
      const di = DEFAULT_ORDER.indexOf(k);
      let at = 0;
      for (let i = di - 1; i >= 0; i--) {
        const prev = DEFAULT_ORDER[i];
        const pos = order.indexOf(prev);
        if (pos >= 0) {
          at = pos + 1;
          break;
        }
      }
      order.splice(at, 0, k);
    }
    out.order = order;
  }

  // Hidden. `seen` doubles as "the keys the writing build knew about" — which
  // is why serializeColumnPrefs always emits the full order. A column the
  // cookie predates isn't in it, so it keeps its CURRENT default visibility
  // instead of being forced visible by its absence from `hidden`.
  if (Array.isArray(r.hidden) || stored.length > 0) {
    const hidden = new Set<ColumnKey>();
    if (Array.isArray(r.hidden)) for (const k of r.hidden) if (known(k)) hidden.add(k);
    for (const k of DEFAULT_ORDER) {
      if (!seen.has(k) && DEFAULT_HIDDEN.includes(k)) hidden.add(k);
    }
    out.hidden = [...hidden];
  }
  // Never let a stored pref hide everything: an empty table with no visible
  // header is unrecoverable through the UI (there is nothing left to
  // right-click). Title is the one column that always stays.
  if (out.hidden.length >= out.order.length) out.hidden = out.hidden.filter((k) => k !== "title");

  if (r.widths && typeof r.widths === "object") {
    for (const [k, v] of Object.entries(r.widths as Record<string, unknown>)) {
      if (!known(k)) continue;
      const n = typeof v === "number" ? v : Number(v);
      if (!Number.isFinite(n)) continue;
      out.widths[k] = clampWidth(k, n);
    }
  }
  return out;
}

export function clampWidth(key: ColumnKey, px: number): number {
  const def = BY_KEY.get(key);
  const min = def?.minWidth ?? 64;
  return Math.round(Math.min(MAX_COLUMN_WIDTH, Math.max(min, px)));
}

export function widthOf(prefs: ColumnPrefs, key: ColumnKey): number {
  return prefs.widths[key] ?? BY_KEY.get(key)?.width ?? 160;
}

/**
 * Columns the single-slug drill-in shows whatever the stored prefs say.
 *
 * That view is one document's version history, and "which version did the
 * discussion happen on?" is a question it exists to answer — a count only
 * visible to people who went hunting in the column menu answers it for nobody.
 * Pinning is per-view and never writes to prefs: a user who keeps `comments`
 * off in the catalog still gets it here, and still has it off on the way back.
 */
export const SLUG_VIEW_PINNED: readonly ColumnKey[] = ["comments"];

/**
 * The columns to render, in order.
 *
 * `pinned` forces columns visible for one view without touching the user's
 * stored prefs (see SLUG_VIEW_PINNED). A pinned column keeps its place in the
 * user's order and its user width — it is unhidden, not relocated.
 */
export function visibleColumns(
  prefs: ColumnPrefs,
  pinned: readonly ColumnKey[] = [],
): ColumnDef[] {
  const hidden = new Set(prefs.hidden);
  for (const k of pinned) hidden.delete(k);
  const out = prefs.order
    .filter((k) => !hidden.has(k))
    .map((k) => BY_KEY.get(k))
    .filter((c): c is ColumnDef => !!c);
  // Normalized prefs always list every known column, so this only fires for a
  // hand-built prefs object. Still worth it: a pin that silently renders
  // nothing is worse than one appended out of position.
  for (const k of pinned) {
    if (out.some((c) => c.key === k)) continue;
    const def = BY_KEY.get(k);
    if (def) out.push(def);
  }
  return out;
}

export function isVisible(prefs: ColumnPrefs, key: ColumnKey): boolean {
  return !prefs.hidden.includes(key);
}

export function toggleColumn(prefs: ColumnPrefs, key: ColumnKey): ColumnPrefs {
  const hidden = new Set(prefs.hidden);
  if (hidden.has(key)) hidden.delete(key);
  else hidden.add(key);
  // Same last-column guard as normalize: keep at least one column standing.
  if (hidden.size >= prefs.order.length) hidden.delete(key);
  return { ...prefs, hidden: [...hidden] };
}

/** Move `key` so that it lands immediately before `beforeKey` (end when null). */
export function moveColumn(
  prefs: ColumnPrefs,
  key: ColumnKey,
  beforeKey: ColumnKey | null,
): ColumnPrefs {
  if (key === beforeKey) return prefs;
  const order = prefs.order.filter((k) => k !== key);
  const at = beforeKey ? order.indexOf(beforeKey) : -1;
  if (at < 0) order.push(key);
  else order.splice(at, 0, key);
  return { ...prefs, order };
}

export function setWidth(prefs: ColumnPrefs, key: ColumnKey, px: number): ColumnPrefs {
  return { ...prefs, widths: { ...prefs.widths, [key]: clampWidth(key, px) } };
}

// ─── cookie persistence ──────────────────────────────────────────────────
//
// A cookie rather than localStorage on purpose: app/page.tsx reads it during
// SSR and hands the prefs to CatalogTable as its initial state, so the first
// paint is already the user's layout. With localStorage the server would render
// the default columns and the client would reshuffle them on hydrate — a
// visible flash on every navigation.

export const COLUMN_COOKIE = "arti_cols";
// A year. Column layout is a long-lived preference; nothing here is sensitive.
export const COLUMN_COOKIE_MAX_AGE = 60 * 60 * 24 * 365;

/**
 * Serialize to a cookie value.
 *
 * `order` is written in full even when it matches the default, because it is
 * also the record of which columns existed when the cookie was written — see
 * normalizeColumnPrefs, which needs that to tell "the user unhid this" from
 * "this column didn't exist yet". ~300 bytes; a fully-default layout deletes
 * the cookie instead of writing one (writeColumnCookie).
 */
export function serializeColumnPrefs(p: ColumnPrefs): string {
  const body: Record<string, unknown> = { order: p.order, hidden: p.hidden };
  if (Object.keys(p.widths).length > 0) body.widths = p.widths;
  return encodeURIComponent(JSON.stringify(body));
}

export function parseColumnPrefs(cookieValue: string | null | undefined): ColumnPrefs {
  if (!cookieValue) return defaultColumnPrefs();
  try {
    // The value may or may not still be percent-encoded depending on who read
    // it (Next's cookies() decodes; document.cookie does not).
    const text = cookieValue.includes("%") ? decodeURIComponent(cookieValue) : cookieValue;
    return normalizeColumnPrefs(JSON.parse(text));
  } catch {
    // Corrupt or hand-edited cookie — fall back rather than break the catalog.
    return defaultColumnPrefs();
  }
}

/**
 * Persist prefs to the browser's cookie jar (client-side only).
 *
 * A default layout *removes* the cookie rather than storing the defaults: that
 * way "reset columns" leaves no trace, and a user who never customizes never
 * pays cookie bytes on every request to every arti route.
 *
 * SameSite=Lax + no Secure flag so it also works on http://localhost during
 * development; there is nothing sensitive in a column layout.
 */
export function writeColumnCookie(p: ColumnPrefs): void {
  if (typeof document === "undefined") return;
  if (isDefaultPrefs(p)) {
    document.cookie = `${COLUMN_COOKIE}=; path=/; max-age=0; samesite=lax`;
    return;
  }
  document.cookie = `${COLUMN_COOKIE}=${serializeColumnPrefs(p)}; path=/; max-age=${COLUMN_COOKIE_MAX_AGE}; samesite=lax`;
}

/** Pull COLUMN_COOKIE out of a raw `document.cookie` / `Cookie:` header string. */
export function columnCookieFrom(cookieHeader: string | null | undefined): string | null {
  if (!cookieHeader) return null;
  for (const part of cookieHeader.split(";")) {
    const eq = part.indexOf("=");
    if (eq < 0) continue;
    if (part.slice(0, eq).trim() === COLUMN_COOKIE) return part.slice(eq + 1).trim();
  }
  return null;
}
