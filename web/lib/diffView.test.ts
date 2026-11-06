// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { diffViewBootstrapScript, installDiffViewToggle } from "./diffView";

describe("diff view toggle", () => {
  afterEach(() => {
    document.documentElement.removeAttribute("data-arti-diff-view");
    document.body.innerHTML = "";
    localStorage.clear();
  });

  it("flips every block between unified and side by side and remembers the choice", () => {
    const dispose = installDiffViewToggle();
    document.body.innerHTML = '<div class="arti-diff"><button data-arti-diff-toggle><svg></svg></button></div>';
    const btn = document.querySelector<HTMLElement>("[data-arti-diff-toggle] svg")!;
    btn.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(document.documentElement.getAttribute("data-arti-diff-view")).toBe("split");
    expect(localStorage.getItem("arti.diffView")).toBe("split");
    btn.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(document.documentElement.hasAttribute("data-arti-diff-view")).toBe(false);
    expect(localStorage.getItem("arti.diffView")).toBe("unified");
    dispose();
  });

  it("scrolls by however far the button moved, so it stays under the pointer", () => {
    const dispose = installDiffViewToggle();
    document.body.innerHTML = '<button data-arti-diff-toggle></button>';
    const btn = document.querySelector<HTMLElement>("[data-arti-diff-toggle]")!;
    const tops = [100, 340];
    btn.getBoundingClientRect = () => ({ top: tops.shift() }) as DOMRect;
    const scrollBy = vi.fn();
    document.documentElement.scrollBy = scrollBy;
    btn.click();
    expect(scrollBy).toHaveBeenCalledWith(0, 240);
    dispose();
  });

  it("applies a stored side-by-side choice before first paint", () => {
    localStorage.setItem("arti.diffView", "split");
    new Function(diffViewBootstrapScript())();
    expect(document.documentElement.getAttribute("data-arti-diff-view")).toBe("split");
  });
});
