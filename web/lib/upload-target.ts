// Drop-to-version: the document a dropped file lands on, and what changes
// when the dropped file isn't the same kind of thing the document already is.
// Framework-free so both rules are unit-testable (upload-target.test.ts).

import type { ArtifactInfo, ArtifactType } from "./types";
import { baseContentType } from "./upload";

// UploadTarget is the document the viewer is showing, seeded from the payload
// it already has. `viewedVersion` is the version on screen, which is NOT
// necessarily the slug's latest — the modal re-reads the slug on open and
// publishes ahead of whatever it finds. Never treat this as the base version.
export interface UploadTarget {
  slug: string;
  viewedVersion: number | null;
  artifactType: ArtifactType;
  contentType: string;
  title: string;
  description: string;
  labels: string[];
  scopes: string[];
}

// targetFromInfo derives the drop target for a viewer payload, or null when
// this document can't receive a version.
//
// A slug is the hard requirement: version numbers are derived per slug, so a
// slugless artifact has no lineage to extend and a drop there stays what it is
// today — a new document. Archived documents are excluded (a version would
// resurrect the slug past the archive), and write access comes from the
// server's own `can_write` verdict, never a client re-derivation off `creator`
// — versioning reassigns that field.
export function targetFromInfo(info: ArtifactInfo): UploadTarget | null {
  if (!info.named_slug || info.deleted_at || info.can_write !== true) return null;
  return {
    slug: info.named_slug,
    viewedVersion: info.version,
    artifactType: info.artifact_type,
    contentType: info.content_type,
    title: info.title,
    description: info.description ?? "",
    labels: info.labels ?? [],
    scopes: info.scopes ?? [],
  };
}

// TypeChangeWarning describes a publish that changes what the document IS.
// `hard` demands an explicit acknowledgement before the upload can go; `info`
// is purely additive and only tells the user what they gained.
export interface TypeChangeWarning {
  severity: "info" | "hard";
  // kind picks which pair of chips to render: the artifact type, or the
  // content type inside one artifact type.
  kind: "artifact-type" | "content-type";
  from: string;
  to: string;
  headline: string;
  // note is the one extra clause worth a line — the cause of the change where
  // knowing it makes the fix obvious.
  note?: string;
}

// classifyTypeChange compares the document against the dropped file. Returns
// null when nothing about the document's kind changes.
//
// Every transition is acknowledged except gaining app-hood: PACKAGE → APP adds
// a launcher and takes nothing away, so it is reported, not gated.
export function classifyTypeChange(
  target: Pick<UploadTarget, "slug" | "artifactType" | "contentType">,
  next: { artifactType: ArtifactType; contentType: string },
): TypeChangeWarning | null {
  if (target.artifactType !== next.artifactType) {
    if (target.artifactType === "PACKAGE" && next.artifactType === "APP") {
      return {
        severity: "info",
        kind: "artifact-type",
        from: "PACKAGE",
        to: "APP",
        headline: "This becomes a live app",
      };
    }
    if (target.artifactType === "APP" && next.artifactType === "PACKAGE") {
      return {
        severity: "hard",
        kind: "artifact-type",
        from: "APP",
        to: "PACKAGE",
        headline: `s/${target.slug} stops being a live app`,
        note: "the zip has no arti-app.json at its root",
      };
    }
    return {
      severity: "hard",
      kind: "artifact-type",
      from: target.artifactType,
      to: next.artifactType,
      headline: "This changes the document's type",
    };
  }
  const from = baseContentType(target.contentType);
  const to = baseContentType(next.contentType);
  if (from === to) return null;
  return {
    severity: "hard",
    kind: "content-type",
    from,
    to,
    headline: "This changes how the document renders",
  };
}
