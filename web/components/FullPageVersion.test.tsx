// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import FullPageVersion from "./FullPageVersion";
import type { ArtifactInfo } from "@/lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const info = (over: Partial<ArtifactInfo> = {}) =>
  ({ artifact_id: "a1", named_slug: "q3 report", version: 7, ...over }) as ArtifactInfo;

describe("FullPageVersion", () => {
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
    vi.restoreAllMocks();
  });

  const render = (i: ArtifactInfo) => {
    act(() => root.render(<FullPageVersion info={i} />));
    return container.querySelector("a");
  };

  it("links the version number to the slug's version list", () => {
    const a = render(info())!;
    expect(a.textContent).toBe("v7");
    expect(a.getAttribute("href")).toBe("/?slug=q3%20report&order_by=version&order_dir=desc");
  });

  it("sits right of the ✕ and the access dot at their height", () => {
    const a = render(info())!;
    expect(a.style.left).toBe("36px");
    expect(a.style.height).toBe("18px");
    expect(a.style.top).toBe("var(--arti-top-strip, 0px)");
  });

  it("renders nothing for an unnamed document", () => {
    expect(render(info({ named_slug: null, version: null }))).toBeNull();
    expect(render(info({ version: null }))).toBeNull();
  });

  it("renders nothing when embedded", () => {
    vi.spyOn(window, "top", "get").mockReturnValue({} as Window);
    expect(render(info())).toBeNull();
  });
});
