"use client";

import { useEffect, useState, type ReactNode } from "react";

import { listShares } from "@/lib/arti";
import AccessModal from "./AccessModal";

// AccessSummary returns a (color, label, tooltip) tuple describing the
// effective read-access for the artifact. Tier classification:
//   - public      array contains '*'  (anyone authenticated)
//   - domain      every pattern is a `*@something` glob — show the domain(s)
//   - restricted  specific emails (and/or empty array == creator-only)
function accessTier(patterns: string[]): {
  tier: "public" | "domain" | "restricted";
  label: string;
  tooltip: string;
} {
  if (patterns.length === 0) {
    return { tier: "restricted", label: "private", tooltip: "creator and admins only" };
  }
  if (patterns.includes("*")) {
    return { tier: "public", label: "public", tooltip: "any authenticated user" };
  }
  const allDomain = patterns.every((p) => /^\*@[^*?]+$/.test(p));
  if (allDomain) {
    const domains = patterns.map((p) => p.slice(2)).join(", ");
    return { tier: "domain", label: domains, tooltip: `restricted to: ${domains}` };
  }
  return {
    tier: "restricted",
    label: patterns.length === 1 ? patterns[0] : `${patterns.length} entries`,
    tooltip: `restricted to: ${patterns.join(", ")}`,
  };
}

// AccessButton renders the "Edit Access" trigger in the controls row, or the
// caller's `trigger`. Clicking it opens the centered AccessModal. Readers see
// the same button labeled "Access" with a view-only modal.
export default function AccessButton({
  artifactID,
  access,
  write,
  hasOtherVersions,
  canShare,
  slug,
  owner,
  isOwner,
  actorIsOwner,
  onSaved,
  trigger,
}: {
  artifactID: string;
  access: string[];
  write?: string[] | null;
  hasOtherVersions: boolean;
  // Changing access and transferring both need owner authority on the server,
  // so one flag gates the editor, the button's label and the Transfer control
  // alike. A version's creator who does not own the document would otherwise
  // get an editable dialog whose Confirm returns 403.
  slug?: string | null;
  owner?: string;
  isOwner: boolean;
  actorIsOwner: boolean;
  // The server's `can_share`. Gates the live-link count, which is owner-only
  // information and is not worth a request otherwise.
  canShare: boolean;
  onSaved: () => void;
  // Replaces the toolbar button; the modal and its behaviour stay the same.
  trigger?: (t: { open: () => void; dot: string; tooltip: string; isOwner: boolean }) => ReactNode;
}) {
  const [open, setOpen] = useState(false);
  // Live external links on this document, counted only when the modal opens:
  // it is owner-only information and nobody pays for it on page load.
  const [liveShares, setLiveShares] = useState(0);

  // Reset on a document switch. Without this the count survives: the effect
  // below returns early when the new document is not shareable, so a previous
  // document's non-zero count would keep rendering AccessModal's amber "N live
  // external links" warning against a document that has none.
  const [countedFor, setCountedFor] = useState(artifactID);
  if (artifactID !== countedFor) {
    setCountedFor(artifactID);
    setLiveShares(0);
  }

  useEffect(() => {
    if (!open || !canShare) return;
    let cancelled = false;
    void listShares(artifactID)
      .then((rows) => {
        if (cancelled) return;
        const now = Date.now();
        setLiveShares(
          rows.filter((l) => !l.revoked_at && new Date(l.expires_at).getTime() > now).length,
        );
      })
      // A failed count must not break the access editor; the notice is an
      // extra warning, not a precondition for editing.
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [open, canShare, artifactID]);

  const tier = accessTier(access);
  const dot =
    tier.tier === "public"
      ? "bg-emerald-400"
      : tier.tier === "domain"
      ? "bg-amber-400"
      : "bg-rose-500";

  return (
    <>
      {trigger ? (
        trigger({ open: () => setOpen(true), dot, tooltip: tier.tooltip, isOwner })
      ) : (
        <button
          type="button"
          onClick={() => setOpen(true)}
          aria-haspopup="dialog"
          title={tier.tooltip}
          className="inline-flex items-center gap-1.5 rounded-md border border-neutral-200 bg-white px-3 py-1 text-xs text-neutral-700 transition hover:bg-neutral-50"
        >
          <span className={`inline-block h-1.5 w-1.5 rounded-full ${dot}`} aria-hidden="true" />
          {isOwner ? "Edit Access" : "Access"}
        </button>
      )}
      {open ? (
        <AccessModal
          artifactID={artifactID}
          access={access}
          write={write}
          hasOtherVersions={hasOtherVersions}
          canEdit={isOwner}
          slug={slug}
          owner={owner}
          isOwner={isOwner}
          actorIsOwner={actorIsOwner}
          liveShareLinks={liveShares}
          onClose={() => setOpen(false)}
          onSaved={onSaved}
        />
      ) : null}
    </>
  );
}
