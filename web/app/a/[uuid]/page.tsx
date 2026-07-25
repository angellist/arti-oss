import { headers } from "next/headers";
import ArtifactViewer from "@/components/ArtifactViewer";
import FullPageView from "@/components/FullPageView";
import { fetchContent, fetchPackageFile, getMe, getMeta, listPackageFiles } from "@/lib/arti";
import { fullPageKind, isFullPageView, isTextualContentType, resolvePackageEntry } from "@/lib/viewer";
import { TEXT_SCALE } from "@/components/ViewerToolbar";
import { PackageRailProvider, SearchRailProvider } from "@/lib/rail-context";

export const dynamic = "force-dynamic";

type SP = Record<string, string | string[] | undefined>;

export async function generateMetadata({
  params,
}: {
  params: Promise<{ uuid: string }>;
}) {
  try {
    const { uuid } = await params;
    const cookie = (await headers()).get("cookie") ?? undefined;
    const info = await getMeta(uuid, cookie);
    return { title: info.title };
  } catch {
    return {};
  }
}

export default async function ByID({
  params,
  searchParams,
}: {
  params: Promise<{ uuid: string }>;
  searchParams: Promise<SP>;
}) {
  const { uuid } = await params;
  const sp = await searchParams;
  const cookie = (await headers()).get("cookie") ?? undefined;

  const [info, me] = await Promise.all([
    getMeta(uuid, cookie),
    getMe(cookie).catch(() => ({ email: "", name: "", is_admin: false })),
  ]);

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
      const m = await listPackageFiles(uuid, cookie);
      const entry = resolvePackageEntry(m.entries, filePath ?? m.entry_point ?? undefined);
      filePath = entry?.path ?? filePath ?? m.entry_point ?? undefined;
      contentType = entry?.content_type || "application/octet-stream";
      entries = m.entries;
      entryPoint = m.entry_point ?? null;
    }
    const kind = fullPageKind(contentType);
    const needsBody = kind === "markdown" || kind === "text";
    const body = needsBody
      ? filePath
        ? (await fetchPackageFile(uuid, filePath, cookie)).body
        : (await fetchContent(uuid, cookie)).body
      : "";
    const tsParam = (Array.isArray(sp.ts) ? sp.ts[0] : sp.ts) ?? "md";
    const textScale = TEXT_SCALE[tsParam as keyof typeof TEXT_SCALE] ?? 1;
    return <FullPageView body={body} contentType={contentType} filePath={filePath} entries={entries} entryPoint={entryPoint} title={info.title} artifactID={info.artifact_id} me={me} textScale={textScale} />;
  }

  if (info.artifact_type === "PACKAGE" || info.artifact_type === "APP") {
    const m = await listPackageFiles(uuid, cookie);
    // Seed the initially-selected file from ?file= (resolved to the canonical
    // manifest entry, mirroring the server) so a shared deep-link opens on it;
    // fall back to the entry point.
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
  // Only fetch the body when the viewer renders it as text. Non-text content
  // (pdf, image, binary) — whether an ATTACHMENT or a legacy mis-typed TEXT
  // artifact — renders from the raw URL via NonTextBody, so pulling the bytes
  // here would just waste a fetch. Same predicate the viewer renders by.
  const needsBody = isTextualContentType(info.content_type);
  const body = needsBody ? (await fetchContent(uuid, cookie)).body : "";
  return (
    <SearchRailProvider>
      <main className="min-h-screen bg-white pb-12">
        <ArtifactViewer info={info} body={body} fullHref={`?v=full`} me={me} />
      </main>
    </SearchRailProvider>
  );
}
