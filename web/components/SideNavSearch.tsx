"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { getAggregates, getMe } from "@/lib/arti";
import { SEARCH_OPEN_KEY } from "@/lib/catalog";
import { useSetSearchBarOpen } from "@/lib/rail-context";
import type { AggregatesResponse, Me } from "@/lib/types";
import UploadButton from "./UploadButton";
import NewMenu from "./NewMenu";
import { SearchIcon } from "./SearchIcon";

const TOP_LABELS = 30;
const TOP_SCOPES = 6;
const TOP_CONTENT_TYPES = 10;

// The active catalog filter (type + q) is sticky: the rail stays mounted
// when you open a doc (/s/…, /a/…) whose URL carries no filter, so without
// this the chips would snap back to "all" the moment you enter a doc view.
// We persist the last catalog filter to localStorage (it also survives the
// rail unmounting when it switches to a package's file tree) and show it
// while off the catalog.
const FILTER_TYPE_KEY = "arti.filter.type";
const FILTER_Q_KEY = "arti.filter.q";

// Two families in one row. The uppercase values are real artifact_types; the
// lowercase ones are pseudo-types the server resolves to a content_type glob
// (see ApplyTypeFilter), so the rail can offer one-click filtering for the body
// shapes people look for without anyone typing content_type syntax.
//
// Which pseudo-types earn a chip is a question about the corpus, so it was
// measured rather than guessed (prod /aggregates, 2026-07-30): markdown 7,658 ·
// html 499 · json 155 · image 68 · diagram 2. `diagram` is here despite the
// count because it's a brand-new authoring kind that only just became creatable.
// PDF (2 artifacts) does NOT get a chip — the server still resolves the
// pseudo-type, so `type:pdf` works as a search token, it just isn't worth a slot
// in the row. `application/zip` is common (156) but those are PACKAGEs, which
// already have their own chip.
//
// For exact content-type values and their live counts, the "content types"
// section below is driven straight off the aggregate.
const TYPES = [
  { value: "", label: "all" },
  { value: "TEXT", label: "TEXT" },
  { value: "PACKAGE", label: "PACKAGE" },
  { value: "APP", label: "APP" },
  { value: "ATTACHMENT", label: "ATTACHMENT" },
  { value: "MARKDOWN", label: "markdown" },
  { value: "DIAGRAM", label: "diagram" },
  { value: "HTML", label: "html" },
  { value: "JSON", label: "json" },
  { value: "IMAGE", label: "image" },
] as const;

// A listing glyph for the BROWSE ALL rail link — same stroke weight and size
// as SearchIcon / UploadButton's arrow so the three rail links read as one set.
function BrowseIcon({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className={className}
    >
      <line x1="8" y1="6" x2="21" y2="6" />
      <line x1="8" y1="12" x2="21" y2="12" />
      <line x1="8" y1="18" x2="21" y2="18" />
      <line x1="3" y1="6" x2="3.01" y2="6" />
      <line x1="3" y1="12" x2="3.01" y2="12" />
      <line x1="3" y1="18" x2="3.01" y2="18" />
    </svg>
  );
}

// Tri is the state of a tri-state filter chip: not applied, filtered-to, or
// excluded. Scope and label chips cycle off → pos → neg → off on click.
type Tri = "off" | "pos" | "neg";

// FilterRow is one tri-state chip in the scope / label lists. A leading ✓
// (pos) or − (neg) marks the state; the column is always reserved so the
// labels stay left-aligned across states. neg is rose, matching the `-`
// negation it writes into the query.
function FilterRow({
  label,
  count,
  state,
  onClick,
}: {
  label: string;
  count: number;
  state: Tri;
  onClick: () => void;
}) {
  const tone =
    state === "pos"
      ? "bg-blue-100 font-medium text-blue-800"
      : state === "neg"
        ? "bg-rose-100 font-medium text-rose-800"
        : "text-neutral-700 hover:bg-neutral-100";
  const countTone =
    state === "pos"
      ? "text-blue-700/70"
      : state === "neg"
        ? "text-rose-700/70"
        : "text-neutral-400";
  const title =
    state === "pos"
      ? "filtering to this — click to exclude"
      : state === "neg"
        ? "excluding this — click to clear"
        : "click to filter, again to exclude";
  return (
    <li>
      <button
        type="button"
        onClick={onClick}
        title={title}
        className={
          "flex w-full items-center justify-between gap-2 rounded px-1 py-0.5 text-[12px] transition " +
          tone
        }
      >
        <span className="flex min-w-0 items-center gap-1">
          <span className="w-2 shrink-0 text-center" aria-hidden>
            {state === "pos" ? "✓" : state === "neg" ? "−" : ""}
          </span>
          <span className="truncate">{label}</span>
        </span>
        <span className={"shrink-0 text-[11px] " + countTone}>{count}</span>
      </button>
    </li>
  );
}

