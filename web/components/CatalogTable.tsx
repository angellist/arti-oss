"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import type { ArtifactInfo, Me } from "@/lib/types";
import { archiveArtifact, hasPerm, sameEmail, unarchiveArtifact, type SortDir, type SortField } from "@/lib/arti";
import { ALL_VERSIONS_KEY, SEARCH_OPEN_KEY, SHOW_ARCHIVED_KEY, catalogView } from "@/lib/catalog";
import { relativeTime } from "@/lib/time";
import CreatorName from "@/components/CreatorName";
import { SearchIcon } from "@/components/SearchIcon";

type ColKey = SortField;

type Col = {
  key: ColKey;
  label: string;
  sortable: boolean;
  className?: string;
};

// Column order — title is the wide content column, type sits as a
// metadata pill just before created. Widths are explicit so
// table-fixed lays them out predictably even with sparse data.
const COLS: Col[] = [
  { key: "title",   label: "title",   sortable: true,  className: "min-w-[280px] w-[36%] pl-6 pr-2" },
  { key: "slug",    label: "slug",    sortable: true,  className: "w-44 px-2" },
  { key: "version", label: "v",       sortable: true,  className: "w-16 px-2" },
  { key: "creator", label: "creator", sortable: true,  className: "w-36 px-2" },
  // scope + labels share one column; the combined width equals the two
  // former columns (w-52 + w-40 = 368px). Purple scope chips and neutral
  // label chips stay color-coded. Sorting still keys off scope.
  { key: "scope",   label: "scope · labels", sortable: true,  className: "w-[368px] px-2" },
  { key: "type",    label: "type",    sortable: true,  className: "w-28 px-2" },
  { key: "created", label: "created", sortable: true,  className: "w-36 px-2 pr-6" },
];

const PAGE_SIZE = 50;

