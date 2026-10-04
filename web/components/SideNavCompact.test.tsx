// @vitest-environment jsdom

import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";

import { RailModeShell } from "@/lib/rail-context";
import SideNavCompact from "./SideNavCompact";

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: () => {}, refresh: () => {} }),
  useSearchParams: () => new URLSearchParams("type=TEXT&q=label:memo"),
  usePathname: () => "/",
}));

vi.mock("@/lib/arti", () => ({ listApiKeys: async () => [] }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("SideNavCompact", () => {
  afterEach(() => window.localStorage.clear());

  it("persists the catalog filter so Search from a doc view restores it", () => {
    const container = document.createElement("div");
    const root = createRoot(container);
    act(() => {
      root.render(
        <RailModeShell>
          <SideNavCompact me={null} onExpand={() => {}} expandIcon={null} />
        </RailModeShell>,
      );
    });
    expect(window.localStorage.getItem("arti.filter.type")).toBe("TEXT");
    expect(window.localStorage.getItem("arti.filter.q")).toBe("label:memo");
    act(() => root.unmount());
  });
});
