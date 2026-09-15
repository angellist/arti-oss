import { redirect } from "next/navigation";

// Archived is a tab under Settings now; keep the old path working for links
// already in the wild, carrying their page / filter / sort params across.
export default async function ArchivedRedirect({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const qs = new URLSearchParams();
  for (const [key, value] of Object.entries(await searchParams)) {
    for (const v of Array.isArray(value) ? value : [value]) {
      if (v !== undefined) qs.append(key, v);
    }
  }
  const query = qs.toString();
  redirect(query ? `/settings/archived?${query}` : "/settings/archived");
}
