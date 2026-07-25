"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import type { BrowseFacet, BrowseValueCount } from "@/lib/types";
import { browseRowHref, filterAndRankValues } from "@/lib/browse";

const PAGE_SIZE = 500;

const FACETS: { value: BrowseFacet; label: string }[] = [
  { value: "type", label: "Type" },
  { value: "label", label: "Label" },
  { value: "scope", label: "Scope" },
  { value: "content_type", label: "Content Type" },
  { value: "owner", label: "Owner" },
];

type SortKey = "count" | "name";
type SortDir = "asc" | "desc";

export default function BrowseTable({
  facet,
  values,
  total,
  page,
  sort,
  dir,
}: {
  facet: BrowseFacet;
  values: BrowseValueCount[];
  total: number;
  page: number; // 1-indexed
  sort: SortKey;
  dir: SortDir;
}) {
  const router = useRouter();
  const sp = useSearchParams();
  const [query, setQuery] = useState("");
  // Switching facets (Type/Label/Scope/Content Type) keeps this component
  // mounted, so a leftover query would silently filter the new facet's
  // values against the old search term — reset it whenever the facet changes.
  useEffect(() => {
    setQuery("");
  }, [facet]);
  const displayValues = useMemo(
    () => filterAndRankValues(values, query),
    [values, query],
  );
  const searching = query.trim() !== "";

  function withParams(set: (qs: URLSearchParams) => void): string {
    const next = new URLSearchParams(sp.toString());
    set(next);
    return `/browse?${next.toString()}`;
  }

  function facetHref(f: BrowseFacet): string {
    return withParams((next) => {
      next.set("facet", f);
      next.delete("page");
    });
  }

  function sortHref(key: SortKey): string {
    return withParams((next) => {
      let d: SortDir = key === "name" ? "asc" : "desc";
      if (sort === key) d = dir === "asc" ? "desc" : "asc";
      next.set("sort", key);
      next.set("dir", d);
      next.delete("page");
    });
  }

  function pageHref(p: number): string {
    return withParams((next) => {
      if (p <= 1) next.delete("page");
      else next.set("page", String(p));
    });
  }

  const arrow = (key: SortKey) => {
    if (sort !== key) return "";
    return dir === "asc" ? " ↑" : " ↓";
  };

  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const from = total === 0 ? 0 : (page - 1) * PAGE_SIZE + 1;
  const to = Math.min(total, from + values.length - 1);

  return (
    <div>
      <div className="flex flex-wrap gap-1.5 border-b border-neutral-200 bg-white px-6 py-3">
        {FACETS.map((f) => {
          const active = facet === f.value;
          return (
            <Link
              key={f.value}
              href={facetHref(f.value)}
              className={
                "rounded-full px-2.5 py-0.5 text-[12px] transition " +
                (active
                  ? "bg-blue-600 text-white"
                  : "border border-neutral-200 text-neutral-600 hover:bg-neutral-50")
              }
            >
              {f.label}
            </Link>
          );
        })}
      </div>

      {facet === "owner" ? (
        <div className="border-b border-amber-100 bg-amber-50 px-6 py-2 text-[12px] text-amber-800">
          <span className="font-medium">Note:</span> counts reflect every
          artifact the owner has created, including private ones. Clicking
          through shows only the artifacts you have access to — private
          artifacts and internal system items (e.g.{" "}
          <code className="rounded bg-amber-100 px-0.5">scope:app:couch</code>)
          are hidden from the catalog view.
        </div>
      ) : null}

      <div className="border-b border-neutral-200 bg-white px-6 py-2">
        <input
          type="text"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={`search ${facet} values…`}
          className="w-full max-w-xs rounded-md border border-neutral-200 bg-neutral-50 px-3 py-1.5 text-[12px] text-neutral-900 placeholder:text-neutral-400 focus:border-blue-400 focus:bg-white focus:outline-none focus:ring-1 focus:ring-blue-200"
        />
      </div>

      <div className="max-w-xl overflow-x-auto">
        <table className="w-full bg-white text-[13px] leading-snug">
          <thead className="border-b border-neutral-200 bg-neutral-50 text-left text-[12px] text-neutral-700">
            <tr>
              <th className="py-2 pl-6 pr-2 font-bold">
                <button
                  type="button"
                  onClick={() => router.push(sortHref("name"))}
                  className="cursor-pointer select-none transition hover:text-neutral-900"
                >
                  value{arrow("name")}
                </button>
              </th>
              <th className="w-20 py-2 pr-6 text-right font-bold">
                <button
                  type="button"
                  onClick={() => router.push(sortHref("count"))}
                  className="cursor-pointer select-none transition hover:text-neutral-900"
                >
                  count{arrow("count")}
                </button>
              </th>
            </tr>
          </thead>
          <tbody>
            {displayValues.map((v) => (
              <tr
                key={v.value}
                className="border-b border-neutral-100 transition hover:bg-neutral-50/70"
              >
                <td className="truncate py-1.5 pl-6 pr-2">
                  <Link
                    href={browseRowHref(facet, v.value)}
                    className="text-blue-700 hover:underline"
                    title={`filter the catalog to this ${facet}`}
                  >
                    {v.value}
                  </Link>
                </td>
                <td className="py-1.5 pr-6 text-right text-neutral-500">{v.count}</td>
              </tr>
            ))}
            {displayValues.length === 0 ? (
              <tr>
                <td colSpan={2} className="px-6 py-12 text-center text-neutral-400">
                  {searching ? "no matches" : "no values"}
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>

      <nav className="flex max-w-xl items-center justify-between border-t border-neutral-200 bg-white px-6 py-2 text-[11px] text-neutral-500">
        <span>
          {searching ? (
            <>
              <span className="font-medium text-neutral-700">{displayValues.length}</span> match
              {displayValues.length === 1 ? "" : "es"} for &ldquo;{query.trim()}&rdquo;
              {values.length < total ? (
                <>
                  {" "}
                  (searching the loaded{" "}
                  <span className="font-medium text-neutral-700">{values.length}</span> of{" "}
                  <span className="font-medium text-neutral-700">{total}</span> values)
                </>
              ) : null}
            </>
          ) : total === 0 ? (
            "no values"
          ) : (
            <>
              showing <span className="font-medium text-neutral-700">{from}</span>–
              <span className="font-medium text-neutral-700">{to}</span> of{" "}
              <span className="font-medium text-neutral-700">{total}</span>
            </>
          )}
        </span>
        {!searching && totalPages > 1 ? (
          <div className="flex items-center gap-1">
            <PageLink disabled={page <= 1} href={pageHref(page - 1)} label="← prev" />
            <span className="px-2 text-neutral-500">
              page {page} of {totalPages}
            </span>
            <PageLink disabled={page >= totalPages} href={pageHref(page + 1)} label="next →" />
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
