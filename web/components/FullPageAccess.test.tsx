// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import FullPageAccess from "./FullPageAccess";
import type { ArtifactInfo } from "@/lib/types";

const listShares = vi.fn(async () => [] as unknown[]);

vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh: () => {} }) }));
vi.mock("@/lib/arti", () => ({
  listGroups: async () => [],
  listIdpGroups: async () => [],
  MIN_PEOPLE_QUERY: 2,
  searchPeople: async () => [],
  updateArtifactAccess: async () => ({}),
  transferArtifactOwner: async () => ({}),
  listShares: (...a: unknown[]) => listShares(...(a as [])),
  sameEmail: (a: string, b: string) => a.toLowerCase() === b.toLowerCase(),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const info = (over: Partial<ArtifactInfo> = {}) =>
  ({
    artifact_id: "a1",
    named_slug: "report",
    allowed_access: ["*"],
    allowed_write: null,
    owner: "o@x.com",
    can_manage_comments: false,
    can_share: false,
    ...over,
  }) as ArtifactInfo;

describe("FullPageAccess", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    listShares.mockClear();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
  });

  const render = (i: ArtifactInfo) => {
    act(() => root.render(<FullPageAccess info={i} me={{ email: "o@x.com", name: "", is_admin: false } as never} />));
    return container.querySelector("button");
  };

  it("sits immediately left of the ✕ at the same size", () => {
    const btn = render(info())!;
    expect(btn.style.width).toBe("18px");
    expect(btn.style.height).toBe("18px");
    expect(btn.style.right).toBe("18px");
    expect(btn.style.top).toBe("var(--arti-top-strip, 0px)");
  });

  it("colours the dot by read-access tier", () => {
    expect(render(info({ allowed_access: ["*"] }))!.innerHTML).toContain("bg-emerald-400");
    expect(render(info({ allowed_access: ["*@x.com"] }))!.innerHTML).toContain("bg-amber-400");
    expect(render(info({ allowed_access: [] }))!.innerHTML).toContain("bg-rose-500");
  });

  it("opens an editable panel for the owner", () => {
    const btn = render(info({ can_manage_comments: true, can_share: true }))!;
    act(() => btn.click());
    expect(document.body.textContent).toContain("Edit access");
    expect(listShares).toHaveBeenCalledWith("a1");
  });

  it("opens a read-only panel for a reader", () => {
    const btn = render(info())!;
    expect(btn.getAttribute("aria-label")).toMatch(/^access:/);
    act(() => btn.click());
    const heading = Array.from(document.body.querySelectorAll("h2")).map((h) => h.textContent);
    expect(heading).toContain("Access");
    expect(listShares).not.toHaveBeenCalled();
  });

  it("stacks the panel above the corner controls and the stale-version strip", () => {
    const btn = render(info())!;
    act(() => btn.click());
    const overlay = document.body.querySelector('[class*="bg-black/40"]')!;
    expect(overlay.className).toContain("z-[80]");
  });

  it("renders nothing when embedded", () => {
    const spy = vi.spyOn(window, "top", "get").mockReturnValue({} as Window);
    expect(render(info())).toBeNull();
    spy.mockRestore();
  });
});
