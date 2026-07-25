// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";

import AccessModal from "./AccessModal";

vi.mock("@/lib/arti", () => ({
  listGroups: async () => [],
  listIdpGroups: async () => [],
  updateArtifactAccess: async () => ({}),
}));

// Draft mode reports mirror mode as null, not as "every reader, listed".
//
// Why it matters: on the version being created the two look identical, but an
// explicit allowed_write is STICKY — later versions inherit it, and since the
// server unions write into read (the ⊆ invariant), a stored write of ['*'] would
// silently re-widen read access the first time someone narrows it to a domain.
describe("AccessModal draft mode preserves mirror", () => {
  let container: HTMLDivElement;
  let root: Root;

  const render = (props: Partial<Parameters<typeof AccessModal>[0]> & { onCommit: (a: string[], w: string[] | null) => void }) => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    act(() => {
      root.render(
        <AccessModal
          draft
          access={["a@example.com", "b@example.com"]}
          write={null}
          hasOtherVersions={false}
          canEdit={true}
          onClose={() => {}}
          onSaved={() => {}}
          {...props}
        />,
      );
    });
  };

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    document.body.innerHTML = "";
  });

  const removeFirst = () => {
    const btn = Array.from(document.querySelectorAll("button")).find((b) =>
      (b.getAttribute("aria-label") || "").startsWith("remove"),
    );
    act(() => btn!.click());
  };

  it("reports null (mirror) when every remaining reader is still a writer", () => {
    const onCommit = vi.fn();
    render({ onCommit });
    removeFirst();
    expect(onCommit).toHaveBeenCalledTimes(1);
    const [access, write] = onCommit.mock.calls[0];
    expect(access).toEqual(["b@example.com"]);
    // NOT ["b@example.com"] — an explicit write list is sticky and would
    // re-widen read access on a later narrowing.
    expect(write).toBeNull();
  });

  it("reports an explicit list once read and write genuinely differ", () => {
    const onCommit = vi.fn();
    render({ onCommit, write: ["a@example.com"] });
    removeFirst(); // drop a@ — b@ remains, and b@ is read-only
    const [access, write] = onCommit.mock.calls[0];
    expect(access).toEqual(["b@example.com"]);
    expect(write).toEqual([]);
  });

  it("never reports mirror for creator-only writes ([])", () => {
    const onCommit = vi.fn();
    render({ onCommit, write: [] });
    removeFirst();
    const [, write] = onCommit.mock.calls[0];
    expect(write).not.toBeNull();
    expect(write).toEqual([]);
  });
});
