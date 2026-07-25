import { describe, it, expect } from "vitest";
import { isEditableArtifact, saveAsNewVersionInput, saveConfirmMessage } from "./edit";

const base = {
  artifact_type: "TEXT" as const,
  content_type: "text/markdown",
  named_slug: "my-doc",
  deleted_at: null,
};

describe("isEditableArtifact", () => {
  it("allows a live TEXT artifact with a textual content_type and a slug", () => {
    expect(isEditableArtifact(base)).toBe(true);
  });

  it("rejects a slugless artifact — saving would create an UNRELATED artifact, not a version", () => {
    expect(isEditableArtifact({ ...base, named_slug: null })).toBe(false);
    expect(isEditableArtifact({ ...base, named_slug: "" })).toBe(false);
  });

  it("rejects non-TEXT types — packages/apps/attachments have no single editable body", () => {
    for (const t of ["PACKAGE", "APP", "ATTACHMENT"] as const) {
      expect(isEditableArtifact({ ...base, artifact_type: t })).toBe(false);
    }
  });

  it("rejects a legacy TEXT artifact with a non-textual content_type — the server refuses TEXT+binary", () => {
    expect(isEditableArtifact({ ...base, content_type: "application/pdf" })).toBe(false);
  });

  it("rejects archived artifacts", () => {
    expect(isEditableArtifact({ ...base, deleted_at: "2026-01-01T00:00:00Z" })).toBe(false);
  });
});

describe("saveAsNewVersionInput", () => {
  const info = { title: "My Doc", content_type: "text/markdown", named_slug: "my-doc" };

  it("targets the SAME slug with the same title/content_type so the server versions instead of forking", () => {
    const input = saveAsNewVersionInput(info, "hello");
    expect(input.named_slug).toBe("my-doc");
    expect(input.title).toBe("My Doc");
    expect(input.content_type).toBe("text/markdown");
    expect(input.artifact_type).toBe("TEXT");
  });

  it("omits labels/scopes so the server INHERITS them from the previous version (sending [] would clear them)", () => {
    const input = saveAsNewVersionInput(info, "hello");
    expect("labels" in input).toBe(false);
    expect("scopes" in input).toBe(false);
  });

  it("base64-encodes the edited text as UTF-8, round-tripping non-ASCII", () => {
    const text = "héllo — ✓ 中文\nline2";
    const input = saveAsNewVersionInput(info, text);
    const decoded = new TextDecoder().decode(
      Uint8Array.from(atob(input.content_base64), (c) => c.charCodeAt(0)),
    );
    expect(decoded).toBe(text);
  });
});

describe("saveConfirmMessage", () => {
  it("always warns that a new version is created and the viewed one survives", () => {
    const msg = saveConfirmMessage("my-doc", 3, 3);
    expect(msg).toContain("v4");
    expect(msg).toContain('"s/my-doc"');
    expect(msg).toContain("v3 stays unchanged");
    expect(msg).not.toContain("latest");
  });

  it("adds the supersede warning when editing an older version — saving publishes old content as newest", () => {
    const msg = saveConfirmMessage("my-doc", 1, 3);
    expect(msg).toContain("editing v1");
    expect(msg).toContain("latest is v3");
    expect(msg).toContain("as the newest version");
  });

  it("degrades to 'a new version' when the latest-version probe failed (it is best-effort)", () => {
    const msg = saveConfirmMessage("my-doc", 2, null);
    expect(msg).toContain("a new version");
    expect(msg).not.toContain("editing v2, but");
  });

  it("never prints 'vnull' when the viewed version is missing", () => {
    const msg = saveConfirmMessage("my-doc", null, null);
    expect(msg).not.toContain("vnull");
    expect(msg).toContain("the current version stays unchanged");
  });
});
