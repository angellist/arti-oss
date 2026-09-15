"use client";

import { useState } from "react";
import Link from "next/link";
import type { ArtifactInfo, Me } from "@/lib/types";
import { APP_SORTS, selectApps, type AppSort } from "@/lib/apps";
import { LABEL_CHIP } from "@/lib/chips";
import { appHref, artifactHref } from "@/lib/hrefs";
import { relativeTime } from "@/lib/time";
import CreatorName from "@/components/CreatorName";
import { SearchIcon } from "@/components/SearchIcon";

const LABELS_SHOWN = 3;

function EyeIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className="h-3 w-3"
    >
      <path d="M1.5 12S5.5 5 12 5s10.5 7 10.5 7-4 7-10.5 7S1.5 12 1.5 12Z" />
      <circle cx="12" cy="12" r="3" />
    </svg>
  );
}

function SortIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      aria-hidden="true"
      className="h-3.5 w-3.5 shrink-0 text-neutral-400"
    >
      <line x1="4" y1="6" x2="19" y2="6" />
      <line x1="4" y1="12" x2="14" y2="12" />
      <line x1="4" y1="18" x2="9" y2="18" />
    </svg>
  );
}

function AppCard({ a }: { a: ArtifactInfo }) {
  const run = appHref(a);
  const views = a.view_count_30d ?? 0;
  const extra = a.labels.length - LABELS_SHOWN;
  return (
    <div className="relative flex flex-col gap-2 rounded-lg border border-neutral-200 bg-white p-3.5 transition hover:border-neutral-300 hover:shadow-sm">
      {/* The whole card runs the app. /app/{ident} is served by the Go edge,
          so this is a plain <a> — next/link would try to resolve a route that
          does not exist on the Next side. The info link below sits above it. */}
      {run ? (
        <a
          href={run}
          aria-label={`open ${a.title}`}
          className="absolute inset-0 rounded-lg focus-visible:outline focus-visible:-outline-offset-2 focus-visible:outline-blue-400"
        />
      ) : null}

      <div className="flex items-start gap-2">
        <h3 className="line-clamp-2 min-w-0 flex-1 text-[15px] font-semibold leading-tight text-neutral-900">
          {a.title}
        </h3>
        <span
          className="mt-0.5 inline-flex shrink-0 items-center gap-1 text-[11px] text-neutral-400"
          title={`${views} views in the last 30 days`}
        >
          <EyeIcon />
          {views}
        </span>
        {a.version != null ? (
          <span className="mt-px shrink-0 rounded bg-neutral-100 px-1.5 py-0.5 font-mono text-[11px] text-neutral-700">
            v{a.version}
          </span>
        ) : null}
      </div>

      {a.named_slug ? (
        <p className="truncate font-mono text-[11.5px] text-neutral-500">{a.named_slug}</p>
      ) : (
        <p className="truncate text-[11.5px] italic text-neutral-400">
          no slug · {a.artifact_id.slice(0, 8)}
        </p>
      )}

      {a.labels.length > 0 ? (
        <div className="flex flex-wrap gap-1">
          {a.labels.slice(0, LABELS_SHOWN).map((l) => (
            <span key={l} className={LABEL_CHIP}>
              {l}
            </span>
          ))}
          {extra > 0 ? <span className="text-[11px] text-neutral-400">+{extra}</span> : null}
        </div>
      ) : null}

      <div className="mt-auto flex flex-wrap items-center gap-x-1.5 gap-y-1 pt-1 text-[11px] text-neutral-500">
        <span title={a.modified_at}>{relativeTime(a.modified_at)}</span>
        <span className="text-neutral-300">·</span>
        <span className="min-w-0 truncate">
          <CreatorName email={a.creator} />
        </span>
        <Link
          href={artifactHref(a)}
          className="relative ml-auto whitespace-nowrap text-neutral-400 transition hover:text-neutral-700"
          title="artifact page — versions, labels, access, comments"
        >
          ⓘ info
        </Link>
      </div>
    </div>
  );
}

export default function AppsGrid({
  rows,
  total,
  me,
}: {
  rows: ArtifactInfo[];
  total: number;
  me: Me | null;
}) {
  const [query, setQuery] = useState("");
  const [mine, setMine] = useState(false);
  const [sort, setSort] = useState<AppSort>("recent");

  const shown = selectApps(rows, { query, owner: mine ? (me?.email ?? null) : null, sort });

  return (
    <>
      <div className="flex flex-wrap items-center gap-2.5 border-b border-neutral-200 px-6 py-3">
        <div className="relative min-w-[180px] max-w-[420px] flex-1">
          <SearchIcon className="pointer-events-none absolute left-2.5 top-2.5 h-3.5 w-3.5 text-neutral-400" />
          <input
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="filter apps — name, slug, label, owner"
            aria-label="filter apps"
            className="w-full rounded-md border border-neutral-200 bg-neutral-50 py-1.5 pl-8 pr-3 text-[13px] text-neutral-900 placeholder:text-neutral-400 focus:border-blue-400 focus:bg-white focus:outline-none focus:ring-1 focus:ring-blue-200"
          />
        </div>

        {/* Hidden until we know who the caller is — the toggle has nothing to
            compare against otherwise. */}
        {me?.email ? (
          <button
            type="button"
            aria-pressed={mine}
            onClick={() => setMine((v) => !v)}
            className={
              "flex items-center gap-1.5 rounded-full px-3 py-1 text-[12px] transition " +
              (mine
                ? "bg-blue-100 font-medium text-blue-800"
                : "border border-neutral-200 text-neutral-600 hover:bg-neutral-50")
            }
          >
            👤 Owned by me
          </button>
        ) : null}

        <label
          className="flex items-center gap-1.5 rounded-full border border-neutral-200 py-1 pl-3 pr-1.5 text-[12px] text-neutral-600"
          title="sort"
        >
          <SortIcon />
          <select
            value={sort}
            onChange={(e) => setSort(e.target.value as AppSort)}
            aria-label="sort apps"
            className="cursor-pointer border-0 bg-transparent text-[12px] text-neutral-600 focus:outline-none"
          >
            {APP_SORTS.map((s) => (
              <option key={s.value} value={s.value}>
                {s.label}
              </option>
            ))}
          </select>
        </label>

        <span className="ml-auto text-[12px] text-neutral-400">
          {shown.length} of {rows.length} apps
          {total > rows.length ? ` (of ${total} — filter and sort cover these ${rows.length})` : ""}
        </span>
      </div>

      {shown.length === 0 ? (
        <p className="px-6 py-10 text-[13px] text-neutral-500">no apps match that filter.</p>
      ) : (
        <div className="grid grid-cols-1 gap-3.5 px-6 py-4 pb-10 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
          {shown.map((a) => (
            <AppCard key={a.artifact_id} a={a} />
          ))}
        </div>
      )}

    </>
  );
}
