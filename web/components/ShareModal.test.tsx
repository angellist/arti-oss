import { describe, it, expect, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import { ShareModal, isVisibleRow } from "./ShareModal";
import type { ShareLink } from "@/lib/types";

vi.mock("@/lib/arti", () => ({
  listShares: async () => [],
  mintShare: async () => ({}),
  revokeShare: async () => {},
}));

const base = {
  artifactID: "11111111-1111-1111-1111-111111111111",
  pageURL: "https://arti.example.com/s/some-doc/3",
  version: 3,
  hasSlug: true,
  onClose: () => {},
};

describe("ShareModal", () => {
  // Copying a URL grants nothing, so everyone who can see the document gets
  // that half. Minting publishes to the internet, so it is owner-only.
  it("shows the copy-URL half to a non-owner and hides the external half", () => {
    const html = renderToStaticMarkup(<ShareModal {...base} canShare={false} />);
    expect(html).toContain("Copy link");
    expect(html).not.toContain("Create external link");
  });

  // can_share is a single server-computed flag folding owner, feature flag and
  // artifact eligibility together. The client must not re-derive any part of
  // it: `creator` is reassigned by versioning, so a delegated writer would be
  // offered a control the server then refuses.
  it("offers the external half to an owner when the feature is on", () => {
    const html = renderToStaticMarkup(<ShareModal {...base} canShare />);
    expect(html).toContain("Create external link");
    expect(html).toContain("with no AngelList account");
  });

  // Pinned is the safe option, so it is the one you get by not choosing.
  it("defaults the scope to this version", () => {
    const html = renderToStaticMarkup(<ShareModal {...base} canShare />);
    expect(html).toContain("This version (v3)");
    // The pinned radio is the checked one.
    const pinnedIdx = html.indexOf("This version");
    const latestIdx = html.indexOf("Latest version, always");
    expect(pinnedIdx).toBeGreaterThan(-1);
    expect(latestIdx).toBeGreaterThan(pinnedIdx);
    expect(html.slice(0, pinnedIdx)).toContain("checked");
  });

  // The hazard has to be readable without hovering, and it has to name
  // writers: publishing a version needs write access rather than ownership,
  // so more people can change what an external reader sees than can mint or
  // revoke the link.
  it("warns in body text that a tracking link follows any writer's versions", () => {
    const html = renderToStaticMarkup(<ShareModal {...base} canShare />);
    expect(html).toContain("versions published later, by anyone who can write to this document");
    // Not hidden behind a title/tooltip attribute.
    expect(html).not.toContain('title="This link will also show versions');
  });

  // A slugless artifact has no "latest" to track.
  it("offers no tracking option when the artifact has no slug", () => {
    const html = renderToStaticMarkup(
      <ShareModal {...base} hasSlug={false} canShare />,
    );
    expect(html).not.toContain("Latest version, always");
  });

  it("renders the page URL for copying", () => {
    const html = renderToStaticMarkup(<ShareModal {...base} canShare />);
    expect(html).toContain("https://arti.example.com/s/some-doc/3");
  });
});

describe("isVisibleRow", () => {
  const now = Date.parse("2026-08-23T00:00:00Z");
  const link = (over: Partial<ShareLink>): ShareLink => ({
    id: "x",
    token_prefix: "abcd1234",
    scope: "version",
    note: "",
    created_by: "o@example.com",
    created_at: "2026-08-01T00:00:00Z",
    expires_at: "2026-09-01T00:00:00Z",
    open_count: 0,
    ...over,
  });

  it("keeps a live link", () => {
    expect(isVisibleRow(link({}), now)).toBe(true);
  });

  // An owner asking "did this leak" needs recent history, not an empty table.
  it("keeps a link revoked within the last week", () => {
    expect(isVisibleRow(link({ revoked_at: "2026-08-21T00:00:00Z" }), now)).toBe(true);
  });

  it("drops a link revoked long ago", () => {
    expect(isVisibleRow(link({ revoked_at: "2026-07-01T00:00:00Z" }), now)).toBe(false);
  });

  it("drops a link that expired long ago", () => {
    expect(isVisibleRow(link({ expires_at: "2026-07-01T00:00:00Z" }), now)).toBe(false);
  });

  it("keeps a link that expired recently", () => {
    expect(isVisibleRow(link({ expires_at: "2026-08-22T00:00:00Z" }), now)).toBe(true);
  });
});
