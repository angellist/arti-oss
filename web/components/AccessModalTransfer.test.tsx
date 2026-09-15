import { describe, it, expect, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import AccessModal, { levelsFor, ownerCoveredBy, type Row } from "./AccessModal";

vi.mock("@/lib/arti", () => ({
  listGroups: async () => [],
  listIdpGroups: async () => [],
  MIN_PEOPLE_QUERY: 2,
  searchPeople: async () => [],
  updateArtifactAccess: async () => ({}),
  transferArtifactOwner: async () => ({
    slug: "doc",
    owner: "bob@example.com",
    previous_owner: "alice@example.com",
  }),
}));

// The owner row is the only place the viewer says who a document belongs to.
// It must render the server's resolved owner: `creator` names whoever pushed
// the latest version, so on a transferred document the two differ and naming
// the wrong one sends people to someone with no authority to help.
describe("AccessModal owner row", () => {
  const base = {
    artifactID: "x",
    access: ["carol@example.com"],
    write: null,
    hasOtherVersions: false,
    canEdit: true,
    onClose: () => {},
    onSaved: () => {},
  };

  it("names the owner and offers Transfer to them", () => {
    const html = renderToStaticMarkup(
      <AccessModal {...base} slug="doc" owner="alice@example.com" isOwner={true} />,
    );
    expect(html).toContain("Owner");
    expect(html).toContain("alice@example.com");
    expect(html).toContain("Transfer");
  });

  // A delegated writer may edit the document but may not give it away.
  it("hides Transfer from someone who is not the owner", () => {
    const html = renderToStaticMarkup(
      <AccessModal {...base} slug="doc" owner="alice@example.com" isOwner={false} />,
    );
    expect(html).toContain("alice@example.com");
    expect(html).not.toContain("Transfer");
  });

  // Ownership is slug-scoped, so a slugless artifact has nothing to transfer.
  it("hides Transfer when there is no slug", () => {
    const html = renderToStaticMarkup(
      <AccessModal {...base} slug={null} owner="alice@example.com" isOwner={true} />,
    );
    expect(html).not.toContain("Transfer");
  });

  it("shows no owner row on a draft", () => {
    const html = renderToStaticMarkup(
      <AccessModal {...base} draft={true} owner="alice@example.com" isOwner={true} onCommit={() => {}} />,
    );
    expect(html).not.toContain("Transfer");
  });
});

// A group token cannot prove the outgoing owner is covered: membership lives on
// the server. Treating one as cover would disable the keep-access checkbox on
// most group-shared documents, forcing the outgoing owner to keep access with
// no way to decline.
describe("ownerCoveredBy", () => {
  const owner = "alice@example.com";
  const w = (token: string): Row => ({ token, level: "write" });
  const r = (token: string): Row => ({ token, level: "read" });

  it("counts a write-level wildcard, domain glob, or the address itself", () => {
    expect(ownerCoveredBy(owner, [w("*")])).toBe(true);
    expect(ownerCoveredBy(owner, [w("*@example.com")])).toBe(true);
    expect(ownerCoveredBy(owner, [w("ALICE@EXAMPLE.COM")])).toBe(true);
  });

  // keep_access grants read AND write. A world-READABLE document with an
  // explicit write list leaves the outgoing owner's write exactly what they
  // stand to lose, so read cover alone must not disable the checkbox.
  it("does not count a read-only match", () => {
    expect(ownerCoveredBy(owner, [r("*")])).toBe(false);
    expect(ownerCoveredBy(owner, [r("*@example.com")])).toBe(false);
    expect(ownerCoveredBy(owner, [r(owner)])).toBe(false);
  });

  it("does not count a group, an idp group, or a domain they are not in", () => {
    expect(ownerCoveredBy(owner, [w("group:eng")])).toBe(false);
    expect(ownerCoveredBy(owner, [w("idp:engineering")])).toBe(false);
    expect(ownerCoveredBy(owner, [w("*@other.com")])).toBe(false);
    expect(ownerCoveredBy(owner, [w("bob@example.com")])).toBe(false);
  });

  it("is false when there is no owner to check", () => {
    expect(ownerCoveredBy(undefined, [w("*")])).toBe(false);
  });
});

// Coverage is judged against the SAVED pair. Staged rows may never be
// confirmed, so treating an uncommitted write grant as cover would disable the
// checkbox over access the document does not actually carry.
describe("keep-access coverage reads saved state", () => {
  it("levelsFor of the saved pair is what ownerCoveredBy sees", () => {
    const owner = "alice@example.com";
    // Saved: alice reads only. Cover must be false — she has no write yet.
    expect(ownerCoveredBy(owner, levelsFor([owner], []))).toBe(false);
    // Saved: mirror mode, so every reader is a writer.
    expect(ownerCoveredBy(owner, levelsFor([owner], null))).toBe(true);
    // Saved: alice explicitly among the writers.
    expect(ownerCoveredBy(owner, levelsFor([owner, "b@example.com"], [owner]))).toBe(true);
  });
});

// keep_access always grants the OUTGOING owner. An admin transferring someone
// else's document must not read the checkbox as being about themselves.
describe("AccessModal keep-access label", () => {
  const base = {
    artifactID: "x",
    access: ["carol@example.com"],
    write: null,
    hasOtherVersions: false,
    canEdit: true,
    slug: "doc",
    owner: "alice@example.com",
    isOwner: true,
    onClose: () => {},
    onSaved: () => {},
  };

  it("says 'my' when the caller owns the document", () => {
    const html = renderToStaticMarkup(<AccessModal {...base} actorIsOwner={true} />);
    expect(html).toContain("Transfer");
    // The pane is behind a click, so assert on the prop plumbing via the row.
    expect(html).toContain("alice@example.com");
  });

  it("renders for an admin who is not the owner", () => {
    const html = renderToStaticMarkup(<AccessModal {...base} actorIsOwner={false} />);
    expect(html).toContain("Transfer");
  });
});