export default function CatalogTable({
  rows,
  total,
  page,
  me,
}: {
  rows: ArtifactInfo[];
  total: number;
  page: number; // 1-indexed
  me?: Me | null;
}) {
  const router = useRouter();
  const sp = useSearchParams();
  const orderBy = (sp.get("order_by") as SortField | null) ?? "created";
  const orderDir = (sp.get("order_dir") as SortDir | null) ?? "desc";

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

  // Keyboard: `/` opens (and focuses) search from anywhere on the catalog;
  // typing any printable key while the bar is already open jumps focus into the
  // box (type-to-search). Ignored while another field is focused, under a
  // modifier, or in the single-slug drill-in view (which has no search bar).
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
  }, [searchOpen, drilledIntoSlug, router]);

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

  const arrow = (key: ColKey) => {
    if (orderBy !== key) return "";
    return orderDir === "asc" ? " ↑" : " ↓";
  };

  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const from = total === 0 ? 0 : (page - 1) * PAGE_SIZE + 1;
  const to = Math.min(total, from + rows.length - 1);

  return (
    <div>
      {actionErr ? (
        <div className="border-b border-rose-200 bg-rose-50 px-6 py-2 text-xs text-rose-700">
          error: {actionErr}
        </div>
      ) : null}
      {/* Reveal-on-demand search bar above the table — opened via SEARCH in the
          rail (or shown automatically when a query/toggle is already active).
          Row 1 is the full-width search box; row 2 the catalog-wide view
          toggles + a close control. Hidden when drilled into a single slug
          (that view already shows full history with its own inline controls). */}
      {searchOpen ? (
        <div className="space-y-2.5 border-b border-neutral-200 bg-white px-6 py-3">
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
      <div className="overflow-x-auto">
        {/* <mark>s — server-side highlight fragments and the client-side
            slug/label ones alike — are restyled here from the browser's neon
            yellow to a soft amber. (Layout note: with width:auto, table-fixed
            never actually engages and this is an auto-layout table; column
            sizing is protected from snippet blowup in HighlightSnippets.) */}
        <table className="min-w-full table-fixed bg-white text-[13px] leading-snug [&_mark]:rounded-sm [&_mark]:bg-amber-100 [&_mark]:text-inherit">
          <thead className="border-b border-neutral-200 bg-neutral-50 text-left text-[12px] text-neutral-700">
            <tr>
              {COLS.map((c) => {
                const headerLabel = c.key === "version" ? versionColLabel : c.label;
                return (
                  <th key={c.key} className={`py-2 font-bold ${c.className ?? ""}`}>
                    {c.sortable ? (
                      <button
                        onClick={() => router.push(sortHref(c.key as SortField))}
                        className="cursor-pointer select-none transition hover:text-neutral-900"
                      >
                        {headerLabel}
                        {arrow(c.key)}
                      </button>
                    ) : (
                      headerLabel
                    )}
                  </th>
                );
              })}
              {drilledIntoSlug ? (
                <th className="w-40 px-2 pr-6 py-2 font-bold text-right">actions</th>
              ) : null}
            </tr>
          </thead>
          <tbody>
            {rows.map((a) => {
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
                  <td className={`pl-6 pr-2 py-1.5 ${dim}`}>
                    <Link
                      href={localURL}
                      className="group text-[15px] font-medium text-blue-700"
                    >
                      {a.highlights?.title?.[0] ? (
                        // OpenSearch already returns a highlighted title
                        // fragment; render it so matched terms are marked in
                        // the title, not just the body snippet.
                        <span
                          className="group-hover:underline"
                          dangerouslySetInnerHTML={{
                            __html: sanitizeHighlight(a.highlights.title[0]),
                          }}
                        />
                      ) : (
                        <span className="group-hover:underline">{a.title}</span>
                      )}
                      {a.artifact_type === "PACKAGE" ? (
                        <span
                          className="ml-1.5 text-base"
                          title="multi-file PACKAGE artifact"
                          aria-label="package"
                        >
                          📦
                        </span>
                      ) : null}
                    </Link>
                    {appHref ? (
                      // Plain <a> (not Link): /app/{ident} is served by the Go
                      // edge, not a Next route, so it needs a full navigation.
                      <a
                        href={appHref}
                        className="ml-2 inline-flex items-center rounded-md bg-blue-600 px-1.5 py-0.5 align-middle text-[11px] font-medium text-white shadow-sm transition hover:bg-blue-700"
                        title="open the running app, full-page"
                        aria-label="open the running app"
                      >
                        ↗
                      </a>
                    ) : null}
                    <HighlightSnippets highlights={a.highlights} />
                  </td>
                  <td className={`px-2 py-1.5 text-neutral-700 ${dim}`}>
                    {a.named_slug ? (
                      <Link
                        href={slugFilterHref(a.named_slug)}
                        className="text-blue-700 hover:underline"
                        title="filter to every version of this slug"
                      >
                        <Highlighted text={a.named_slug} terms={terms} />
                      </Link>
                    ) : (
                      <span className="text-neutral-300">—</span>
                    )}
                  </td>
                  <td className={`px-2 py-1.5 text-neutral-700 ${dim}`}>
                    {a.version != null ? (
                      `v${a.version}`
                    ) : (
                      <span className="text-neutral-300">—</span>
                    )}
                    {archived ? (
                      <span
                        className="ml-1 rounded bg-neutral-200 px-1 py-0.5 text-[10px] uppercase tracking-wide text-neutral-500"
                        title={a.deleted_at ? `archived ${a.deleted_at}` : "archived"}
                      >
                        archived
                      </span>
                    ) : null}
                  </td>
                  <td className={`px-2 py-1.5 text-neutral-700 ${dim}`}>
                    <CreatorName email={a.creator} />
                  </td>
                  <td className={`px-2 py-1.5 text-neutral-500 ${dim}`}>
                    {a.scopes.length > 0 || a.labels.length > 0 ? (
                      <span className="inline-flex flex-wrap gap-1">
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
                      <span className="text-neutral-300">—</span>
                    )}
                  </td>
                  <td className={`px-2 py-1.5 ${dim}`}>
                    <span
                      className="inline-block rounded bg-neutral-100 px-1.5 py-0.5 text-[11px] text-neutral-600"
                      title={a.content_type}
                    >
                      {a.artifact_type}
                    </span>
                    <div className="mt-0.5 text-[11px] text-neutral-400">{a.content_type}</div>
                  </td>
                  <td
                    className={
                      "whitespace-nowrap px-2 py-1.5 text-neutral-500 " +
                      (drilledIntoSlug ? "" : "pr-6") +
                      " " +
                      dim
                    }
                    title={a.created_at}
                  >
                    {relativeTime(a.created_at)}
                  </td>
                  {drilledIntoSlug ? (
                    <td className="whitespace-nowrap px-2 pr-6 py-1.5 text-right">
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
                </tr>
              );
            })}
            {rows.length === 0 ? (
              <tr>
                <td
                  colSpan={COLS.length + (drilledIntoSlug ? 1 : 0)}
                  className="px-6 py-12 text-center text-neutral-400"
                >
                  no artifacts
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>

      <nav className="flex items-center justify-between border-t border-neutral-200 bg-white px-6 py-2 text-[11px] text-neutral-500">
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
