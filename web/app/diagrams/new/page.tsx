import { redirect } from "next/navigation";

// The original "new diagram" route, kept as a redirect: it shipped, it's linked
// from web/docs/guides/diagrams.md, and people bookmark URLs. Authoring now
// lives under one /new/<kind> family.
export default function LegacyNewDiagramPage() {
  redirect("/new/diagram");
}
