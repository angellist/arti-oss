// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { CollapseToggle, EditableTitle } from "./ArtifactViewer";

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: () => {}, back: () => {}, refresh: () => {} }),
  useSearchParams: () => new URLSearchParams(""),
  usePathname: () => "/a/11111111-2222-3333-4444-555555555555",
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("EditableTitle", () => {
  let container: HTMLDivElement;
  let root: Root;
  let collapses: number;

  beforeEach(() => {
    collapses = 0;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  const render = (canEdit: boolean) => {
    act(() => {
      root.render(
        <EditableTitle
          initial="Weekly report"
          canEdit={canEdit}
          isPackage={false}
          onSave={async () => {}}
          onCollapse={() => {
            collapses += 1;
          }}
        />,
      );
    });
  };

  const h1 = () => container.querySelector("h1") as HTMLElement;
  const input = () => container.querySelector("input") as HTMLInputElement | null;

  // The collapse used to be deferred 400 ms so a second click could still mean
  // "rename", which made the title feel broken next to the triangle that
  // toggles the same header instantly. No timer: the click IS the collapse.
  it("collapses on the click itself, with no disambiguation delay", () => {
    vi.useFakeTimers();
    try {
      render(true);
      act(() => h1().click());
      expect(collapses, "the header must toggle without advancing any timer").toBe(1);
      act(() => vi.advanceTimersByTime(1000));
      expect(collapses, "and nothing may fire later").toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("collapses for a viewer who cannot rename", () => {
    render(false);
    act(() => h1().click());
    expect(collapses).toBe(1);
  });

  // Two clicks toggle twice, so the header lands back where it started before
  // the input replaces it — which is why dropping the timer costs no state.
  it("still opens the rename input on a double-click, net-neutral on collapse", () => {
    render(true);
    act(() => {
      h1().click();
      h1().click();
      h1().dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    });
    expect(input(), "double-click renames").not.toBeNull();
    expect(collapses % 2, "and leaves the header as it found it").toBe(0);
  });

  it("does not open the rename input for a viewer who cannot rename", () => {
    render(false);
    act(() => {
      h1().click();
      h1().click();
      h1().dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    });
    expect(input()).toBeNull();
  });
});

describe("CollapseToggle", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  // It is the fast path for collapsing the header, so it has to be findable
  // and hittable — at 10px in a p-0.5 button it was a ~14px target.
  it("draws a glyph big enough to aim at", () => {
    act(() => {
      root.render(<CollapseToggle collapsed={false} onClick={() => {}} label="collapse header" />);
    });
    const svg = container.querySelector("svg") as SVGElement;
    expect(Number(svg.getAttribute("width"))).toBeGreaterThanOrEqual(14);
    expect(Number(svg.getAttribute("height"))).toBeGreaterThanOrEqual(14);
  });
});
