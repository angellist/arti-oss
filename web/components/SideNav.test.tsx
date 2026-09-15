// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import SideNav from "./SideNav";
import { RailModeShell } from "@/lib/rail-context";

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: () => {}, refresh: () => {} }),
  useSearchParams: () => new URLSearchParams(""),
  usePathname: () => "/",
}));

vi.mock("@/lib/arti", () => ({
  getAggregates: async () => ({ scope_types: [], scopes: [], labels: [], content_types: [] }),
  getMe: async () => null,
  listApiKeys: async () => [],
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function click(el: Element) {
  act(() => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

describe("SideNav mobile drawer", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    act(() => {
      root.render(
        <RailModeShell>
          <SideNav />
        </RailModeShell>,
      );
    });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  const drawer = () => document.querySelector('[aria-label="navigation drawer"]');
  const openDrawer = () => {
    const toggle = container.querySelector('[aria-label="open navigation"]');
    click(toggle as Element);
  };

  it("closes on a plain rail link so a navigation doesn't leave it covering the page", () => {
    openDrawer();
    expect(drawer()).not.toBeNull();
    const apps = Array.from(drawer()!.querySelectorAll("a")).find((a) => /apps/i.test(a.textContent ?? ""));
    click(apps as Element);
    expect(drawer()).toBeNull();
  });

  it("stays open when a submenu trigger is tapped, or its items unmount before they can be used", () => {
    openDrawer();
    const newTrigger = drawer()!.querySelector('[aria-haspopup="menu"]');
    expect(newTrigger).not.toBeNull();
    click(newTrigger as Element);
    expect(drawer()).not.toBeNull();
    expect(document.querySelector('[role="menu"]')).not.toBeNull();
  });
});
