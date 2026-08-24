// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AccessModal from "./AccessModal";

const updateArtifactAccess = vi.fn(async () => ({}));

vi.mock("@/lib/arti", () => ({
  listGroups: async () => [],
  listIdpGroups: async () => [],
  // The people typeahead reads these; the modal renders no suggestions without
  // a typed draft, so a stub that never matches keeps these tests about access.
  MIN_PEOPLE_QUERY: 2,
  searchPeople: async () => [],
  updateArtifactAccess: (...args: unknown[]) => updateArtifactAccess(...(args as [])),
}));

// The access editor stages edits: nothing is written until Confirm, Confirm is
// dead until something actually changed, and Cancel throws the staging away.
describe("AccessModal explicit commit", () => {
  let container: HTMLDivElement;
  let root: Root;
  const onClose = vi.fn();
  const onSaved = vi.fn();

  beforeEach(() => {
    updateArtifactAccess.mockClear();
    onClose.mockClear();
    onSaved.mockClear();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    act(() => {
      root.render(
        <AccessModal
          artifactID="art_1"
          access={["a@example.com", "b@example.com"]}
          write={["a@example.com"]}
          hasOtherVersions={false}
          canEdit={true}
          onClose={onClose}
          onSaved={onSaved}
        />,
      );
    });
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    document.body.innerHTML = "";
  });

  const button = (label: string) =>
    Array.from(document.querySelectorAll("button")).find((b) => b.textContent?.trim() === label) as
      | HTMLButtonElement
      | undefined;
  const removeFirst = () => {
    const btn = Array.from(document.querySelectorAll("button")).find((b) =>
      (b.getAttribute("aria-label") || "").startsWith("remove"),
    );
    act(() => btn!.click());
  };

  it("starts with Confirm disabled and PATCHes nothing on edit", () => {
    expect(button("Confirm")!.disabled).toBe(true);
    removeFirst();
    // Edit is staged only — the row is gone from the list, the server untouched.
    expect(updateArtifactAccess).not.toHaveBeenCalled();
    expect(button("Confirm")!.disabled).toBe(false);
  });

  it("Confirm writes the staged rows once, then saves and closes", async () => {
    removeFirst();
    await act(async () => {
      button("Confirm")!.click();
    });
    expect(updateArtifactAccess).toHaveBeenCalledTimes(1);
    expect(updateArtifactAccess).toHaveBeenCalledWith("art_1", ["b@example.com"], []);
    expect(onSaved).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("Cancel discards staged edits without writing", () => {
    removeFirst();
    act(() => button("Cancel")!.click());
    expect(updateArtifactAccess).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
