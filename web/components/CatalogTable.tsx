"use client";

import { useCallback, useEffect, useRef, useState, type FormEvent, type PointerEvent as ReactPointerEvent } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import type { ArtifactInfo, Me } from "@/lib/types";
import { archiveArtifact, hasPerm, sameEmail, unarchiveArtifact, type SortDir, type SortField } from "@/lib/arti";
import { ALL_VERSIONS_KEY, SEARCH_OPEN_KEY, SHOW_ARCHIVED_KEY, catalogView, rowSetKey } from "@/lib/catalog";
import {
  clampWidth,
  defaultColumnPrefs,
  moveColumn,
  setWidth,
  toggleColumn,
  visibleColumns,
  widthOf,
  writeColumnCookie,
  type ColumnDef,
  type ColumnKey,
  type ColumnPrefs,
} from "@/lib/columns";
import { relativeTime } from "@/lib/time";
import { formatBytes } from "@/lib/format";
import CreatorName from "@/components/CreatorName";
import ColumnMenu from "@/components/ColumnMenu";
import { SearchIcon } from "@/components/SearchIcon";

const PAGE_SIZE = 50;

// Width of the trailing cell that holds the ⋮ column-menu button. Fixed and
// tiny — it exists so the menu has a visible, keyboard-reachable affordance,
// since a right-click menu is otherwise invisible.
const MENU_COL_WIDTH = 30;

// Shared by every header cell. Sticky per-cell rather than on <thead>: cell
// stickiness is the universally supported form (Safari only grew `position:
// sticky` on thead/tr later), and the cell is what needs the opaque
// background anyway — the thead's own background scrolls out from under a
// pinned cell and rows would show through.
const HEADER_CELL = "sticky top-0 z-20 bg-neutral-50 ";
const HEADER_RULE = "inset 0 -1px 0 0 rgb(229 229 229)"; // neutral-200
const DROP_MARK = "rgb(37 99 235)"; // blue-600

// Exported for its own test: getting this wrong is silent — the header keeps
// working and just loses its bottom edge while a column is being dragged.
export function headerShadow(dropBefore: boolean, dropAfter: boolean): string {
  const layers = [HEADER_RULE];
  if (dropBefore) layers.push(`inset 2px 0 0 0 ${DROP_MARK}`);
  if (dropAfter) layers.push(`inset -2px 0 0 0 ${DROP_MARK}`);
  return layers.join(", ");
}