// CollapsibleSection — a sidebar section with a triangle toggle to collapse/expand.
// Collapsed state is persisted in localStorage under the given key.
function CollapsibleSection({
  title,
  storageKey,
  children,
}: {
  title: string;
  storageKey: string;
  children: React.ReactNode;
}) {
  const [collapsed, setCollapsed] = useState(false);

  useEffect(() => {
    try {
      if (window.localStorage.getItem(storageKey) === "1") setCollapsed(true);
    } catch {
      // ignore
    }
  }, [storageKey]);

  const toggle = () => {
    setCollapsed((c) => {
      const next = !c;
      try {
        window.localStorage.setItem(storageKey, next ? "1" : "0");
      } catch {
        // ignore
      }
      return next;
    });
  };

  return (
    <section>
      <button
        type="button"
        onClick={toggle}
        className="mb-1.5 flex w-full items-center gap-1 text-[10px] font-semibold uppercase tracking-widest text-neutral-400 hover:text-neutral-600"
        aria-expanded={!collapsed}
      >
        <svg
          width="8"
          height="8"
          viewBox="0 0 10 10"
          fill="currentColor"
          aria-hidden="true"
          className={`shrink-0 transition-transform ${collapsed ? "" : "rotate-90"}`}
        >
          <polygon points="2,1 8,5 2,9" />
        </svg>
        {title}
      </button>
      {!collapsed ? children : null}
    </section>
  );
}

