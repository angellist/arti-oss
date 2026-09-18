// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import BlocksManager from "./BlocksManager";
import type { Block } from "@/lib/types";

const calls = vi.hoisted(() => ({
  added: [] as Array<[string, string]>,
  removed: [] as string[],
  matched: [] as string[],
  list: [] as Block[],
  slugless: false,
}));

vi.mock("@/lib/arti", () => ({
  listBlocks: async () => calls.list,
  addBlock: async (pattern: string, reason: string) => {
    calls.added.push([pattern, reason]);
  },
  removeBlock: async (pattern: string) => {
    calls.removed.push(pattern);
  },
  listBlockMatches: async (pattern: string) => {
    calls.matched.push(pattern);
    if (calls.slugless) {
      return [
        {
          artifact_id: "22222222-2222-2222-2222-222222222222",
          named_slug: null,
          version: null,
          title: "Chat upload",
          creator: "seb@example.com",
          artifact_type: "ATTACHMENT",
          content_type: "application/pdf",
          size_bytes: 900,
          created_at: new Date().toISOString(),
        },
      ];
    }
    return [
      {
        artifact_id: "11111111-1111-1111-1111-111111111111",
        named_slug: "q3-board-deck",
        version: 4,
        title: "Q3 board deck",
        creator: "seb@example.com",
        artifact_type: "TEXT",
        content_type: "text/markdown",
        size_bytes: 120,
        created_at: new Date().toISOString(),
      },
    ];
  },
}));

vi.mock("next/link", () => ({
  default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a>,
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const block = (pattern: string): Block => ({
  pattern,
  reason: "shared externally",
  created_by: "admin@example.com",
  created_at: new Date().toISOString(),
});

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  calls.added = [];
  calls.removed = [];
  calls.matched = [];
  calls.list = [];
  calls.slugless = false;
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
});

const render = (initial: Block[]) =>
  act(() => {
    root.render(<BlocksManager initialBlocks={initial} />);
  });

const click = async (el: Element | null | undefined) => {
  await act(async () => {
    el?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
};

const byText = (text: string) =>
  Array.from(container.querySelectorAll("button")).find((b) => b.textContent?.trim() === text);

describe("BlocksManager", () => {
  it("blocks a pattern without asking, because blocking is the emergency action", async () => {
    render([]);
    const input = container.querySelector<HTMLInputElement>('input[aria-label="pattern to block"]')!;
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);

    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, "leaked-doc");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      container.querySelector("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });

    expect(calls.added).toEqual([["leaked-doc", ""]]);
    expect(confirm).not.toHaveBeenCalled();
  });

  // Lifting is what puts a document back in front of everyone, so it is the
  // direction that asks first — and a declined confirm must change nothing.
  it("asks before lifting and does nothing when the admin declines", async () => {
    render([block("leaked-doc")]);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);

    await click(byText("Lift"));
    expect(confirm).toHaveBeenCalled();
    expect(calls.removed).toEqual([]);

    confirm.mockReturnValue(true);
    await click(byText("Lift"));
    expect(calls.removed).toEqual(["leaked-doc"]);
  });

  it("lists what a pattern hides on demand, linking each document to its review page", async () => {
    render([block("mem--couch--*")]);
    expect(calls.matched).toEqual([]);

    await click(byText("mem--couch--*"));

    expect(calls.matched).toEqual(["mem--couch--*"]);
    expect(container.textContent).toContain("Q3 board deck");
    const link = container.querySelector("a[href^='/admin/blocked/']");
    expect(link?.getAttribute("href")).toBe("/admin/blocked/q3-board-deck");
  });

  // A slugless document is blocked by its id, and that id is the only name the
  // review page can reach it by, so the row must still link somewhere.
  it("links a slugless document by its artifact id", async () => {
    calls.slugless = true;
    render([block("11111111-1111-1111-1111-111111111111")]);

    await click(byText("11111111-1111-1111-1111-111111111111"));

    const link = container.querySelector("a[href^='/admin/blocked/']");
    expect(link?.getAttribute("href")).toBe("/admin/blocked/22222222-2222-2222-2222-222222222222");
    expect(container.textContent).toContain("Chat upload");
  });

  it("says so plainly when nothing is blocked", () => {
    render([]);
    expect(container.textContent).toContain("Nothing is blocked.");
  });
});