export default function CatalogTable({
  rows,
  total,
  page,
  me,
  initialColumns,
}: {
  rows: ArtifactInfo[];
  total: number;
  page: number; // 1-indexed
  me?: Me | null;
  // Parsed from the arti_cols cookie during SSR so the first paint already has
  // the user's layout — see lib/columns.
  initialColumns?: ColumnPrefs;
}) {
  const router = useRouter();
  const sp = useSearchParams();
  const orderBy = (sp.get("order_by") as SortField | null) ?? "created";
  const orderDir = (sp.get("order_dir") as SortDir | null) ?? "desc";

  // ─── column layout state ───────────────────────────────────────────────
  const [prefs, setPrefs] = useState<ColumnPrefs>(() => initialColumns ?? defaultColumnPrefs());
  // Every mutation goes through here so state and cookie can't drift.
  const applyPrefs = useCallback((next: ColumnPrefs) => {
    setPrefs(next);
    writeColumnCookie(next);
  }, []);
  const [menuAt, setMenuAt] = useState<{ x: number; y: number } | null>(null);
  const [dragKey, setDragKey] = useState<ColumnKey | null>(null);
  const [dropAt, setDropAt] = useState<{ key: ColumnKey; side: "before" | "after" } | null>(null);
  const colEls = useRef(new Map<ColumnKey, HTMLTableColElement | null>());
  // Live drag state for a resize. A ref, not state: pointermove fires at screen
  // rate and re-rendering 50 rows per frame makes the drag lag behind the
  // cursor. The <col> element's width is mutated directly during the drag and
  // committed to state (and the cookie) once, on pointerup.
  const resizeRef = useRef<{
    key: ColumnKey;
    startX: number;
    startW: number;
    latest: number;
    // Did this gesture actually change the width? A bare click on the grip
    // must not be committed — see endResize.
    moved: boolean;
    // Table width minus this column, so the table can be widened in step with
    // the column during the drag (see moveResize).
    otherW: number;
  } | null>(null);
  const tableRef = useRef<HTMLTableElement>(null);
  const [resizingKey, setResizingKey] = useState<ColumnKey | null>(null);
  // Same reasoning for the reorder drag: the live pointer state is a ref, and
  // only the drop indicator (which changes rarely) is state.
  const dragRef = useRef<{
    key: ColumnKey;
    startX: number;
    moved: boolean;
    target: { key: ColumnKey; side: "before" | "after" } | null;
    fromButton: boolean;
  } | null>(null);
  const headerRowRef = useRef<HTMLTableRowElement>(null);
  const suppressClickRef = useRef(false);
  const scrollRef = useRef<HTMLDivElement>(null);

  const cols = visibleColumns(prefs);
  // Latest prefs for handlers that outlive their render — the reorder drag
  // parks its finish handler on the window until pointerup.
  const prefsRef = useRef(prefs);
  useEffect(() => {
    prefsRef.current = prefs;
  });

  // Inline archive/unarchive lives only in the drilled-into-slug view (the
  // version-history list). busy holds the artifact_id being mutated so its
  // buttons disable; actionErr surfaces any failure.
  const [busy, setBusy] = useState<string | null>(null);
  const [actionErr, setActionErr] = useState<string>("");

  // Archive/unarchive is creator-or-admin, mirroring the API's enforcement.
  const canActOn = (row: ArtifactInfo) =>
    !!me && (hasPerm(me, "MANAGE_ARTIFACTS") || sameEmail(row.creator, me.email));

  const doArchive = async (row: ArtifactInfo) => {
    if (!canActOn(row)) return;
    const archived = !!row.deleted_at;
    const ok = window.confirm(
      archived
        ? `Unarchive "${row.title}" (v${row.version})? It will reappear in the catalog.`
        : `Archive "${row.title}" (v${row.version})? It will disappear from the catalog but can be restored.`,
    );
    if (!ok) return;
    setBusy(row.artifact_id);
    setActionErr("");
    try {
      if (archived) {
        await unarchiveArtifact(row.artifact_id);
      } else {
        await archiveArtifact(row.artifact_id);
      }
      router.refresh();
    } catch (e) {
      setActionErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  // Catalog view state, parsed once via the shared helper so page.tsx and
  // this component interpret the URL identically. drilledIntoSlug (a single
  // slug in focus) drives the inline archive controls and hides the control
  // bar; showAllVersions also covers the "latest version only" toggle being
  // off, so the version column header reverts from "latest" to a plain "v".
  const view = catalogView((k) => sp.get(k));
  const drilledIntoSlug = view.drilledIntoSlug;
  const showAllVersions = drilledIntoSlug || view.allVersions;
  const versionColLabel = showAllVersions ? "v" : "latest";

  const q = sp.get("q") ?? "";

  // The search + toggles live in a reveal-on-demand bar (opened via SEARCH in
  // the rail), not always-on: the default listing stays clean. It shows when
  // explicitly opened (find=1) OR when a query/toggle is already active (so the
  // active state is always visible and editable). Never in the drill-in view —
  // that already shows full version history with its own inline controls.
  const searchOpen =
    !drilledIntoSlug && (view.searchOpen || !!q || view.allVersions || view.showArchived);
  const searchInputRef = useRef<HTMLInputElement>(null);
  // Focus the box when the bar opens empty (the explicit "I clicked SEARCH"
  // case). When it opens because a query is already active, leave focus alone.
  useEffect(() => {
    if (searchOpen && !q) searchInputRef.current?.focus();
  }, [searchOpen, q]);

  // The rows scroll inside their own box now, so a navigation that swaps the
  // rows out (next page, a re-sort, a new query, a toggled filter) has to
  // rewind it by hand — the browser only restores *document* scroll, which no
  // longer moves. Without this, page 2 opens halfway down the list.
  //
  // The dependency is rowSetKey over the URL rather than a hand-listed set of
  // values: naming them here means every new filter has to remember to come
  // back and be added (the `allv` / `arch` / `type` toggles were missed
  // exactly that way), while the key lives beside the params it covers.
  const rowsKey = rowSetKey((k) => sp.get(k));
  useEffect(() => {
    // Assigning scrollTop rather than scrollTo(): no smooth-scroll animation
    // wanted on a row swap, and jsdom implements the property but not the
    // method, so the tests can observe it.
    if (scrollRef.current) scrollRef.current.scrollTop = 0;
  }, [rowsKey]);

  // Keyboard: `/` opens (and focuses) search from anywhere on the catalog;
  // typing any printable key while the bar is already open jumps focus into the
  // box (type-to-search). Ignored while another field is focused, under a
  // modifier, or in the single-slug drill-in view (which has no search bar).
  const menuOpen = !!menuAt;
  useEffect(() => {
    const isEditable = (el: Element | null) =>
      el instanceof HTMLElement &&
      (el.tagName === "INPUT" ||
        el.tagName === "TEXTAREA" ||
        el.tagName === "SELECT" ||
        el.isContentEditable);
    function onKeyDown(e: KeyboardEvent) {
      if (drilledIntoSlug || e.metaKey || e.ctrlKey || e.altKey) return;
      // A modal/dialog (e.g. the upload modal) owns the keyboard while open —
      // don't let catalog shortcuts fire behind it, focusing/revealing the bar
      // under the overlay. The modal is portaled and only in the DOM while open,
      // so its presence is a reliable "a modal is open" test even when focus
      // sits on a non-editable element inside it.
      if (document.querySelector('[aria-modal="true"]')) return;
      // Same rule for the column menu: it owns Escape and arrow keys while up.
      if (menuOpen) return;
      if (isEditable(document.activeElement)) return; // already typing somewhere
      if (e.key === "/") {
        e.preventDefault();
        if (searchOpen) {
          searchInputRef.current?.focus();
        } else {
          // Reveal the bar (reads the live URL so no other filter/sort is lost);
          // it mounts on navigation and autofocuses itself (empty q).
          const params = new URLSearchParams(window.location.search);
          params.set(SEARCH_OPEN_KEY, "1");
          params.delete("page");
          router.push(`/?${params.toString()}`);
        }
        return;
      }
      // type-to-search: only when the bar is visible. Focus and let the
      // character land in the box (no preventDefault). Space is excluded so it
      // keeps scrolling the list.
      if (searchOpen && e.key.length === 1 && e.key !== " ") {
        searchInputRef.current?.focus();
      }
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [searchOpen, drilledIntoSlug, router, menuOpen]);

  // Free-text query terms, for client-side highlighting of slug and
  // scope/label chip hits. Server-side highlights only cover title/body
  // (slug_text is copy_to-only so OpenSearch can't fragment it, and the
  // Postgres fallback returns no highlights at all), so matches in these
  // columns are marked here instead. Empty on the plain listing (no q).
  const terms = searchTerms(q);

  function withParams(set: (qs: URLSearchParams) => void): string {
    const next = new URLSearchParams(sp.toString());
    set(next);
    return `/?${next.toString()}`;
  }

  function sortHref(key: SortField): string {
    return withParams((next) => {
      let dir: SortDir = "asc";
      if (orderBy === key) {
        dir = orderDir === "asc" ? "desc" : "asc";
      } else if (key === "created" || key === "version") {
        dir = "desc";
      }
      next.set("order_by", key);
      next.set("order_dir", dir);
      next.delete("page");
    });
  }

  function slugFilterHref(slug: string): string {
    // Clicking a slug is a deterministic drill-in, not a search: the
    // dedicated `slug` param shows that slug's full version history with
    // inline archive controls (latestPerSlugFor keys off this param). Typing
    // `slug:foo` in the search box is instead a search that keeps the
    // version/archived toggles — see catalogView.
    const next = new URLSearchParams();
    next.set("slug", slug);
    next.set("order_by", "version");
    next.set("order_dir", "desc");
    return `/?${next.toString()}`;
  }

  function pageHref(p: number): string {
    return withParams((next) => {
      if (p <= 1) next.delete("page");
      else next.set("page", String(p));
    });
  }

  // Control-bar toggle → URL. `on` sets the key; otherwise it's dropped so the
  // URL stays clean at the default. Paging resets since the row set changes.
  // We also pin the bar open (find=1): without it, unchecking the toggle that
  // was the bar's only reason to show (e.g. `arch` with no query) would make
  // the whole bar vanish out from under the click.
  function toggleHref(key: string, on: boolean): string {
    return withParams((next) => {
      if (on) next.set(key, "1");
      else next.delete(key);
      next.set(SEARCH_OPEN_KEY, "1");
      next.delete("page");
    });
  }

  // Enter in the search box → set (or clear) q and keep the bar open. find=1 is
  // pinned so an empty submit doesn't collapse the bar mid-search.
  function submitSearch(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const val = String(new FormData(e.currentTarget).get("q") ?? "").trim();
    router.push(
      withParams((next) => {
        if (val) next.set("q", val);
        else next.delete("q");
        next.set(SEARCH_OPEN_KEY, "1");
        next.delete("page");
      }),
    );
  }

  // ✕ closes the bar: clear the query + both toggles + the open flag so nothing
  // keeps it visible, returning to the clean default listing. A `type` chip
  // filter and the current sort are left intact (they aren't shown in the bar).
  function closeSearch() {
    router.push(
      withParams((next) => {
        next.delete("q");
        next.delete(ALL_VERSIONS_KEY);
        next.delete(SHOW_ARCHIVED_KEY);
        next.delete(SEARCH_OPEN_KEY);
        next.delete("page");
      }),
    );
  }

  const arrow = (key: SortField | null) => {
    if (!key || orderBy !== key) return "";
    return orderDir === "asc" ? " ↑" : " ↓";
  };

  // ─── resize ────────────────────────────────────────────────────────────
  function beginResize(e: ReactPointerEvent<HTMLSpanElement>, key: ColumnKey) {
    // stopPropagation keeps the mousedown from also starting the header's
    // HTML5 reorder drag; resizeRef doubles as the guard in onDragStart for the
    // case where the browser has already begun one.
    e.preventDefault();
    e.stopPropagation();
    const th = e.currentTarget.closest("th");
    // Measure what is actually on screen rather than the stored preference:
    // the flexible title column has no stored width, and a fixed-layout table
    // can stretch columns to fill, so the two can differ.
    const startW = th ? th.getBoundingClientRect().width : widthOf(prefs, key);
    resizeRef.current = {
      key,
      startX: e.clientX,
      startW,
      latest: clampWidth(key, startW),
      moved: false,
      otherW: (tableRef.current?.getBoundingClientRect().width ?? totalWidth) - startW,
    };
    setResizingKey(key);
    e.currentTarget.setPointerCapture(e.pointerId);
  }

  function moveResize(e: ReactPointerEvent<HTMLSpanElement>) {
    const r = resizeRef.current;
    if (!r) return;
    const w = clampWidth(r.key, r.startW + (e.clientX - r.startX));
    if (w !== r.latest) r.moved = true;
    r.latest = w;
    const el = colEls.current.get(r.key);
    if (el) el.style.width = `${w}px`;
    // The table's own width has to grow with the column, or a fixed-layout
    // table just steals the pixels back from its neighbours and the dragged
    // edge stops tracking the cursor.
    if (tableRef.current) tableRef.current.style.width = `${r.otherW + w}px`;
  }

  function endResize(e: ReactPointerEvent<HTMLSpanElement>) {
    const r = resizeRef.current;
    resizeRef.current = null;
    setResizingKey(null);
    if (e.currentTarget.hasPointerCapture(e.pointerId)) {
      e.currentTarget.releasePointerCapture(e.pointerId);
    }
    // Only a gesture that actually moved the edge is a width preference. A
    // bare click on the grip would otherwise persist the width the column
    // happens to be rendering at — and that is usually NOT its stored width:
    // the table is min-w-full, so columns stretch to fill a wide viewport.
    // Committing that stretched pixel value pins the column, at one width, on
    // a click the user never meant as a resize.
    if (r?.moved) applyPrefs(setWidth(prefs, r.key, r.latest));
  }

  // Double-clicking the grip returns one column to its designed width.
  function resetWidth(key: ColumnKey) {
    const widths = { ...prefs.widths };
    delete widths[key];
    applyPrefs({ ...prefs, widths });
  }

  // ─── reorder ───────────────────────────────────────────────────────────
  //
  // Pointer events rather than HTML5 drag-and-drop. DnD would be less code,
  // but it does nothing on touch, its drag image of a table cell is a
  // half-rendered ghost, and a `draggable` <th> swallows the text selection and
  // click behaviour of the sort button inside it. Pointer capture gives one
  // code path for mouse, pen and touch — and one that a test can drive.
  function beginHeaderDrag(e: ReactPointerEvent<HTMLTableCellElement>, key: ColumnKey) {
    if (e.button !== 0 || resizeRef.current) return; // left button only; grip wins
    // Clear any leftover suppression here rather than in the click handler: a
    // drag that ends over nothing never produces a click on this header, and a
    // stale flag would silently swallow the NEXT header click instead.
    suppressClickRef.current = false;
    dragRef.current = {
      key,
      startX: e.clientX,
      moved: false,
      target: null,
      // A click can only need suppressing if the gesture began on the sort
      // button — see finish.
      fromButton: !!(e.target instanceof Element && e.target.closest("button")),
    };

    // Listeners on the window, not the cell. A fast flick can put the very
    // first pointermove several cells away — with handlers bound to the source
    // <th>, that event never arrives and the drag silently dies. (Pointer
    // capture would also fix delivery, but capturing on pointerdown retargets
    // the subsequent click away from the sort button inside the header.)
    const onMove = (ev: PointerEvent) => {
      const d = dragRef.current;
      if (!d) return;
      if (!d.moved) {
        // A few px of slop so a click that jitters still sorts.
        if (Math.abs(ev.clientX - d.startX) < 5) return;
        d.moved = true;
        setDragKey(d.key);
      }
      const next = dropTargetAt(ev.clientX, d.key);
      // Re-render only when the indicator actually moves: pointermove fires per
      // frame and each one would otherwise re-render every row in the table.
      const same =
        (next === null && d.target === null) ||
        (next !== null &&
          d.target !== null &&
          next.key === d.target.key &&
          next.side === d.target.side);
      if (!same) {
        d.target = next;
        setDropAt(next);
      }
    };
    // Teardown is shared by pointerup and pointercancel. Cancel is not
    // optional bookkeeping: the browser fires it whenever it takes the gesture
    // over — a touch that turns into a pan of this (now two-axis) scroll box is
    // the common case — and without it the window listeners stay attached, the
    // dragged column stays dimmed, and the *next* pointerup anywhere on the
    // page finishes a drag the user abandoned.
    const finish = (commit: boolean, ev?: PointerEvent) => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("pointercancel", onCancel);
      const d = dragRef.current;
      dragRef.current = null;
      if (commit && d?.moved) {
        // The click that follows this pointerup would otherwise re-sort the
        // column the user just finished dragging — but a native click only
        // follows when pointerdown AND pointerup both landed on that column's
        // sort button. Armed in any other case the flag goes stale and eats an
        // unrelated later activation instead (a keyboard Enter on a sort
        // button never passes through pointerdown, so nothing would clear it).
        const upEl = ev?.target instanceof Element ? ev.target : null;
        suppressClickRef.current =
          d.fromButton &&
          !!upEl?.closest("button") &&
          upEl.closest("th[data-col]")?.getAttribute("data-col") === d.key;
        if (d.target) {
          // Read the prefs through the ref, not the closure: this handler
          // lives on the window for the whole gesture, and anything applied
          // mid-drag (a column toggled through the menu) would otherwise be
          // overwritten by the commit.
          const p = prefsRef.current;
          // "after X" means "before whatever follows X" — moveColumn works in
          // terms of the successor, so a drop past the last column appends.
          // The successor comes from the full order, not the visible one:
          // inserting before a hidden column reads the same on screen, and the
          // drop target itself may have been hidden mid-drag.
          const order = p.order.filter((k) => k !== d.key);
          const at = order.indexOf(d.target.key);
          const before = d.target.side === "before" ? d.target.key : (order[at + 1] ?? null);
          applyPrefs(moveColumn(p, d.key, before));
        }
      }
      setDragKey(null);
      setDropAt(null);
    };
    // A cancelled gesture produces no click, so it must NOT arm the
    // click-suppression flag — that would eat the next real sort click.
    const onUp = (ev: PointerEvent) => finish(true, ev);
    const onCancel = () => finish(false);
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onCancel);
  }

  // Which header the pointer is over, and which side of its midpoint — read
  // from live geometry so it works no matter what element the event targeted.
  function dropTargetAt(clientX: number, source: ColumnKey) {
    const row = headerRowRef.current;
    if (!row) return null;
    for (const th of Array.from(row.querySelectorAll<HTMLElement>("th[data-col]"))) {
      const r = th.getBoundingClientRect();
      if (clientX >= r.left && clientX <= r.right) {
        const key = th.dataset.col as ColumnKey;
        if (key === source) return null;
        return { key, side: clientX > r.left + r.width / 2 ? ("after" as const) : ("before" as const) };
      }
    }
    return null;
  }

  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const from = total === 0 ? 0 : (page - 1) * PAGE_SIZE + 1;
  const to = Math.min(total, from + rows.length - 1);
  // Body columns = the visible ones, plus the drill-in actions cell, plus the
  // trailing ⋮ cell.
  const bodyColSpan = cols.length + (drilledIntoSlug ? 1 : 0) + 1;
  const ACTIONS_COL_WIDTH = 160;
  const totalWidth =
    cols.reduce((sum, c) => sum + widthOf(prefs, c.key), 0) +
    (drilledIntoSlug ? ACTIONS_COL_WIDTH : 0) +
    MENU_COL_WIDTH;

  return (
    // A column that fills the page shell (app/page.tsx gives it a definite
    // height): fixed chrome above and below, one scrolling region between.
    // min-h-0 is load-bearing — a flex child's default min-height:auto would
    // let the table push the column taller than the viewport and nothing
    // would ever scroll inside.
    <div className="flex min-h-0 flex-1 flex-col">
      {actionErr ? (
        <div className="shrink-0 border-b border-rose-200 bg-rose-50 px-6 py-2 text-xs text-rose-700">
          error: {actionErr}
        </div>
      ) : null}
      {/* Reveal-on-demand search bar above the table — opened via SEARCH in the
          rail (or shown automatically when a query/toggle is already active).
          Row 1 is the full-width search box; row 2 the catalog-wide view
          toggles + a close control. Hidden when drilled into a single slug
          (that view already shows full history with its own inline controls). */}
      {searchOpen ? (
        <div className="shrink-0 space-y-2.5 border-b border-neutral-200 bg-white px-6 py-3">
          <form onSubmit={submitSearch} className="flex items-center gap-2">
            <SearchIcon className="h-4 w-4 shrink-0 text-neutral-400" />
            <input
              // Re-key on q so an externally-changed query (e.g. clicking a
              // label/scope chip in the rail) resets this uncontrolled box to
              // match; typing between navigations is preserved (q is unchanged).
              key={q}
              ref={searchInputRef}
              type="text"
              name="q"
              defaultValue={q}
              placeholder="search — free text, or slug:foo label:bar type:APP … then press Enter"
              className="min-w-0 flex-1 rounded-md border border-neutral-200 bg-neutral-50 px-3 py-1.5 font-sans text-[13px] text-neutral-900 placeholder:text-neutral-400 focus:border-blue-400 focus:bg-white focus:outline-none focus:ring-1 focus:ring-blue-200"
            />
            <Link
              href="/help/guides/search"
              target="_blank"
              rel="noopener"
              title="search syntax help"
              aria-label="search syntax help"
              className="shrink-0 select-none text-[13px] leading-none text-neutral-400 hover:text-neutral-600"
            >
              ?
            </Link>
          </form>
          <div className="flex flex-wrap items-center gap-4 text-[12px] text-neutral-600">
            <label className="flex cursor-pointer select-none items-center gap-1.5">
              <input
                type="checkbox"
                checked={!view.allVersions}
                onChange={(e) => router.push(toggleHref(ALL_VERSIONS_KEY, !e.target.checked))}
                className="h-3.5 w-3.5 cursor-pointer accent-blue-600"
              />
              latest version only
            </label>
            <label className="flex cursor-pointer select-none items-center gap-1.5">
              <input
                type="checkbox"
                checked={view.showArchived}
                onChange={(e) => router.push(toggleHref(SHOW_ARCHIVED_KEY, e.target.checked))}
                className="h-3.5 w-3.5 cursor-pointer accent-blue-600"
              />
              show archived
            </label>
            <button
              type="button"
              onClick={closeSearch}
              className="ml-auto flex items-center gap-1 rounded px-1.5 py-0.5 text-neutral-500 transition hover:bg-neutral-100 hover:text-neutral-700"
              title="close search and clear the query"
            >
              ✕ close
            </button>
          </div>
        </div>
      ) : null}
      {/* The one scrolling region on the page, in both axes: vertically
          because the shell above bounds its height, horizontally because the
          columns can sum past the viewport. Both live here on purpose — the
          sticky header sticks to *this* box, so the box has to be the thing
          that scrolls. Side effects worth having: the horizontal scrollbar
          sits at the bottom of the viewport instead of 50 rows down, and the
          pagination bar below stays put.

          tabIndex makes the region keyboard-scrollable (PageDown/arrows).
          Chrome ≥127 and Firefox focus scrollers automatically; Safari does
          not, so it is declared rather than assumed. */}
      <div
        ref={scrollRef}
        tabIndex={0}
        aria-label="artifact catalog"
        className="min-h-0 flex-1 overflow-auto bg-white focus:outline-none focus-visible:outline focus-visible:-outline-offset-2 focus-visible:outline-blue-300"
      >
        {/* <mark>s — server-side highlight fragments and the client-side
            slug/label ones alike — are restyled here from the browser's neon
            yellow to a soft amber.

            Layout: table-fixed + an explicit <colgroup> is what makes columns
            resizable at all — under auto layout the browser re-derives widths
            from content on every render and a dragged width would not stick.
            Every column carries a width, and the table is min-w-full: when
            the columns are narrower than the viewport the browser stretches
            them to fill it, and when they are wider the wrapper scrolls. An
            "auto" column that absorbed the slack instead would read better at
            the default width but collapse to ZERO once enough optional columns
            are switched on — which is exactly when the title matters most. */}
        <table
          ref={tableRef}
          // An explicit pixel width is what makes table-layout:fixed actually
          // engage — with width:auto the browser silently falls back to the
          // auto algorithm and sizes columns from their content, ignoring the
          // <colgroup> entirely (a trap this table has fallen into before).
          // minWidth:100% then fills the viewport when the columns are narrow,
          // and the wrapper scrolls when they are not.
          style={{ width: `${totalWidth}px`, minWidth: "100%" }}
          className={
            "min-w-full table-fixed bg-white text-[13px] leading-snug [&_mark]:rounded-sm [&_mark]:bg-amber-100 [&_mark]:text-inherit " +
            (resizingKey ? "select-none" : "")
          }
        >
          <colgroup>
            {cols.map((c) => (
              <col
                key={c.key}
                ref={(el) => {
                  colEls.current.set(c.key, el);
                }}
                style={{ width: `${widthOf(prefs, c.key)}px` }}
              />
            ))}
            {drilledIntoSlug ? <col style={{ width: `${ACTIONS_COL_WIDTH}px` }} /> : null}
            <col style={{ width: `${MENU_COL_WIDTH}px` }} />
          </colgroup>
          {/* The header's bottom rule is a box-shadow, not a border: under
              `border-collapse: collapse` (Tailwind's preflight default) the
              collapsed border belongs to the table grid, not to the sticky
              cell, so it stays behind at the top of the table and the pinned
              header scrolls away bare. Shadows travel with the cell. */}
          <thead
            className="bg-neutral-50 text-left text-[12px] text-neutral-700"
            onContextMenu={(e) => {
              e.preventDefault();
              setMenuAt({ x: e.clientX, y: e.clientY });
            }}
          >
            <tr ref={headerRowRef}>
              {cols.map((c, i) => {
                const headerLabel = c.key === "version" ? versionColLabel : c.label;
                const isDropBefore = dropAt?.key === c.key && dropAt.side === "before";
                const isDropAfter = dropAt?.key === c.key && dropAt.side === "after";
                return (
                  <th
                    key={c.key}
                    data-col={c.key}
                    onPointerDown={(e) => beginHeaderDrag(e, c.key)}
                    // The drop indicator is a second shadow layered on the
                    // header rule, not a replacement for it — as two classes
                    // the later `shadow-*` would simply win and the pinned
                    // header would lose its bottom edge mid-drag.
                    style={{ boxShadow: headerShadow(isDropBefore, isDropAfter) }}
                    className={
                      HEADER_CELL +
                      "relative select-none py-2 font-bold " +
                      (i === 0 ? "pl-6 pr-2 " : "px-2 ") +
                      (c.numeric ? "text-right " : "") +
                      (dragKey ? "cursor-grabbing " : "") +
                      (dragKey === c.key ? "opacity-40 " : "")
                    }
                    title={`${c.help} — drag to reorder, right-click for column options`}
                  >
                    {c.sort ? (
                      <button
                        onClick={() => {
                          // Swallow the click that ends a reorder drag.
                          if (suppressClickRef.current) {
                            suppressClickRef.current = false;
                            return;
                          }
                          router.push(sortHref(c.sort as SortField));
                        }}
                        className="cursor-pointer select-none transition hover:text-neutral-900"
                      >
                        {headerLabel}
                        {arrow(c.sort)}
                      </button>
                    ) : (
                      <span className="select-none">{headerLabel}</span>
                    )}
                    {/* Resize grip: a 9px hit area ending at the cell border,
                        with its 1px rule on the border itself (justify-end).
                        It used to straddle the border, 4px of it overhanging
                        into the next cell — which stopped working the moment
                        the header went sticky: sticky always creates a
                        stacking context, so a child can no longer paint above
                        the *next* header cell, and those 4px started hitting
                        the neighbour (a reorder drag) instead of the grip.
                        Keeping the whole hit area inside its own cell is what
                        makes it reachable again. */}
                    <span
                      role="separator"
                      aria-orientation="vertical"
                      aria-label={`resize ${c.label} column`}
                      onPointerDown={(e) => beginResize(e, c.key)}
                      onPointerMove={moveResize}
                      onPointerUp={endResize}
                      onPointerCancel={endResize}
                      onDoubleClick={() => resetWidth(c.key)}
                      className={
                        "absolute right-0 top-0 z-10 flex h-full w-[9px] cursor-col-resize touch-none items-stretch justify-end " +
                        "after:my-1 after:w-px after:bg-neutral-200 after:transition hover:after:bg-blue-500 " +
                        (resizingKey === c.key ? "after:bg-blue-500" : "")
                      }
                    />
                  </th>
                );
              })}
              {drilledIntoSlug ? (
                <th
                  style={{ boxShadow: headerShadow(false, false) }}
                  className={HEADER_CELL + "px-2 py-2 text-right font-bold"}
                >
                  actions
                </th>
              ) : null}
              <th
                style={{ boxShadow: headerShadow(false, false) }}
                className={HEADER_CELL + "px-1 py-2 text-right"}
              >
                <button
                  type="button"
                  aria-label="choose columns"
                  title="choose columns (or right-click any header)"
                  onClick={(e) => {
                    const r = e.currentTarget.getBoundingClientRect();
                    setMenuAt({ x: r.right - 4, y: r.bottom + 4 });
                  }}
                  className="rounded px-1 text-neutral-400 transition hover:bg-neutral-200 hover:text-neutral-700"
                >
                  ⋮
                </button>
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((a) => {
              const archived = !!a.deleted_at;
              // Dim the *content* cells of an archived version, but NOT the
              // actions cell — CSS opacity flattens a whole row as one group,
              // so dimming the row would fade the unarchive button and make it
              // look disabled. Per-cell dimming keeps the button at full
              // strength. opacity (not a text color) so colored links/pills
              // gray out too.
              const dim = archived ? "opacity-50" : "";
              return (
                <tr
                  key={a.artifact_id}
                  className={
                    "border-b border-neutral-100 transition hover:bg-neutral-50/70 " +
                    (archived ? "bg-neutral-50/60" : "")
                  }
                >
                  {cols.map((c, i) => (
                    <td
                      key={c.key}
                      className={
                        "py-1.5 align-top " +
                        (i === 0 ? "pl-6 pr-2 " : "px-2 ") +
                        (c.numeric ? "text-right " : "") +
                        dim
                      }
                    >
                      <Cell
                        col={c}
                        a={a}
                        terms={terms}
                        showContentTypeSubline={!isColVisible(cols, "content_type")}
                        slugFilterHref={slugFilterHref}
                      />
                    </td>
                  ))}
                  {drilledIntoSlug ? (
                    <td className="whitespace-nowrap px-2 py-1.5 text-right">
                      <button
                        type="button"
                        disabled={!canActOn(a) || busy === a.artifact_id}
                        onClick={() => doArchive(a)}
                        className={
                          "rounded-md border px-2 py-1 text-[11px] transition disabled:cursor-not-allowed disabled:opacity-40 " +
                          (archived
                            ? "border-neutral-200 bg-white text-neutral-700 hover:bg-neutral-50"
                            : "border-rose-200 bg-white text-rose-700 hover:bg-rose-50")
                        }
                        title={
                          canActOn(a)
                            ? archived
                              ? "restore this version"
                              : "archive this version"
                            : "only the creator or an admin can archive"
                        }
                      >
                        {archived ? "↩ unarchive" : "🗑 archive"}
                      </button>
                    </td>
                  ) : null}
                  <td />
                </tr>
              );
            })}
            {rows.length === 0 ? (
              <tr>
                <td colSpan={bodyColSpan} className="px-6 py-12 text-center text-neutral-400">
                  no artifacts
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>

      {menuAt ? (
        <ColumnMenu
          x={menuAt.x}
          y={menuAt.y}
          prefs={prefs}
          onToggle={(k) => applyPrefs(toggleColumn(prefs, k))}
          onReset={() => {
            applyPrefs(defaultColumnPrefs());
            setMenuAt(null);
          }}
          onClose={() => setMenuAt(null)}
        />
      ) : null}

      <nav className="flex shrink-0 items-center justify-between border-t border-neutral-200 bg-white px-6 py-2 text-[11px] text-neutral-500">
        <span>
          {total === 0 ? (
            "no artifacts"
          ) : (
            <>
              showing <span className="font-medium text-neutral-700">{from}</span>–
              <span className="font-medium text-neutral-700">{to}</span> of{" "}
              <span className="font-medium text-neutral-700">{total}</span>
            </>
          )}
        </span>
        {totalPages > 1 ? (
          <div className="flex items-center gap-1">
            <PageLink
              disabled={page <= 1}
              href={pageHref(page - 1)}
              label="← prev"
            />
            <span className="px-2 text-neutral-500">
              page {page} of {totalPages}
            </span>
            <PageLink
              disabled={page >= totalPages}
              href={pageHref(page + 1)}
              label="next →"
            />
          </div>
        ) : null}
      </nav>
    </div>
  );
}

function isColVisible(cols: ColumnDef[], key: ColumnKey): boolean {
  return cols.some((c) => c.key === key);
}

// Cell renders one artifact field. Every column in lib/columns is handled here;
// the switch is exhaustive so adding a registry entry without a renderer is a
// type error rather than a blank column.
function Cell({
  col,
  a,
  terms,
  showContentTypeSubline,
  slugFilterHref,
}: {
  col: ColumnDef;
  a: ArtifactInfo;
  terms: string[];
  showContentTypeSubline: boolean;
  slugFilterHref: (slug: string) => string;
}) {
  const dash = <span className="text-neutral-300">—</span>;
  switch (col.key) {
    case "title": {
      const localURL = a.named_slug
        ? a.version != null
          ? `/s/${a.named_slug}/${a.version}`
          : `/s/${a.named_slug}`
        : `/a/${a.artifact_id}`;
      // APP rows get a "Visit app" launcher to the running app at
      // /app/{ident} (Go-served, version-pinned) — same target as the
      // view-mode toolbar button, so an app is one click from the list.
      const appHref =
        a.artifact_type === "APP"
          ? a.named_slug
            ? a.version != null
              ? `/app/${a.named_slug}/${a.version}`
              : `/app/${a.named_slug}`
            : `/app/${a.artifact_id}`
          : null;
      return (
        <>
          <div className="flex items-start gap-2">
            <Link href={localURL} className="group text-[15px] font-medium text-blue-700">
              {a.highlights?.title?.[0] ? (
                // OpenSearch already returns a highlighted title fragment;
                // render it so matched terms are marked in the title, not just
                // the body snippet.
                <span
                  className="group-hover:underline"
                  dangerouslySetInnerHTML={{ __html: sanitizeHighlight(a.highlights.title[0]) }}
                />
              ) : (
                <span className="group-hover:underline">{a.title}</span>
              )}
              {a.artifact_type === "PACKAGE" ? (
                <span className="ml-1.5 text-base" title="multi-file PACKAGE artifact" aria-label="package">
                  📦
                </span>
              ) : null}
            </Link>
            {appHref ? (
              // Plain <a> (not Link): /app/{ident} is served by the Go edge, not
              // a Next route, so it needs a full navigation. Pushed to the
              // column's right edge (ml-auto) and styled neutral rather than
              // blue: it's secondary to the title link, and a stack of blue
              // chips down the list shouted over the titles. shrink-0 so a
              // wrapping title never squeezes the label.
              <a
                href={appHref}
                className="ml-auto inline-flex shrink-0 items-center rounded-md border border-neutral-200 bg-white px-2 py-0.5 text-[11px] font-medium text-neutral-600 shadow-sm transition hover:bg-neutral-50 hover:text-neutral-900"
                title="open the running app, full-page"
                aria-label="open the running app"
              >
                Visit app ↗
              </a>
            ) : null}
          </div>
          <HighlightSnippets highlights={a.highlights} />
        </>
      );
    }
    case "slug":
      return a.named_slug ? (
        <Link
          href={slugFilterHref(a.named_slug)}
          className="block truncate text-blue-700 hover:underline"
          title="filter to every version of this slug"
        >
          <Highlighted text={a.named_slug} terms={terms} />
        </Link>
      ) : (
        dash
      );
    case "version":
      return (
        <span className="text-neutral-700">
          {a.version != null ? `v${a.version}` : dash}
          {a.deleted_at ? (
            <span
              className="ml-1 rounded bg-neutral-200 px-1 py-0.5 text-[10px] uppercase tracking-wide text-neutral-500"
              title={`archived ${a.deleted_at}`}
            >
              archived
            </span>
          ) : null}
        </span>
      );
    case "creator":
      return (
        <span className="block truncate text-neutral-700">
          <CreatorName email={a.creator} />
        </span>
      );
    case "scope":
      return a.scopes.length > 0 || a.labels.length > 0 ? (
        <span className="inline-flex flex-wrap gap-1 text-neutral-500">
          {a.scopes.map((sc) => (
            <Link
              key={`scope:${sc}`}
              href={`/?q=${encodeURIComponent("scope:" + sc)}`}
              className="inline-block rounded-full bg-purple-50 px-2 py-0.5 text-[11px] text-purple-800 ring-1 ring-purple-200 transition hover:bg-purple-100"
              title="filter by this scope"
            >
              <Highlighted text={sc} terms={terms} />
            </Link>
          ))}
          {a.labels.map((l) => (
            <Link
              key={`label:${l}`}
              href={`/?q=${encodeURIComponent("label:" + l)}`}
              className="inline-block rounded-full bg-neutral-100 px-2 py-0.5 text-[11px] text-neutral-700 ring-1 ring-neutral-200 transition hover:bg-neutral-200"
              title="filter by this label"
            >
              <Highlighted text={l} terms={terms} />
            </Link>
          ))}
        </span>
      ) : (
        dash
      );
    case "type":
      return (
        <>
          <span
            className="inline-block rounded bg-neutral-100 px-1.5 py-0.5 text-[11px] text-neutral-600"
            title={a.content_type}
          >
            {a.artifact_type}
          </span>
          {/* The MIME type rides along under the pill only while it has no
              column of its own — otherwise it would appear twice in one row. */}
          {showContentTypeSubline ? (
            <div className="mt-0.5 truncate text-[11px] text-neutral-400">{a.content_type}</div>
          ) : null}
        </>
      );
    case "content_type":
      return <span className="block truncate text-neutral-500">{a.content_type}</span>;
    case "description":
      return a.description ? (
        <span className="block truncate text-neutral-500" title={a.description}>
          {a.description}
        </span>
      ) : (
        dash
      );
    case "size":
      return a.size_bytes != null ? (
        <span className="whitespace-nowrap text-neutral-500">{formatBytes(a.size_bytes)}</span>
      ) : (
        dash
      );
    case "comments":
      return <CommentsCell a={a} />;
    case "access":
      return <AccessCell a={a} />;
    case "modified":
      return (
        <span className="whitespace-nowrap text-neutral-500" title={a.modified_at}>
          {relativeTime(a.modified_at)}
        </span>
      );
    case "created":
      return (
        <span className="whitespace-nowrap text-neutral-500" title={a.created_at}>
          {relativeTime(a.created_at)}
        </span>
      );
    case "archived":
      return a.deleted_at ? (
        <span className="whitespace-nowrap text-neutral-500" title={a.deleted_at}>
          {relativeTime(a.deleted_at)}
        </span>
      ) : (
        dash
      );
    case "id":
      return (
        <span className="block truncate font-mono text-[11px] text-neutral-400" title={a.artifact_id}>
          {a.artifact_id}
        </span>
      );
  }
}

// CommentsCell shows discussion volume on THIS version (comments are anchored
// to artifact_id, which is per-version). A filled dot marks unresolved threads
// — "someone is waiting on an answer here" is the signal worth scanning for.
// Undefined (not zero) means the server didn't compute counts for this
// response, which renders as an em dash rather than a misleading "0".
function CommentsCell({ a }: { a: ArtifactInfo }) {
  if (a.comment_count == null) return <span className="text-neutral-300">—</span>;
  const open = a.open_thread_count ?? 0;
  // Zero comments does NOT imply zero open threads: a thread whose comments
  // were all deleted is still open and still awaiting a reply (pgstore's
  // CommentCounts counts it on purpose — LEFT JOIN, not JOIN). Returning
  // early on comment_count === 0 hid the amber marker for exactly that row.
  if (a.comment_count === 0 && open === 0) return <span className="text-neutral-300">0</span>;
  return (
    <span
      className="whitespace-nowrap text-neutral-600"
      title={
        open > 0
          ? `${a.comment_count} comment${a.comment_count === 1 ? "" : "s"} on this version, ${open} unresolved thread${open === 1 ? "" : "s"}`
          : `${a.comment_count} comment${a.comment_count === 1 ? "" : "s"} on this version, all resolved`
      }
    >
      {open > 0 ? <span className="mr-1 text-amber-500">●</span> : null}
      {a.comment_count}
    </span>
  );
}

// AccessCell collapses allowed_access into a scannable label. `['*']` is the
// server default (everyone authenticated) and `[]` means creator-only; anything
// else is a list of email globs / group tokens shown as a count with the full
// list on hover.
function AccessCell({ a }: { a: ArtifactInfo }) {
  const acl = a.allowed_access ?? [];
  if (acl.includes("*")) {
    return (
      <span className="text-neutral-400" title="every authenticated user can read this">
        everyone
      </span>
    );
  }
  if (acl.length === 0) {
    return (
      <span className="text-amber-700" title="only the creator can read this">
        private
      </span>
    );
  }
  return (
    <span className="block truncate text-neutral-600" title={acl.join("\n")}>
      {acl[0]}
      {acl.length > 1 ? (
        <span className="text-neutral-400"> +{acl.length - 1}</span>
      ) : null}
    </span>
  );
}

function PageLink({
  href,
  label,
  disabled,
}: {
  href: string;
  label: string;
  disabled: boolean;
}) {
  if (disabled) {
    return (
      <span className="rounded border border-neutral-100 px-2.5 py-0.5 text-neutral-300">
        {label}
      </span>
    );
  }
  return (
    <Link
      href={href}
      className="rounded border border-neutral-200 px-2.5 py-0.5 text-neutral-600 transition hover:bg-neutral-50"
    >
      {label}
    </Link>
  );
}

// searchTerms extracts the free-text terms from the search-box query for
// client-side highlighting. Fielded tokens (label:incident, slug:foo)
// contribute their value part — if you filtered by a label, marking that
// label on every row shows why it matched. Negated tokens (-label:memory,
// -memory) are dropped entirely: their term is what rows must NOT contain,
// so marking it would be misleading. Lucene syntax the terms may carry
// (quotes, parens, booleans, ~fuzz, *wildcards) is stripped, longest
// terms first so the Highlighted regex prefers the longest match.
function searchTerms(q: string): string[] {
  return q
    .split(/\s+/)
    .filter((t) => !t.startsWith("-"))
    .map((t) => (t.includes(":") ? t.slice(t.indexOf(":") + 1) : t))
    .map((t) => t.replace(/^["'(]+/, "").replace(/["')~*?]+$/, ""))
    .filter((t) => t.length >= 2 && !/^(AND|OR|NOT)$/.test(t))
    .sort((a, b) => b.length - a.length);
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// Highlighted wraps case-insensitive occurrences of the query terms in
// <mark> (styled at the table level). Pure React nodes — unlike the
// server-side highlight fragments there's no HTML round-trip, so no
// sanitizer is needed.
function Highlighted({ text, terms }: { text: string; terms: string[] }) {
  if (terms.length === 0) return <>{text}</>;
  const re = new RegExp(`(${terms.map(escapeRegExp).join("|")})`, "gi");
  const parts = text.split(re);
  if (parts.length === 1) return <>{text}</>;
  return (
    <>
      {parts.map((p, i) => (i % 2 === 1 ? <mark key={i}>{p}</mark> : p))}
    </>
  );
}

function sanitizeHighlight(html: string): string {
  // Only bare <mark>/</mark> survive; everything else is stripped. The previous
  // version kept <mark ...> WITH its attributes, so artifact text containing
  // e.g. `<mark onmouseover=alert(1)>` reached dangerouslySetInnerHTML intact
  // (stored XSS). Normalize mark tags to attribute-less and drop all other tags.
  return html
    .replace(/<(?!\/?mark\b)[^>]*>/gi, "") // drop every non-<mark> tag
    .replace(/<mark\b[^>]*>/gi, "<mark>") // strip attributes from <mark ...>
    .replace(/<\/mark\b[^>]*>/gi, "</mark>") // and from </mark ...>
    .replace(/&(?!amp;|lt;|gt;|quot;|#\d+;|#x[\da-f]+;)/gi, "&amp;");
}

function HighlightSnippets({ highlights }: { highlights?: Record<string, string[]> }) {
  if (!highlights) return null;
  const snippets = [
    ...(highlights.content_text ?? []),
    ...(highlights.description ?? []),
  ].slice(0, 2);
  if (snippets.length === 0) return null;
  return (
    // contain:inline-size — intrinsic sizing treats this block as empty, so
    // long unbreakable snippet strings (minified CSS/JSON in artifact bodies)
    // contribute nothing to the auto table layout's column widths. Without it
    // the title column stretches to the snippet's full single-line width and
    // the search page stops matching the main listing's column sizing. The
    // block still fills the cell width it's given, and truncate clips inside.
    <div className="mt-0.5 space-y-0.5 text-[11px] leading-tight text-neutral-500 [contain:inline-size]">
      {snippets.map((s, i) => (
        <p
          key={i}
          className="truncate"
          dangerouslySetInnerHTML={{ __html: sanitizeHighlight(s) }}
        />
      ))}
    </div>
  );
}
