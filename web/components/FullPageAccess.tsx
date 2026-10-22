"use client";

import { useRouter } from "next/navigation";

import AccessButton from "./AccessButton";
import { EXIT_SIZE, cornerButtonClass, useHostTheme } from "./FullPageExit";
import { sameEmail } from "@/lib/arti";
import { useTopLevelWindow } from "@/lib/useViewport";
import type { ArtifactInfo, Me } from "@/lib/types";

// The ?v=full view's access control: a square the size of the ✕, just left of
// it, showing the read-access tier as a dot. Opens the same AccessModal as the
// normal viewer — editable for the owner, read-only for everyone else.
export default function FullPageAccess({ info, me }: { info: ArtifactInfo; me?: Me | null }) {
  const router = useRouter();
  const topLevel = useTopLevelWindow();
  const dark = useHostTheme();
  if (!topLevel) return null;
  return (
    <AccessButton
      artifactID={info.artifact_id}
      access={info.allowed_access}
      write={info.allowed_write}
      hasOtherVersions={!!info.named_slug}
      canShare={!!info.can_share}
      slug={info.named_slug}
      owner={info.owner}
      isOwner={!!info.can_manage_comments}
      actorIsOwner={!!me && !!info.owner && sameEmail(me.email, info.owner)}
      onSaved={() => router.refresh()}
      trigger={({ open, dot, tooltip, isOwner }) => (
        <button
          type="button"
          onClick={open}
          aria-haspopup="dialog"
          title={tooltip}
          aria-label={`${isOwner ? "edit access" : "access"}: ${tooltip}`}
          style={{
            top: "var(--arti-top-strip, 0px)",
            right: EXIT_SIZE,
            width: EXIT_SIZE,
            height: EXIT_SIZE,
          }}
          className={cornerButtonClass(dark)}
        >
          <span className={`block h-[7px] w-[7px] rounded-full ${dot}`} aria-hidden="true" />
        </button>
      )}
    />
  );
}
