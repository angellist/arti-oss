// @vitest-environment jsdom

import { act } from "react";
import { createRoot, hydrateRoot, type Root } from "react-dom/client";
import { renderToString } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import FullPageHtmlFrame from "./FullPageHtmlFrame";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("FullPageHtmlFrame", () => {
  let container: HTMLDivElement;
  let root: Root;
  const src = "https://arti.example/raw/abc?ctx=fullpage";
  const frameSrc = () => container.querySelector("iframe")!.getAttribute("src");

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    window.history.replaceState(null, "", "/");
  });

  it("opens the framed document at the page's #fragment, so a link can point at one slide", () => {
    window.history.replaceState(null, "", "/s/deck?v=full#50");
    act(() => root.render(<FullPageHtmlFrame src={src} title="deck" />));
    expect(frameSrc()).toBe(src + "#50");
  });

  it("server-renders no src, so the only load is the one that carries the #fragment", () => {
    window.history.replaceState(null, "", "/s/deck?v=full#50");
    const html = renderToString(<FullPageHtmlFrame src={src} title="deck" />);
    expect(html).not.toMatch(/ src=/);
    expect(html).toContain(`data-src="${src.replace("&", "&amp;")}"`);
    act(() => root.unmount());
    container.innerHTML = html;
    act(() => {
      root = hydrateRoot(container, <FullPageHtmlFrame src={src} title="deck" />);
    });
    expect(frameSrc()).toBe(src + "#50");
  });

  it("keeps the first #fragment after in-frame navigation clears it and the frame re-renders", () => {
    window.history.replaceState(null, "", "/s/pkg?v=full#50");
    act(() => root.render(<FullPageHtmlFrame src={src} title="pkg" />));
    window.history.replaceState(null, "", "/s/pkg?v=full&file=two.html");
    act(() => root.render(<FullPageHtmlFrame src={src} title="pkg" zoom={1.2} />));
    expect(frameSrc()).toBe(src + "#50");
  });

  it("keeps srcDoc when there is no src to carry a fragment", () => {
    window.history.replaceState(null, "", "/s/deck?v=full#3");
    act(() => root.render(<FullPageHtmlFrame srcDoc="<p>hi</p>" title="doc" />));
    const iframe = container.querySelector("iframe")!;
    expect(iframe.getAttribute("src")).toBeNull();
    expect(iframe.getAttribute("srcdoc")).toBe("<p>hi</p>");
  });
});
