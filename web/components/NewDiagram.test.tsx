// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import NewDiagram from "./NewDiagram";

const push = vi.fn();

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push, refresh: vi.fn() }),
}));

vi.mock("@/lib/arti", () => ({
  createArtifact: vi.fn(),
  getAggregates: () => Promise.resolve({ scopes: [], labels: [] }),
  latestVersionForSlug: vi.fn(),
}));

describe("NewDiagram", () => {
  let container: HTMLDivElement;
  let root: Root;

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    vi.restoreAllMocks();
    push.mockReset();
  });

  it("does not treat an untouched canvas as dirty after suggestions resolve", async () => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);

    await act(async () => {
      root.render(<NewDiagram />);
      await Promise.resolve();
    });

    await act(async () => {
      await Promise.resolve();
    });
    const cancel = Array.from(container.querySelectorAll("button")).find((button) => button.textContent === "Cancel");
    cancel?.click();

    expect(confirm).not.toHaveBeenCalled();
    expect(push).toHaveBeenCalledWith("/");
  });
});
