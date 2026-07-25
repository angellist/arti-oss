import { notFound } from "next/navigation";

import { SearchRailProvider } from "@/lib/rail-context";
import NewArtifact from "@/components/NewArtifact";
import { KINDS, type NewKind } from "@/lib/newartifact";

export const dynamic = "force-dynamic";

// One route for every authoring kind, so adding a kind is a KINDS entry rather
// than a new page. Both create ordinary TEXT artifacts; the kind selects the
// content type and which editor mounts.
export async function generateMetadata({ params }: { params: Promise<{ kind: string }> }) {
  const { kind } = await params;
  const spec = KINDS[kind as NewKind];
  return { title: spec ? `New ${spec.label.toLowerCase()}` : "New artifact" };
}

export default async function NewArtifactPage({ params }: { params: Promise<{ kind: string }> }) {
  const { kind } = await params;
  if (!(kind in KINDS)) notFound();
  return (
    <SearchRailProvider>
      <main className="min-h-screen bg-white pb-12">
        {/* Keyed by kind: both kinds render the same component at the same
            position, so navigating /new/text → /new/diagram would otherwise
            reconcile onto the existing instance and carry the previous draft's
            title, slug, stamped default and body across. The key forces a fresh
            draft per kind. */}
        <NewArtifact key={kind} kind={kind as NewKind} />
      </main>
    </SearchRailProvider>
  );
}
