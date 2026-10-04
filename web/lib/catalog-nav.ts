"use client";

import { useEffect, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { SEARCH_OPEN_KEY } from "./catalog";
import { useSetSearchBarOpen } from "./rail-context";

// The last catalog filter (type + q), persisted so a doc view, whose URL carries
// no filter, can still show and act on it.
export const FILTER_TYPE_KEY = "arti.filter.type";
export const FILTER_Q_KEY = "arti.filter.q";

export type CatalogFilter = { type: string; q: string };

export function readStickyFilter(): CatalogFilter {
  try {
    return {
      type: window.localStorage.getItem(FILTER_TYPE_KEY) ?? "",
      q: window.localStorage.getItem(FILTER_Q_KEY) ?? "",
    };
  } catch {
    return { type: "", q: "" };
  }
}

// useStickyFilter returns the active catalog filter: the URL's on the catalog,
// the persisted one elsewhere. Every rail variant must call it, or a filter
// chosen while that variant is mounted is never persisted.
export function useStickyFilter(): CatalogFilter {
  const sp = useSearchParams();
  const onCatalog = usePathname() === "/";
  const urlType = sp.get("type") ?? "";
  const urlQ = sp.get("q") ?? "";
  const [sticky, setSticky] = useState(readStickyFilter);

  useEffect(() => {
    if (!onCatalog) return;
    setSticky({ type: urlType, q: urlQ });
    try {
      window.localStorage.setItem(FILTER_TYPE_KEY, urlType);
      window.localStorage.setItem(FILTER_Q_KEY, urlQ);
    } catch {
      // localStorage may be unavailable; the in-memory value still works.
    }
  }, [onCatalog, urlType, urlQ]);

  return onCatalog ? { type: urlType, q: urlQ } : sticky;
}

// catalogURL builds the catalog URL for a filter change. On the catalog the
// current params (sort, etc.) carry over; elsewhere it rebuilds from `current`.
// A search or filter action always leaves a slug drill-in and resets paging.
export function catalogURL(
  catalogParams: URLSearchParams | null,
  current: CatalogFilter,
  params: Record<string, string | null>,
): string {
  const next = catalogParams ? new URLSearchParams(catalogParams.toString()) : new URLSearchParams();
  if (!catalogParams) {
    if (current.q) next.set("q", current.q);
    if (current.type) next.set("type", current.type);
  }
  for (const [k, v] of Object.entries(params)) {
    if (v === null || v === "") next.delete(k);
    else next.set(k, v);
  }
  next.delete("page");
  next.delete("slug");
  return `/?${next.toString()}`;
}

// useOpenSearch reveals the catalog's search bar. On the plain catalog it flips
// the shell flag and syncs `find=1` shallowly, because a router.push would wait
// on a full force-dynamic re-render. A slug drill-in hides the bar, and other
// pages have none, so those navigate to the catalog keeping the sticky filter.
export function useOpenSearch(current?: CatalogFilter): () => void {
  const router = useRouter();
  const sp = useSearchParams();
  const pathname = usePathname();
  const setBarOpen = useSetSearchBarOpen();
  return () => {
    const onCatalog = pathname === "/";
    if (onCatalog && !sp.get("slug")) {
      setBarOpen(true);
      const params = new URLSearchParams(window.location.search);
      params.set(SEARCH_OPEN_KEY, "1");
      window.history.replaceState(null, "", `${window.location.pathname}?${params.toString()}`);
      return;
    }
    router.push(catalogURL(onCatalog ? sp : null, current ?? readStickyFilter(), { [SEARCH_OPEN_KEY]: "1" }));
  };
}
