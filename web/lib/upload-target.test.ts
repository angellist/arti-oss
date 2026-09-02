import { describe, expect, it } from "vitest";
import { classifyTypeChange, targetFromInfo, type UploadTarget } from "./upload-target";
import type { ArtifactInfo } from "./types";

function info(over: Partial<ArtifactInfo> = {}): ArtifactInfo {
  return {
    artifact_id: "11111111-2222-3333-4444-555555555555",
    artifact_type: "TEXT",
    named_slug: "quarterly-report",
    version: 7,
    title: "Quarterly report",
    description: "How the quarter went",
    content_type: "text/markdown",
    size_bytes: 10,
    sha256: "abc",
    creator: "tian@example.com",
    scopes: ["a:reports"],
    labels: ["report", "arti"],
    allowed_access: ["*"],
    allowed_write: null,
    can_write: true,
    metadata: {},
    created_at: "2026-08-01T00:00:00Z",
    modified_at: "2026-08-01T00:00:00Z",
    deleted_at: null,
    url: "/s/quarterly-report",
    ...over,
  };
}

describe("targetFromInfo", () => {
  it("carries the document's own metadata for the modal to inherit", () => {
    expect(targetFromInfo(info())).toEqual<UploadTarget>({
      slug: "quarterly-report",
      viewedVersion: 7,
      artifactType: "TEXT",
      contentType: "text/markdown",
      title: "Quarterly report",
      description: "How the quarter went",
      labels: ["report", "arti"],
      scopes: ["a:reports"],
    });
  });

  // A slugless artifact has no lineage: version numbers are derived per slug,
  // so there is nothing for a dropped file to be the next version OF.
  it("is null without a slug", () => {
    expect(targetFromInfo(info({ named_slug: null, version: null }))).toBeNull();
  });

  it("is null when archived", () => {
    expect(targetFromInfo(info({ deleted_at: "2026-08-02T00:00:00Z" }))).toBeNull();
  });

  // can_write is the server's own verdict. Absent (a payload from somewhere
  // that doesn't compute it) must not fall back to a client guess — offering
  // the affordance would just produce a 403 on submit.
  it("is null when the server says the caller can't write, or doesn't say", () => {
    expect(targetFromInfo(info({ can_write: false }))).toBeNull();
    expect(targetFromInfo(info({ can_write: undefined }))).toBeNull();
  });
});

describe("classifyTypeChange", () => {
  const text = { slug: "doc", artifactType: "TEXT" as const, contentType: "text/markdown" };

  it("is null when the document's kind is unchanged", () => {
    expect(
      classifyTypeChange(text, { artifactType: "TEXT", contentType: "text/markdown" }),
    ).toBeNull();
  });

  // Parameters aren't part of the document's kind — a re-upload that adds a
  // charset must not read as a type change.
  it("ignores content-type parameters and case", () => {
    expect(
      classifyTypeChange(text, { artifactType: "TEXT", contentType: "Text/Markdown; charset=utf-8" }),
    ).toBeNull();
  });

  it("gates a change of artifact type", () => {
    const c = classifyTypeChange(text, { artifactType: "PACKAGE", contentType: "application/zip" });
    expect(c).toMatchObject({ severity: "hard", kind: "artifact-type", from: "TEXT", to: "PACKAGE" });
  });

  // The one that broke this document in review: an HTML page republished as
  // markdown renders as source, and nothing warned about it.
  it("gates a change of content type inside one artifact type", () => {
    const html = { ...text, contentType: "text/html" };
    const c = classifyTypeChange(html, { artifactType: "TEXT", contentType: "text/markdown" });
    expect(c).toMatchObject({
      severity: "hard",
      kind: "content-type",
      from: "text/html",
      to: "text/markdown",
    });
  });

  it("gates a binary landing on a text document", () => {
    const c = classifyTypeChange(text, { artifactType: "ATTACHMENT", contentType: "application/pdf" });
    expect(c).toMatchObject({ severity: "hard", from: "TEXT", to: "ATTACHMENT" });
  });

  // Gaining app-hood takes nothing away, so it is reported without a gate.
  it("reports PACKAGE → APP without demanding an acknowledgement", () => {
    const pkg = { slug: "kit", artifactType: "PACKAGE" as const, contentType: "application/zip" };
    expect(classifyTypeChange(pkg, { artifactType: "APP", contentType: "application/zip" })).toMatchObject(
      { severity: "info", from: "PACKAGE", to: "APP" },
    );
  });

  // Losing it does take something away — /app/<slug> stops serving — and the
  // cause is worth naming, because the fix is one re-drop.
  it("gates APP → PACKAGE and names the cause", () => {
    const app = { slug: "design-kit", artifactType: "APP" as const, contentType: "application/zip" };
    const c = classifyTypeChange(app, { artifactType: "PACKAGE", contentType: "application/zip" });
    expect(c?.severity).toBe("hard");
    expect(c?.headline).toContain("design-kit");
    expect(c?.note).toContain("arti-app.json");
  });
});
