import { notFound } from "next/navigation";
import { loadHelpManifest, loadHelpDoc } from "@/lib/help-docs";
import { renderMarkdown, PROSE_CLASSNAME } from "@/lib/markdown";
import { rewriteHelpLinks } from "@/lib/help-links";
import HelpDoc from "@/components/HelpDoc";

export function generateStaticParams() {
  return loadHelpManifest().flatMap((s) =>
    s.docs.map((d) => ({ slug: d.slug.split("/") })),
  );
}

export const dynamicParams = false; // only prebuilt docs; unknown slugs → 404

export default async function HelpDocPage({ params }: { params: Promise<{ slug: string[] }> }) {
  const { slug } = await params;
  const found = loadHelpDoc(slug.join("/"));
  if (!found) notFound();
  // Frontmatter (title/order/summary) is nav metadata only — split off and not
  // shown in the reader (unlike the artifact viewer, which displays it verbatim).
  const { html } = renderMarkdown(found.markdown);
  const linked = rewriteHelpLinks(html, found.doc.section);
  return <HelpDoc html={linked} className={PROSE_CLASSNAME} />;
}
