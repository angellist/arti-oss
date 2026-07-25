import { describe, it, expect, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import AccessModal, { levelsFor } from "./AccessModal";

vi.mock("@/lib/arti", () => ({
  listGroups: async () => [],
  listIdpGroups: async () => [],
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

  it("shows the this-version-only note when there are other versions", () => {
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
    expect(html).toContain("this version only");
  });
});
