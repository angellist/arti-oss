// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RAIL_RIGHT, RAIL_WIDTH } from "@/lib/commentsGeometry";
import FullPageExit from "./FullPageExit";

// The one call the component makes to leave the SPA. Mocked rather than spied
// on: jsdom's location.assign is non-configurable (see lib/navigate).
const nav = vi.hoisted(() => ({ urls: [] as string[] }));
vi.mock("@/lib/navigate", () => ({
  hardNavigate: (url: string) => nav.urls.push(url),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("FullPageExit", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    nav.urls.length = 0;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    delete document.documentElement.dataset.theme;
    vi.restoreAllMocks();
  });

  const render = () => {
    act(() => root.render(<FullPageExit />));
    return container.querySelector("button");
  };

  it("renders one labelled control in a top-level window", () => {
    const btn = render();
    expect(btn, "the chrome-less view's only affordance").not.toBeNull();
    expect(btn!.getAttribute("aria-label")).toBe("back to normal view");
    // Offset below the stale-version strip, not pinned to the viewport top.
    expect(btn!.getAttribute("style")).toContain("--arti-top-strip");
  });

  it("sizes and insets the bubble from the comments rail's constants", () => {
    const btn = render()!;
    expect(btn.style.width).toBe(`${RAIL_WIDTH}px`);
    expect(btn.style.height).toBe(`${RAIL_WIDTH}px`);
    // Inset past the rail's own, so the bubble cannot land on the content
    // frame's scrollbar: 15px is the widest classic scrollbar we see.
    expect(Number.parseInt(btn.style.right, 10)).toBeGreaterThan(RAIL_RIGHT + 15);
    expect(btn.style.borderRadius, "a full cap, like the rail's").toBe(`${RAIL_WIDTH / 2}px`);
    expect(btn.style.top).toBe(`calc(var(--arti-top-strip, 0px) + ${RAIL_RIGHT}px)`);
  });

  it("asks the frames it embeds for the page theme on mount", () => {
    const frame = document.createElement("iframe");
    document.body.append(frame);
    const posted: unknown[] = [];
    vi.spyOn(frame.contentWindow!, "postMessage").mockImplementation((d: unknown) => posted.push(d));

    render();

    // The overlay's own report can land before this component exists, so the
    // handshake has to work from either side.
    expect(posted).toEqual([{ source: "arti-theme-query" }]);
    frame.remove();
  });

  // arti renders markdown, text and JSON full-page itself, so the surface under
  // this bubble is dark whenever the reader's theme is — with no frame to
  // report it.
  it("takes the app theme when no frame reports one", () => {
    document.documentElement.dataset.theme = "dark";
    expect(render()!.className, "the rail's dark capsule surface").toContain("rgba(32,32,30,.78)");
  });

  it("lets a frame's report override the app theme", () => {
    document.documentElement.dataset.theme = "dark";
    const btn = render()!;
    const frame = document.createElement("iframe");
    document.body.append(frame);
    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", { data: { source: "arti-theme", dark: false }, source: frame.contentWindow }),
      );
    });
    expect(btn.className, "the light capsule, matching the light page it covers").toContain("bg-white/[.72]");
    frame.remove();
  });

  it("flips to the dark surface when the content frame reports a dark page", () => {
    const btn = render()!;
    const light = btn.className;
    const frame = document.createElement("iframe");
    document.body.append(frame);
    const post = (data: unknown, source: Window | null) =>
      act(() => window.dispatchEvent(new MessageEvent("message", { data, source })));

    post({ source: "arti-theme", dark: true }, frame.contentWindow);
    expect(btn.className, "the rail's dark capsule surface").toContain("rgba(32,32,30,.78)");

    post({ source: "arti-theme", dark: false }, frame.contentWindow);
    expect(btn.className).toBe(light);

    // Not a frame this document embeds: our own window, then a frame nested
    // inside the content frame.
    post({ source: "arti-theme", dark: true }, window);
    expect(btn.className).toBe(light);
    const nested = frame.contentWindow!.document.createElement("iframe");
    frame.contentWindow!.document.body.append(nested);
    post({ source: "arti-theme", dark: true }, nested.contentWindow);
    expect(btn.className).toBe(light);

    post({ source: "arti-file-nav", path: "a.html" }, frame.contentWindow);
    expect(btn.className).toBe(light);
    frame.remove();
  });

  it("leaves full page by dropping the view param, keeping the rest", () => {
    window.history.replaceState(null, "", "/s/report/3?v=full&file=a.html&ts=sm");
    const btn = render()!;
    act(() => btn.click());
    expect(nav.urls).toEqual(["/s/report/3?file=a.html&ts=sm"]);
  });

  it("reads the URL at click time, so an in-content file navigation is honored", () => {
    window.history.replaceState(null, "", "/s/pkg?v=full&file=one.html");
    const btn = render()!;
    // What FullPageHtmlFrame does when a link inside the package navigates.
    window.history.replaceState(null, "", "/s/pkg?v=full&file=two.html");
    act(() => btn.click());
    expect(nav.urls).toEqual(["/s/pkg?file=two.html"]);
  });

  it("renders nothing when the view is embedded in another page", () => {
    // The store reads window.self === window.top; an embed fails that.
    const spy = vi.spyOn(window, "top", "get").mockReturnValue({} as Window);
    const btn = render();
    expect(btn, "an embedder owns its own chrome").toBeNull();
    spy.mockRestore();
  });
});
