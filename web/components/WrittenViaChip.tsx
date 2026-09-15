import { credentialLabel, credentialTooltip } from "@/lib/credential";

// WrittenViaChip names the credential a version was written with, next to the
// creator it was written as. The two differ whenever something other than the
// person held the credential, and nothing in the catalog used to show that.
export default function WrittenViaChip({
  writtenVia,
  writtenViaName,
}: {
  writtenVia: string | null;
  writtenViaName: string | null;
}) {
  const label = credentialLabel(writtenVia, writtenViaName);
  if (!label || !writtenVia) return null;
  return (
    <a
      href={`/?q=${encodeURIComponent(`via:${writtenVia}`)}`}
      title={credentialTooltip(writtenVia)}
      className="rounded bg-amber-50 px-1.5 py-0.5 text-[10px] text-amber-800 hover:bg-amber-100"
    >
      via {label}
    </a>
  );
}
