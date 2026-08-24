// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import SideNavSearch from "./SideNavSearch";
import { RailModeShell } from "@/lib/rail-context";

const nav = vi.hoisted(() => ({ search: "", pushed: [] as string[] }));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: (href: string) => nav.pushed.push(href), refresh: () => {} }),
  useSearchParams: () => new URLSearchParams(nav.search),
  usePathname: () => "/",
}));

vi.mock("@/lib/arti", () => ({
  getAggregates: async () => ({ scope_types: [], scopes: [], labels: [], content_types: [] }),
  getMe: async () => null,
}));

// Not under test, and both reach for context the rail doesn't provide here.
vi.mock("./UploadButton", () => ({ default: () => null }));
vi.mock("./NewMenu", () => ({ default: () => null }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("SideNavSearch SEARCH item", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    nav.search = "";
    nav.pushed.length = 0;
    window.history.replaceState(null, "", "/");
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  const render = () => {
    act(() => {
      root.render(
        <RailModeShell>
          <SideNavSearch />
        </RailModeShell>,
      );
    });
  };

  const searchItem = () =>
    Array.from(container.querySelectorAll("button")).find(
      (b) => (b.textContent ?? "").trim() === "Search",
    )!;

  it("opens the bar shallowly on the plain catalog", () => {
    render();
    act(() => searchItem().click());
    expect(nav.pushed, "the bar is a flag away; no round trip for it").toEqual([]);
    expect(window.location.search).toBe("?find=1");
  });

  // A drill-in's pathname is also "/", but CatalogTable hides the bar while
  // `slug` is set — so flipping the flag there would look like SEARCH doing
  // nothing. It has to navigate out of the drill-in instead.
  it("navigates out of a slug drill-in instead of flipping a hidden flag", () => {
    nav.search = "slug=weekly-report&order_by=version&order_dir=desc";
    window.history.replaceState(null, "", `/?${nav.search}`);
    render();

    act(() => searchItem().click());

    expect(nav.pushed, "SEARCH must go somewhere the bar is actually visible").toHaveLength(1);
    expect(nav.pushed[0], "the drill-in is what hides the bar, so it goes").not.toContain("slug=");
    expect(nav.pushed[0]).toContain("find=1");
  });
});
