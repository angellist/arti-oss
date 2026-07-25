import { headers } from "next/headers";
import CatalogTable from "@/components/CatalogTable";
import { SearchRailProvider } from "@/lib/rail-context";
import { getMe, listArtifacts, type SortField, type SortDir } from "@/lib/arti";
import { catalogView } from "@/lib/catalog";
import { columnCookieFrom, parseColumnPrefs } from "@/lib/columns";
import type { Me } from "@/lib/types";

export const dynamic = "force-dynamic";

type SP = Record<string, string | string[] | undefined>;

const PAGE_SIZE = 50;

function pick(sp: SP, k: string): string | undefined {
  const v = sp[k];
  if (Array.isArray(v)) return v[0];
  return v;
}

export default async function Home({
  searchParams,
}: {
  searchParams: Promise<SP>;
}) {
  const sp = await searchParams;
  const h = await headers();
  const cookie = h.get("cookie") ?? undefined;

  const pageNum = Math.max(1, parseInt(pick(sp, "page") ?? "1", 10) || 1);
  const offset = (pageNum - 1) * PAGE_SIZE;

  // The catalog view state (shared with CatalogTable via catalogView so the
  // two can't drift). Drilling into a single slug shows its full version
  // history; the control-bar toggles apply the same behavior catalog-wide:
  //   - allVersions  → don't collapse to latest-per-slug
  //   - showArchived → include archived versions in the candidate set
  // A slug drill-in likewise pulls in archived versions so they can be seen
  // (grayed) and unarchived inline.
  const view = catalogView((k) => pick(sp, k));

  const params = {
    type: pick(sp, "type"),
    q: pick(sp, "q"),
    // The dedicated `slug` param is a deterministic drill-in (see catalogView):
    // it filters to that slug server-side and, via latestPerSlugFor, shows the
    // full version history. A `slug:` token typed in `q` is a search instead.
    slug: pick(sp, "slug"),
    order_by: pick(sp, "order_by") as SortField | undefined,
    order_dir: pick(sp, "order_dir") as SortDir | undefined,
    limit: PAGE_SIZE,
    offset,
    all_versions: view.allVersions || undefined,
    include_archived: view.drilledIntoSlug || view.showArchived || undefined,
  };

  let total = 0;
  let rows: Awaited<ReturnType<typeof listArtifacts>>["artifacts"] = [];
  let err = "";
  try {
    const data = await listArtifacts(params, cookie);
    total = data.total;
    rows = data.artifacts;
  } catch (e) {
    err = e instanceof Error ? e.message : String(e);
  }

  // me gates the archive/unarchive controls (creator-or-admin). Null if the
  // identity lookup fails — controls simply won't render.
  let me: Me | null = null;
  try {
    me = await getMe(cookie);
  } catch {
    me = null;
  }

  return (
    <SearchRailProvider>
      {/* The catalog is an app-shell page, not a document page: it owns the
          viewport height and scrolls *inside* itself. That is what lets the
          table header stay pinned — a `position: sticky` thead sticks to its
          nearest scrollport, and the table already needs a horizontal
          scroll wrapper (many optional columns), so the wrapper is the
          scrollport whether we like it or not. Making that wrapper the
          vertical scroller too is what gives it something to stick to; with
          document-level scrolling the header would just ride away. The
          pagination bar comes along for free — it now stays visible instead
          of living 50 rows down. h-dvh (not vh) so mobile browser chrome
          doesn't hide the bottom bar; -3rem for SideNav's fixed mobile top
          bar, which the layout's pt-12 accounts for. */}
      <main className="flex h-[calc(100dvh-3rem)] flex-col overflow-hidden md:h-dvh">
        {err ? (
          <div className="shrink-0 border-b border-rose-200 bg-rose-50 px-6 py-2 text-xs text-rose-700">
            error: {err}
          </div>
        ) : null}
        <CatalogTable
          rows={rows}
          total={total}
          page={pageNum}
          me={me}
          // Column layout is read from the request's own cookie so the server
          // renders the user's columns directly. Handing it to the client as
          // initial state instead (localStorage, or a useEffect) would paint
          // the default layout first and visibly reshuffle on hydration.
          initialColumns={parseColumnPrefs(columnCookieFrom(cookie))}
        />
      </main>
    </SearchRailProvider>
  );
}
