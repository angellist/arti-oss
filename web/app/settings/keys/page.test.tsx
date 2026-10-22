// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import ApiKeysPage from "./page";

const future = "2999-01-01T00:00:00Z";
const myKeys = [
  { id: "a", name: "devin-helios", owner_email: "me@x.com", key_prefix: "arti_upload_a", scopes: ["upload"],
    created_at: "2026-01-01T00:00:00Z", expires_at: future, last_used_at: null, revoked_at: null },
  { id: "b", name: "old-ci", owner_email: "me@x.com", key_prefix: "arti_upload_b", scopes: ["upload"],
    created_at: "2026-01-02T00:00:00Z", expires_at: future, last_used_at: null, revoked_at: "2026-02-01T00:00:00Z" },
];

const createApiKey = vi.fn(async () => ({ key: "arti_upload_secret123" }));

vi.mock("@/lib/arti", () => ({
  getMe: async () => null,
  listApiKeys: async () => myKeys,
  getCredentialUsage: async () => ({ sources: [], docs: {} }),
  createApiKey: (...args: unknown[]) => createApiKey(...(args as [])),
  revokeApiKey: async () => {},
  hasPerm: () => false,
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function buttonByText(text: string): HTMLButtonElement | undefined {
  return Array.from(document.querySelectorAll("button")).find((b) => b.textContent?.includes(text));
}

async function click(el: Element) {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

describe("new API key dialog", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    createApiKey.mockClear();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<ApiKeysPage />);
    });
    await click(buttonByText("Create key")!);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  it("does not create a key until Generate, and Cancel closes without one", async () => {
    expect(createApiKey).not.toHaveBeenCalled();
    expect(buttonByText("Generate")!.disabled).toBe(true);

    await click(buttonByText("Cancel")!);
    expect(createApiKey).not.toHaveBeenCalled();
    expect(container.querySelector("#key-ttl")).toBeNull();
  });

  it("generates with the chosen name and expiry and needs a second click to close", async () => {
    const input = container.querySelector<HTMLInputElement>("#key-name")!;
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(input, " ci-upload ");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const ttl = container.querySelector<HTMLSelectElement>("#key-ttl")!;
    await act(async () => {
      ttl.value = "365";
      ttl.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await click(buttonByText("Generate")!);
    expect(createApiKey).toHaveBeenCalledWith("ci-upload", ["upload"], 365);
    expect(container.textContent).toContain("arti_upload_secret123");
    expect(buttonByText("Cancel")).toBeUndefined();

    const now = vi.spyOn(Date, "now").mockReturnValue(1_000);
    await click(buttonByText("Done")!);
    expect(container.textContent).toContain("arti_upload_secret123");

    now.mockReturnValue(2_000);
    await click(buttonByText("I have copied this key")!);
    now.mockRestore();
    expect(container.textContent).not.toContain("arti_upload_secret123");
    await act(async () => {
      await new Promise((r) => requestAnimationFrame(r));
    });
    expect(document.activeElement).toBe(buttonByText("Create key"));
  });
});

describe("new API key dialog focus and double-click", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<ApiKeysPage />);
    });
    await click(buttonByText("Create key")!);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  it("moves focus into the dialog, names it, and ignores a quick second activation of Done", async () => {
    const input = container.querySelector<HTMLInputElement>("#key-name")!;
    expect(document.activeElement).toBe(input);

    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(input, "ci");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(buttonByText("Generate")!);
    expect(document.activeElement).toBe(buttonByText("Copy"));
    const done = buttonByText("Done")!;

    await click(done);
    await click(done);
    expect(container.textContent).toContain("arti_upload_secret123");
    expect(buttonByText("I have copied this key")).toBe(done);
    const dialog = container.querySelector("[role=dialog]")!;
    expect(document.getElementById(dialog.getAttribute("aria-labelledby")!)?.textContent).toContain(
      "API key created",
    );
  });
});

describe("my keys filters", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<ApiKeysPage />);
    });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  it("shows active keys by default and filters by status and search", async () => {
    expect(container.textContent).toContain("devin-helios");
    expect(container.textContent).not.toContain("old-ci");

    await click(buttonByText("Revoked")!);
    expect(container.textContent).toContain("old-ci");
    expect(container.textContent).not.toContain("devin-helios");

    await click(buttonByText("All")!);
    const search = container.querySelector<HTMLInputElement>("#key-search")!;
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      setValue.call(search, "old");
      search.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(container.textContent).toContain("old-ci");
    expect(container.textContent).not.toContain("devin-helios");
  });
});