export default function SideNavSearch() {
  const router = useRouter();
  const sp = useSearchParams();
  const pathname = usePathname();
  const onCatalog = pathname === "/";
  const urlType = sp.get("type") ?? "";
  const urlQ = sp.get("q") ?? "";

  // Sticky filter (see FILTER_*_KEY note). Initialized from localStorage so
  // a doc view shows the right chips immediately, with no all→filter flash.
  const [sticky, setSticky] = useState<{ type: string; q: string }>(() => {
    try {
      return {
        type: window.localStorage.getItem(FILTER_TYPE_KEY) ?? "",
        q: window.localStorage.getItem(FILTER_Q_KEY) ?? "",
      };
    } catch {
      return { type: "", q: "" };
    }
  });
  // On the catalog the URL is authoritative; mirror it into the sticky
  // store so a later doc view can read it back.
  useEffect(() => {
    if (!onCatalog) return;
    setSticky({ type: urlType, q: urlQ });
    try {
      window.localStorage.setItem(FILTER_TYPE_KEY, urlType);
      window.localStorage.setItem(FILTER_Q_KEY, urlQ);
    } catch {
      // localStorage may be unavailable; the in-memory sticky still works
      // until the rail unmounts.
    }
  }, [onCatalog, urlType, urlQ]);

  const currentType = onCatalog ? urlType : sticky.type;
  const currentQ = onCatalog ? urlQ : sticky.q;

  const [agg, setAgg] = useState<AggregatesResponse | null>(null);
  useEffect(() => {
    let live = true;
    getAggregates()
      .then((r) => {
        if (live) setAgg(r);
      })
      .catch(() => {
        if (live) setAgg({ scope_types: [], scopes: [], labels: [], content_types: [] });
      });
    return () => {
      live = false;
    };
  }, []);

  // Current user, used by the "Owned by Me" chip to toggle a
  // creator:<email> token. Null until loaded / if unauthenticated, in
  // which case the chip is hidden.
  const [me, setMe] = useState<Me | null>(null);
  useEffect(() => {
    let live = true;
    getMe()
      .then((m) => {
        if (live) setMe(m);
      })
      .catch(() => {
        if (live) setMe(null);
      });
    return () => {
      live = false;
    };
  }, []);

  const navigate = (params: Record<string, string | null>) => {
    // On the catalog, preserve any other params already in the URL (sort,
    // etc.). On a doc view the URL carries no filter, so rebuild from the
    // sticky type + q — otherwise toggling one chip would drop the others.
    // TODO(arti#56): the sticky store only carries type+q, so toggling a chip
    // from a doc view returns to the catalog with default sort (order_by/dir
    // are dropped). Persist sort too if that round-trip matters.
    const next = onCatalog ? new URLSearchParams(sp.toString()) : new URLSearchParams();
    if (!onCatalog) {
      if (currentQ) next.set("q", currentQ);
      if (currentType) next.set("type", currentType);
    }
    for (const [k, v] of Object.entries(params)) {
      if (v === null || v === "") next.delete(k);
      else next.set(k, v);
    }
    next.delete("page");
    // Any search/chip/type action leaves a slug drill-in: that lives under the
    // dedicated `slug` param (which none of these actions set), so drop it or
    // the drill-in would silently combine with the new search.
    next.delete("slug");
    router.push(`/?${next.toString()}`);
  };

  // The search box itself no longer lives in the rail — it's a reveal-on-demand
  // bar at the top of the listing (see CatalogTable).
  //
  // On the catalog listing the bar is already mounted a flag away, so flip that
  // flag directly: the UI-only `find` param never affected the row set, and
  // routing through it made the box wait on a full re-render of a
  // force-dynamic page. `page` is left in the URL because a shallow update
  // swaps no rows (see revealSearch in CatalogTable).
  //
  // A slug drill-in is excluded even though its pathname is also `/`:
  // CatalogTable hides the bar while `slug` is set, so flipping the flag there
  // would look like SEARCH doing nothing. That case needs navigate(), which
  // drops `slug` and lands on the catalog with the bar open. Off the catalog
  // entirely, likewise — there is no bar to flip yet, and navigate() preserves
  // the sticky filter so arriving via SEARCH doesn't discard the active view.
  const setBarOpen = useSetSearchBarOpen();
  const openSearch = () => {
    if (onCatalog && !sp.get("slug")) {
      setBarOpen(true);
      const params = new URLSearchParams(window.location.search);
      params.set(SEARCH_OPEN_KEY, "1");
      window.history.replaceState(null, "", `${window.location.pathname}?${params.toString()}`);
      return;
    }
    navigate({ [SEARCH_OPEN_KEY]: "1" });
  };

  const setType = (t: string) => {
    navigate({ type: currentType === t ? null : t });
  };

  // Toggle a field:value token in `q` — present? remove. absent? append.
  // Tokens are matched whole-word, so `label:memory` won't accidentally
  // collide with `label:memory-leak` or free text containing that string.
  const tokenActive = (token: string) =>
    currentQ.split(/\s+/).filter(Boolean).includes(token);

  const toggleToken = (token: string) => {
    const parts = currentQ.split(/\s+/).filter(Boolean);
    const idx = parts.indexOf(token);
    if (idx >= 0) parts.splice(idx, 1);
    else parts.push(token);
    navigate({ q: parts.length > 0 ? parts.join(" ") : null });
  };

  // Tri-state for scope/label chips. The negated form is the same token with
  // a leading `-` (the server understands `-field:value`). Whole-token match
  // mirrors tokenActive, so a chip never confuses itself with a longer token.
  const tokenState = (token: string): Tri => {
    const parts = currentQ.split(/\s+/).filter(Boolean);
    if (parts.includes(token)) return "pos";
    if (parts.includes(`-${token}`)) return "neg";
    return "off";
  };

  // cycleToken advances a chip off → pos → neg → off, rewriting q in place so
  // the token keeps its position when flipping pos → neg.
  const cycleToken = (token: string) => {
    const neg = `-${token}`;
    const parts = currentQ.split(/\s+/).filter(Boolean);
    const iPos = parts.indexOf(token);
    const iNeg = parts.indexOf(neg);
    if (iPos >= 0) parts.splice(iPos, 1, neg);
    else if (iNeg >= 0) parts.splice(iNeg, 1);
    else parts.push(token);
    navigate({ q: parts.length > 0 ? parts.join(" ") : null });
  };

  return (
    <div className="space-y-5 text-sm">
      {/* Search / Browse All / Upload / New diagram are one group of rail
          links — same type scale, leading icon, and a row rhythm tighter than
          the section gap but still breathing.
          The pl-2 puts each row's 12px icon on the same vertical centerline as
          the 28px arti logo above it (rail padding 16 + 8 + 6 = 30 = 16 + 14),
          so the rail reads as one column of glyphs the way couch's does. */}
      <nav className="space-y-2.5 pl-2">
        {/* SEARCH reveals the full-width search bar at the top of the listing
            (CatalogTable) rather than living in the rail. */}
        <button
          type="button"
          onClick={openSearch}
          className="flex w-full items-center gap-1.5 text-[10px] font-semibold uppercase tracking-widest text-neutral-400 transition hover:text-neutral-600"
        >
          <SearchIcon className="h-3 w-3 shrink-0" />
          Search
        </button>

        <Link
          href="/browse"
          className="flex w-full items-center gap-1.5 text-[10px] font-semibold uppercase tracking-widest text-neutral-400 transition hover:text-neutral-600"
        >
          <BrowseIcon className="h-3 w-3 shrink-0" />
          Browse All
        </Link>

        <UploadButton />

        <NewMenu />
      </nav>

      <section>
        <h3 className="mb-1.5 text-[10px] font-semibold uppercase tracking-widest text-neutral-400">
          types
        </h3>
        <div className="flex flex-wrap gap-1.5">
          {/* Attachments are visible to everyone now; only couch's private
              app:couch-scoped rows are hidden from non-admins (server side). */}
          {TYPES.map((t) => {
            const active = currentType === t.value;
            return (
              <button
                key={t.value || "all"}
                type="button"
                onClick={() => setType(t.value)}
                className={
                  "rounded-md px-2.5 py-0.5 text-[11px] transition " +
                  (active
                    ? "bg-neutral-200 text-neutral-800"
                    : "border border-neutral-200 text-neutral-600 hover:bg-neutral-50")
                }
              >
                {t.label}
              </button>
            );
          })}
          {/* "Owned by Me" filters to the caller's own artifacts by
              toggling a creator:<email> token in the same q box — the
              server already understands the creator: field. Hidden until
              we know who the caller is. */}
          {me?.email
            ? (() => {
                const token = `creator:${me.email}`;
                const active = tokenActive(token);
                return (
                  <button
                    type="button"
                    onClick={() => toggleToken(token)}
                    title={`creator:${me.email}`}
                    className={
                      "rounded-md px-2.5 py-0.5 text-[11px] transition " +
                      (active
                        ? "bg-neutral-200 text-neutral-800"
                        : "border border-neutral-200 text-neutral-600 hover:bg-neutral-50")
                    }
                  >
                    👤 Owned by Me
                  </button>
                );
              })()
            : null}
        </div>
      </section>

      <CollapsibleSection title="popular labels" storageKey="arti.rail.labels">
        {agg === null ? (
          <p className="text-[11px] italic text-neutral-400">loading…</p>
        ) : agg.labels.length === 0 ? (
          <p className="text-[11px] italic text-neutral-400">none yet</p>
        ) : (
          <ul className="space-y-0.5">
            {agg.labels.slice(0, TOP_LABELS).map((l) => {
              const token = `label:${l.label}`;
              return (
                <FilterRow
                  key={l.label}
                  label={l.label}
                  count={l.count}
                  state={tokenState(token)}
                  onClick={() => cycleToken(token)}
                />
              );
            })}
          </ul>
        )}
      </CollapsibleSection>

      <CollapsibleSection title="content types" storageKey="arti.rail.content_types">
        {agg === null ? (
          <p className="text-[11px] italic text-neutral-400">loading…</p>
        ) : agg.content_types.length === 0 ? (
          <p className="text-[11px] italic text-neutral-400">none yet</p>
        ) : (
          <ul className="space-y-0.5">
            {agg.content_types.slice(0, TOP_CONTENT_TYPES).map((c) => {
              const token = `content_type:${c.content_type}`;
              return (
                <FilterRow
                  key={c.content_type}
                  label={c.content_type}
                  count={c.count}
                  state={tokenState(token)}
                  onClick={() => cycleToken(token)}
                />
              );
            })}
          </ul>
        )}
      </CollapsibleSection>

      <CollapsibleSection title="scopes" storageKey="arti.rail.scopes">
        {agg === null ? (
          <p className="text-[11px] italic text-neutral-400">loading…</p>
        ) : agg.scope_types.length === 0 ? (
          <p className="text-[11px] italic text-neutral-400">none yet</p>
        ) : (
          <ul className="space-y-0.5">
            {agg.scope_types.slice(0, TOP_SCOPES).map((s) => {
              const token = `scope:${s.type}:*`;
              return (
                <FilterRow
                  key={s.type}
                  label={s.type}
                  count={s.count}
                  state={tokenState(token)}
                  onClick={() => cycleToken(token)}
                />
              );
            })}
          </ul>
        )}
      </CollapsibleSection>

      {(currentQ || currentType) && (
        <div className="pt-2">
          <Link
            href="/"
            className="text-[11px] text-neutral-500 hover:text-neutral-700"
          >
            × reset
          </Link>
        </div>
      )}
    </div>
  );
}
