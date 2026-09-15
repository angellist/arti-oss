import { headers } from "next/headers";
import { SearchRailProvider } from "@/lib/rail-context";
import { getMe, listArtifacts } from "@/lib/arti";
import type { ArtifactInfo, Me } from "@/lib/types";
import AppsGrid from "@/components/AppsGrid";

export const dynamic = "force-dynamic";

// The portal loads every app in one request so its filter and sort are instant
// and cover the whole set rather than one page of it. Past this cap the grid
// says what it is showing and both controls become page-local (see AppsGrid).
const MAX_APPS = 500;

export default async function Apps() {
  const h = await headers();
  const cookie = h.get("cookie") ?? undefined;

  let rows: ArtifactInfo[] = [];
  let total = 0;
  let err = "";
  try {
    // Latest-per-slug is the server default; without it a frequently
    // re-published app would fill the grid with its own history.
    const data = await listArtifacts({ type: "APP", limit: MAX_APPS }, cookie);
    rows = data.artifacts;
    total = data.total;
  } catch (e) {
    err = e instanceof Error ? e.message : String(e);
  }

  let me: Me | null = null;
  try {
    me = await getMe(cookie);
  } catch {
    me = null;
  }

  return (
    <SearchRailProvider>
      <main className="min-h-screen pb-12">
        <div className="border-b border-neutral-200 bg-neutral-50 px-6 py-3">
          <h1 className="text-base font-semibold text-neutral-900">apps</h1>
          {/* The copy has to stay true when the corpus outgrows one page: it
              claims completeness only while the page actually holds every app. */}
          <p className="mt-0.5 text-[12px] text-neutral-500">
            {rows.length < total
              ? `the ${rows.length} most recent of ${total} APP artifacts, latest version per slug. `
              : "every APP artifact, latest version per slug. "}
            click a card to run it; info opens the artifact page.
          </p>
        </div>
        {err ? (
          <div className="border-b border-rose-200 bg-rose-50 px-6 py-2 text-xs text-rose-700">
            error: {err}
          </div>
        ) : null}
        <AppsGrid rows={rows} total={total} me={me} />
      </main>
    </SearchRailProvider>
  );
}
