import { headers } from "next/headers";
import { SearchRailProvider } from "@/lib/rail-context";
import {
  getMe,
  listArtifacts,
  type SortField,
  type SortDir,
} from "@/lib/arti";
import ArchivedTable from "@/components/ArchivedTable";

export const dynamic = "force-dynamic";

type SP = Record<string, string | string[] | undefined>;

const PAGE_SIZE = 50;

function pick(sp: SP, k: string): string | undefined {
  const v = sp[k];
  if (Array.isArray(v)) return v[0];
  return v;
}

export default async function Archived({
  searchParams,
}: {
  searchParams: Promise<SP>;
}) {
  const sp = await searchParams;
  const h = await headers();
  const cookie = h.get("cookie") ?? undefined;

  const pageNum = Math.max(1, parseInt(pick(sp, "page") ?? "1", 10) || 1);
  const offset = (pageNum - 1) * PAGE_SIZE;

  // Fetch /api/me in parallel with the list so the table can render
  // unarchive (creator-or-admin) and delete (admin-only) buttons with
  // correct permission gating in a single round-trip.
  const [data, me] = await Promise.all([
    listArtifacts(
      {
        type: pick(sp, "type"),
        q: pick(sp, "q"),
        order_by: pick(sp, "order_by") as SortField | undefined,
        order_dir: pick(sp, "order_dir") as SortDir | undefined,
        limit: PAGE_SIZE,
        offset,
        archived: "only",
      },
      cookie,
    ).catch((e: Error) => ({ artifacts: [], total: 0, err: e.message })),
    getMe(cookie).catch(() => ({ email: "", name: "", is_admin: false })),
  ]);

  const err = "err" in data ? data.err : "";

  return (
    <SearchRailProvider>
      <main className="min-h-screen pb-12">
        <div className="border-b border-neutral-200 bg-neutral-50 px-6 py-3">
          <h1 className="text-base font-semibold text-neutral-900">archived</h1>
          <p className="mt-0.5 text-[12px] text-neutral-500">
            soft-archived artifacts. you can unarchive your own; admins
            can unarchive or permanently delete anyone&apos;s.
          </p>
        </div>
        {err ? (
          <div className="border-b border-rose-200 bg-rose-50 px-6 py-2 text-xs text-rose-700">
            error: {err}
          </div>
        ) : null}
        <ArchivedTable
          rows={data.artifacts}
          total={data.total}
          page={pageNum}
          me={me}
        />
      </main>
    </SearchRailProvider>
  );
}
