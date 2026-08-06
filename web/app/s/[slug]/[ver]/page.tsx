import { headers } from "next/headers";
import { redirect } from "next/navigation";
import ArtifactViewer from "@/components/ArtifactViewer";
import FullPageView from "@/components/FullPageView";
import StaleVersionBanner from "@/components/StaleVersionBanner";
import { ArtiError, fetchContent, fetchPackageFile, getBySlug, getMe, listPackageFiles } from "@/lib/arti";
import { fullPageKind, isFullPageView, isTextualContentType, resolvePackageEntry } from "@/lib/viewer";
import { TEXT_SCALE } from "@/components/ViewerToolbar";
import { PackageRailProvider, SearchRailProvider } from "@/lib/rail-context";

export const dynamic = "force-dynamic";

type SP = Record<string, string | string[] | undefined>;

// The version segment is a bare integer (e.g. "1"). For back-compat
// the legacy "v_<N>" form is still accepted, but new URLs use just N.
// Anything else falls back to the slug's latest via redirect.
function parseVersionSegment(seg: string): number | null {
  const trimmed = seg.startsWith("v_") ? seg.slice(2) : seg;
  const n = parseInt(trimmed, 10);
  return isNaN(n) || n <= 0 || String(n) !== trimmed ? null : n;
}

function viewQuery(sp: SP): string {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(sp)) {
    if (Array.isArray(v)) params.set(k, v[0] ?? "");
    else if (v) params.set(k, v);
  }
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

export async function generateMetadata({
  params,
}: {
  params: Promise<{ slug: string; ver: string }>;
}) {
  try {
    const { slug, ver } = await params;
    const version = parseVersionSegment(ver);
    if (version === null) return {};
    const cookie = (await headers()).get("cookie") ?? undefined;
    const info = await getBySlug(slug, version, cookie);
    return { title: info.title };
  } catch {
    return {};
  }
}

export default async function BySlugVersion({
  params,
  searchParams,
}: {
  params: Promise<{ slug: string; ver: string }>;
  searchParams: Promise<SP>;
}) {
  const { slug, ver } = await params;
  const sp = await searchParams;
  const cookie = (await headers()).get("cookie") ?? undefined;

  const version = parseVersionSegment(ver);
  if (version === null) {
    // Not a v_N segment — bounce to latest, preserving any query state.
    redirect(`/s/${slug}${viewQuery(sp)}`);
  }

  let info;
  try {
    info = await getBySlug(slug, version, cookie);
  } catch (e) {
    // Version doesn't exist (server returned 404) — fall back to latest.
    if (e instanceof ArtiError && e.status === 404) {
      redirect(`/s/${slug}${viewQuery(sp)}`);
    }
    throw e;
  }
  const latest = await getBySlug(slug, undefined, cookie).catch(() => null);
  const me = await getMe(cookie).catch(() => ({ email: "", name: "", is_admin: false }));
  const staleBanner =
    latest !== null &&
    info.version !== null &&
    latest.version !== null &&
    latest.version > info.version ? (
      <StaleVersionBanner
        currentVersion={info.version}
        latestVersion={latest.version}
        latestHref={`/s/${slug}${viewQuery(sp)}`}
        // ?v=full paints a fixed full-viewport layer, so the strip has to be
        // pinned above it; the normal viewer takes it in-flow at the top of
        // the right pane.
        floating={isFullPageView(sp)}
      />
    ) : null;

  if (isFullPageView(sp)) {
    // One full-page URL shape for every artifact type: the normal-view URL +
    // ?v=full [+ &file=<path>]. For a PACKAGE/APP resolve the file (explicit
    // ?file=, else the manifest entry point) and read its content type from the
    // manifest — so HTML/image/pdf/binary render straight from the per-file URL
    // and the body is fetched ONLY for text-ish kinds (markdown/plain/json/…),
    // never slurping the zip or double-fetching an HTML body FullPageView discards.
    let filePath = typeof sp.file === "string" ? sp.file : undefined;
    let contentType = info.content_type;
    let entries: Array<{ path: string; content_type: string }> | undefined;
    let entryPoint: string | null = null;
    if (info.artifact_type === "PACKAGE" || info.artifact_type === "APP") {
      const m = await listPackageFiles(info.artifact_id, cookie);
      const entry = resolvePackageEntry(m.entries, filePath ?? m.entry_point ?? undefined);
      filePath = entry?.path ?? filePath ?? m.entry_point ?? undefined;
      contentType = entry?.content_type || "application/octet-stream";
      entries = m.entries;
      entryPoint = m.entry_point ?? null;
    }
    const kind = fullPageKind(contentType);
    const needsBody = kind === "markdown" || kind === "text" || kind === "diagram";
    const body = needsBody
      ? filePath
        ? (await fetchPackageFile(info.artifact_id, filePath, cookie)).body
        : (await fetchContent(info.artifact_id, cookie)).body
      : "";
    const tsParam = (Array.isArray(sp.ts) ? sp.ts[0] : sp.ts) ?? "md";
    const textScale = TEXT_SCALE[tsParam as keyof typeof TEXT_SCALE] ?? 1;
    return (
      <>
        {staleBanner}
        <FullPageView body={body} contentType={contentType} filePath={filePath} entries={entries} entryPoint={entryPoint} title={info.title} artifactID={info.artifact_id} me={me} textScale={textScale} />
      </>
    );
  }

  if (info.artifact_type === "PACKAGE" || info.artifact_type === "APP") {
    const m = await listPackageFiles(info.artifact_id, cookie);
    // Seed the initially-selected file from ?file= (resolved to the canonical
    // manifest entry, mirroring the server) so a shared deep-link opens on it;
    // fall back to the entry point.
    const initialSelected =
      resolvePackageEntry(m.entries, (typeof sp.file === "string" ? sp.file : undefined) ?? m.entry_point ?? undefined)?.path ??
      m.entry_point ??
      null;
    return (
      <>
        {staleBanner}
        <PackageRailProvider manifest={m} initialSelected={initialSelected}>
          <main className="min-h-screen bg-white pb-12">
            <ArtifactViewer info={info} manifest={m} me={me} />
          </main>
        </PackageRailProvider>
      </>
    );
  }
  // Only fetch the body when the viewer renders it as text — non-text content
  // (a legacy mis-typed TEXT artifact with pdf/image bytes) renders from the
  // raw URL, so skip the wasted fetch. Same predicate the viewer renders by.
  const needsBody = isTextualContentType(info.content_type);
  const body = needsBody ? (await fetchContent(info.artifact_id, cookie)).body : "";
  return (
    <>
      {staleBanner}
      <SearchRailProvider>
        <main className="min-h-screen bg-white pb-12">
          <ArtifactViewer info={info} body={body} fullHref={`?v=full`} me={me} />
        </main>
      </SearchRailProvider>
    </>
  );
}
