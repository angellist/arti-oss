import { headers } from "next/headers";
import { SearchRailProvider } from "@/lib/rail-context";
import { getBrowseAggregates } from "@/lib/arti";
import type { BrowseFacet } from "@/lib/types";
import BrowseTable from "@/components/BrowseTable";

export const dynamic = "force-dynamic";

type SP = Record<string, string | string[] | undefined>;

// 500 is the max pgstore.BrowseAggregates honors (above that it silently
// resets to 50) — fetching the max up front means the instant search box
// covers effectively every value for the facets in practice (dozens, not
// thousands), not just the current page.
const PAGE_SIZE = 500;
const FACETS: BrowseFacet[] = ["type", "label", "scope", "content_type", "owner"];

function pick(sp: SP, k: string): string | undefined {
  const v = sp[k];
  if (Array.isArray(v)) return v[0];
  return v;
}

export default async function Browse({
  searchParams,
}: {
  searchParams: Promise<SP>;
}) {
  const sp = await searchParams;
  const h = await headers();
  const cookie = h.get("cookie") ?? undefined;

  const facetParam = pick(sp, "facet") ?? "";
  const facet: BrowseFacet = (FACETS as string[]).includes(facetParam)
    ? (facetParam as BrowseFacet)
    : "label";
  const sort = pick(sp, "sort") === "name" ? "name" : "count";
  const dir = pick(sp, "dir") === "asc" ? "asc" : "desc";
  const pageNum = Math.max(1, parseInt(pick(sp, "page") ?? "1", 10) || 1);

  let total = 0;
  let values: Awaited<ReturnType<typeof getBrowseAggregates>>["values"] = [];
  let err = "";
  try {
    const data = await getBrowseAggregates(
      { facet, sort, dir, page: pageNum, page_size: PAGE_SIZE },
      cookie,
    );
    total = data.total;
    values = data.values;
  } catch (e) {
    err = e instanceof Error ? e.message : String(e);
  }

  return (
    <SearchRailProvider>
      <main className="min-h-screen pb-12">
        <div className="border-b border-neutral-200 bg-neutral-50 px-6 py-3">
          <h1 className="text-base font-semibold text-neutral-900">browse</h1>
          <p className="mt-0.5 text-[12px] text-neutral-500">
            every distinct type, label, scope, and content type, with counts.
            click a value to filter the catalog to it.
          </p>
        </div>
        {err ? (
          <div className="border-b border-rose-200 bg-rose-50 px-6 py-2 text-xs text-rose-700">
            error: {err}
          </div>
        ) : null}
        <BrowseTable
          facet={facet}
          values={values}
          total={total}
          page={pageNum}
          sort={sort}
          dir={dir}
        />
      </main>
    </SearchRailProvider>
  );
}
