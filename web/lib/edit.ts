// Pure logic behind the viewer's "Edit" action (TEXT artifacts only):
// which artifacts may be edited, what the save POSTs, and what the
// confirm dialog must warn about. Kept framework-free so the invariants
// that make Edit safe are unit-testable (see edit.test.ts).

import type { ArtifactInfo } from "./types";
import type { CreateArtifactInput } from "./arti";
import { isTextualContentType } from "./viewer";
import { bytesToBase64 } from "./upload";

// isEditableArtifact gates the ⋯-menu "Edit" item. TEXT with a textual
// content_type only (the server rejects TEXT + binary anyway), and only
// when a slug exists: new versions attach to a slug, so "saving" a
// slugless artifact would create an unrelated artifact, not a version.
// Not creator-gated on purpose — versioning a slug is write==read on the
// server (any reader may POST a new version), and the UI mirrors that.
export function isEditableArtifact(
  info: Pick<ArtifactInfo, "artifact_type" | "content_type" | "named_slug" | "deleted_at">,
): boolean {
  return (
    !info.deleted_at &&
    info.artifact_type === "TEXT" &&
    isTextualContentType(info.content_type) &&
    !!info.named_slug
  );
}

// saveAsNewVersionInput builds the POST body that turns edited text into a
// new version of the SAME slug. Title and content_type carry over from the
// viewed version; labels/scopes/allowed_access are deliberately omitted so
// the server inherits them from the slug's previous version (sending [] here
// would clear them instead).
export function saveAsNewVersionInput(
  info: Pick<ArtifactInfo, "title" | "content_type" | "named_slug">,
  text: string,
): CreateArtifactInput {
  return {
    title: info.title,
    content_type: info.content_type,
    artifact_type: "TEXT",
    content_base64: bytesToBase64(new TextEncoder().encode(text).buffer as ArrayBuffer),
    named_slug: info.named_slug ?? "",
  };
}

// saveConfirmMessage is the warning shown before a save commits. Always
// says a new version is being created (and that the viewed one survives);
// when the viewed version is NOT the slug's latest, it adds the supersede
// warning — saving publishes this older content as the newest version.
// `latest` is the best-effort probe (null when unknown).
export function saveConfirmMessage(
  slug: string,
  version: number | null,
  latest: number | null,
): string {
  const nextV = latest != null ? `v${latest + 1}` : "a new version";
  // Slugged artifacts always carry a version in practice, but the type
  // allows null — fall back to prose rather than printing "vnull".
  const curV = version != null ? `v${version}` : "the current version";
  let msg =
    `Save your edits as ${nextV} of "s/${slug}"?\n\n` +
    `This creates a new version — ${curV} stays unchanged.`;
  if (version != null && latest != null && latest !== version) {
    msg +=
      `\n\nNote: you are editing v${version}, but the latest is v${latest}. ` +
      `Saving publishes this older content (plus your edits) as the newest version.`;
  }
  return msg;
}
