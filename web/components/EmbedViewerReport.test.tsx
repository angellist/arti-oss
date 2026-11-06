// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import EmbedViewerReport from "./EmbedViewerReport";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("EmbedViewerReport", () => {
  let container: HTMLDivElement;
  let root: Root;
  const post = vi.fn();

  beforeEach(() => {
    post.mockReset();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  function framedBy(host: unknown, top: unknown) {
    vi.spyOn(window, "parent", "get").mockReturnValue(host as Window);
    vi.spyOn(window, "top", "get").mockReturnValue(top as Window);
  }

  it("posts the shown and latest version to the embedding host", () => {
    const host = { postMessage: post };
    framedBy(host, host);
    act(() => root.render(<EmbedViewerReport slug="design" id="a1" version={3} latestVersion={5} />));
    expect(post).toHaveBeenCalledWith(
      { type: "arti:viewer", slug: "design", id: "a1", version: 3, latest_version: 5 },
      "*",
    );
  });

  it("stays silent in a top-level window", () => {
    framedBy({ postMessage: post }, window);
    act(() => root.render(<EmbedViewerReport slug="design" id="a1" version={3} latestVersion={5} />));
    expect(post).not.toHaveBeenCalled();
  });
});
