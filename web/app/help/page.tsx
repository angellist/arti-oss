import { redirect } from "next/navigation";
import { loadHelpManifest } from "@/lib/help-docs";

export default function HelpIndex() {
  const first = loadHelpManifest()[0]?.docs[0];
  redirect(first ? `/help/${first.slug}` : "/");
}
