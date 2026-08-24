import { headers } from "next/headers";
import { notFound } from "next/navigation";
import ArtifactViewer from "@/components/ArtifactViewer";
import FullPageView from "@/components/FullPageView";
import { ArtiError, fetchContent, fetchPackageFile, getBySlug, getMe, listPackageFiles } from "@/lib/arti";
import { fullPageKind, isFullPageView, resolvePackageEntry, textScaleFromParam } from "@/lib/viewer";
import { PackageRailProvider, SearchRailProvider } from "@/lib/rail-context";

export const dynamic = "force-dynamic";

type SP = Record<string, string | string[] | undefined>;

export async function generateMetadata({
  params,
}: {
  params: Promise<{ slug: string }>;
}) {
  try {
    const { slug } = await params;
    const cookie = (await headers()).get("cookie") ?? undefined;
    const info = await getBySlug(slug, undefined, cookie);
    return { title: info.title };
  } catch {
    return {};
  }
}

export default async function BySlug({
  params,
  searchParams,
}: {
  params: Promise<{ slug: string }>;
  searchParams: Promise<SP>;
}) {
  const { slug } = await params;
  const sp = await searchParams;
  const cookie = (await headers()).get("cookie") ?? undefined;

  let info;
  let me;
  try {
    [info, me] = await Promise.all([
      getBySlug(slug, undefined, cookie),
      getMe(cookie).catch(() => ({ email: "", name: "", is_admin: false })),
    ]);
  } catch (e) {
    // No artifact under this slug (or no access) → render the standard 404
    // page instead of letting the ArtiError bubble up as a 500 wall.
    if (e instanceof ArtiError && (e.status === 404 || e.status === 403)) notFound();
    throw e;
  }

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
    const textScale = textScaleFromParam(sp);
    return <FullPageView body={body} contentType={contentType} filePath={filePath} entries={entries} entryPoint={entryPoint} title={info.title} artifactID={info.artifact_id} me={me} textScale={textScale} commentsEnabled={info.comments_enabled !== false} />;
  }

  if (info.artifact_type === "PACKAGE" || info.artifact_type === "APP") {
    const m = await listPackageFiles(info.artifact_id, cookie);
    // Seed the initially-selected file from ?file= (resolved to the canonical
    // manifest entry, mirroring the server), so a shared /s/<slug>?file=<path>
    // deep-links to that file; fall back to the entry point.
    const initialSelected =
      resolvePackageEntry(m.entries, (typeof sp.file === "string" ? sp.file : undefined) ?? m.entry_point ?? undefined)?.path ??
      m.entry_point ??
      null;
    return (
      <PackageRailProvider manifest={m} initialSelected={initialSelected}>
        <main className="min-h-screen bg-white pb-12">
          <ArtifactViewer info={info} manifest={m} me={me} />
        </main>
      </PackageRailProvider>
    );
  }
  const { body } = await fetchContent(info.artifact_id, cookie);
  return (
    <SearchRailProvider>
      <main className="min-h-screen bg-white pb-12">
        <ArtifactViewer info={info} body={body} fullHref={`?v=full`} me={me} />
      </main>
    </SearchRailProvider>
  );
}
