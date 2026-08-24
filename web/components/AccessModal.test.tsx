import { describe, it, expect, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import AccessModal, { levelsFor } from "./AccessModal";

vi.mock("@/lib/arti", () => ({
  listGroups: async () => [],
  listIdpGroups: async () => [],
  // The people typeahead reads these; the modal renders no suggestions without
  // a typed draft, so a stub that never matches keeps these tests about access.
  MIN_PEOPLE_QUERY: 2,
  searchPeople: async () => [],
  updateArtifactAccess: async () => ({}),
}));

describe("AccessModal", () => {
  it("renders a row per principal with its level; idp badged SSO", () => {
    const html = renderToStaticMarkup(
      <AccessModal
        artifactID="x"
        access={["alice@example.com", "idp:engineering"]}
        write={["idp:engineering"]}
        hasOtherVersions={false}
        canEdit={true}
        onClose={() => {}}
        onSaved={() => {}}
      />,
    );
    expect(html).toContain("alice@example.com");
    expect(html).toContain("engineering");
    expect(html).toContain("SSO");
    // idp:engineering is a writer → its select defaults to the write option.
    expect(html).toContain('value="write"');
    // alice is a reader (not in write) → a read-level select is present.
    expect(html).toContain('value="read"');
  });

  it("levelsFor distinguishes mirror (null) from creator-only ([]) from a subset", () => {
    // null = write follows read → every reader is a writer.
    expect(levelsFor(["a", "b"], null).every((r) => r.level === "write")).toBe(true);
    // undefined behaves like null (defensive).
    expect(levelsFor(["a", "b"], undefined).every((r) => r.level === "write")).toBe(true);
    // [] (non-null empty) = creator-only → every listed reader is read-only.
    // This is the exact case the `omitempty` bug collapsed into mirror.
    expect(levelsFor(["a", "b"], []).every((r) => r.level === "read")).toBe(true);
    // explicit subset → only listed tokens are writers.
    expect(levelsFor(["a", "b"], ["a"])).toEqual([
      { token: "a", level: "write" },
      { token: "b", level: "read" },
    ]);
  });

  it("renders the config-neutral phrase for '*' (no tenant name)", () => {
    const html = renderToStaticMarkup(
      <AccessModal
        artifactID="x"
        access={["*"]}
        hasOtherVersions={false}
        canEdit={true}
        onClose={() => {}}
        onSaved={() => {}}
      />,
    );
    expect(html).toContain("Anyone who can sign in");
    expect(html).not.toMatch(/AngelList/i);
  });

  it("read-only variant renders no level <select> controls", () => {
    const html = renderToStaticMarkup(
      <AccessModal
        artifactID="x"
        access={["alice@example.com"]}
        hasOtherVersions={false}
        canEdit={false}
        onClose={() => {}}
        onSaved={() => {}}
      />,
    );
    expect(html).not.toContain("<select");
    expect(html).toContain("Read");
  });

  it("says edits apply to all versions when there are other versions", () => {
    const html = renderToStaticMarkup(
      <AccessModal
        artifactID="x"
        access={["*"]}
        hasOtherVersions={true}
        canEdit={true}
        onClose={() => {}}
        onSaved={() => {}}
      />,
    );
    // Access is slug-wide (DD-0055): the note must say ALL versions, and the
    // old per-version warning must be gone.
    expect(html).toContain("all versions");
    expect(html).not.toContain("this version only");
  });

  it("says nothing is saved yet in draft mode", () => {
    const html = renderToStaticMarkup(
      <AccessModal
        draft
        access={["*"]}
        hasOtherVersions={false}
        canEdit={true}
        onClose={() => {}}
        onSaved={() => {}}
        onCommit={() => {}}
      />,
    );
    expect(html).toContain("Not saved yet");
    // Staged, not implicit: Confirm lands the rows in the draft, Cancel drops them.
    expect(html).toContain("Confirm");
    expect(html).toContain("Cancel");
  });

  it("edits are staged behind Confirm/Cancel, and Confirm starts disabled", () => {
    const html = renderToStaticMarkup(
      <AccessModal
        artifactID="x"
        access={["alice@example.com"]}
        hasOtherVersions={false}
        canEdit={true}
        onClose={() => {}}
        onSaved={() => {}}
      />,
    );
    expect(html).toContain("Confirm");
    expect(html).toContain("Cancel");
    // Nothing changed yet → Confirm is not lit up.
    expect(html).toContain("no changes");
  });

  it("read-only variant gets a Close button and no Confirm", () => {
    const html = renderToStaticMarkup(
      <AccessModal
        artifactID="x"
        access={["alice@example.com"]}
        hasOtherVersions={false}
        canEdit={false}
        onClose={() => {}}
        onSaved={() => {}}
      />,
    );
    expect(html).toContain("Close");
    expect(html).not.toContain("Confirm");
  });
});
