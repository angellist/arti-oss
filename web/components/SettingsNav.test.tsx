// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import SettingsNav from "./SettingsNav";

const nav = vi.hoisted(() => ({ pathname: "/settings/groups" }));

vi.mock("next/navigation", () => ({ usePathname: () => nav.pathname }));
vi.mock("next/link", () => ({
  default: ({ href, children, className }: { href: string; children: React.ReactNode; className?: string }) => (
    <a href={href} className={className}>
      {children}
    </a>
  ),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  nav.pathname = "/settings/groups";
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

const render = (canManageRoles: boolean) =>
  act(() => {
    root.render(<SettingsNav canManageRoles={canManageRoles} showApiKeys />);
  });

const hrefs = () =>
  Array.from(container.querySelectorAll("a")).map((a) => a.getAttribute("href"));

describe("SettingsNav", () => {
  it("hides Users from a caller without MANAGE_ROLES", () => {
    render(false);
    expect(hrefs()).not.toContain("/settings/users");
    // User Groups is for everyone, so its presence is what proves the nav
    // rendered at all rather than the assertion passing on an empty tree.
    expect(hrefs()).toContain("/settings/groups");
  });

  it("shows Users to a MANAGE_ROLES holder, above User Groups", () => {
    render(true);
    const order = hrefs();
    expect(order).toContain("/settings/users");
    expect(order.indexOf("/settings/users")).toBeLessThan(order.indexOf("/settings/groups"));
  });

  it("gates Users and Roles on the same permission", () => {
    render(false);
    expect(hrefs()).not.toContain("/settings/roles");
    render(true);
    expect(hrefs()).toContain("/settings/roles");
  });

  it("marks the active entry when the roster page is open", () => {
    nav.pathname = "/settings/users";
    render(true);
    const active = Array.from(container.querySelectorAll("a")).find(
      (a) => a.getAttribute("href") === "/settings/users",
    );
    expect(active?.className).toContain("font-medium");
  });
});
