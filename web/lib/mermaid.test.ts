// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";

let imported = false;
const render = vi.fn();
const initialize = vi.fn();
vi.mock("mermaid", () => {
  imported = true;
  return {
    default: {
      initialize,
      render,
    },
  };
});

import { renderMermaidIn } from "./mermaid";

describe("renderMermaidIn", () => {
  beforeEach(() => {
    imported = false;
    render.mockReset();
  });

  it("does not import Mermaid when there are no placeholders", async () => {
    await renderMermaidIn(document.createElement("div"));
    expect(imported).toBe(false);
  });

  it("renders each placeholder once", async () => {
    render.mockResolvedValue({
      svg: '<svg><style>.node{fill:red}</style><text>Label</text><script>alert(1)</script></svg>',
    });
    const container = document.createElement("div");
    container.innerHTML =
      '<div class="mermaid" data-mermaid-placeholder><pre>flowchart TD\nA --> B</pre></div>';
    await renderMermaidIn(container);
    await renderMermaidIn(container);
    expect(render).toHaveBeenCalledTimes(1);
    expect(initialize).toHaveBeenCalledTimes(1);
    expect(initialize.mock.calls[0][0]).toMatchObject({ suppressErrorRendering: true });
    expect(container.querySelector("svg")).not.toBeNull();
    expect(container.querySelector("style")?.textContent).toContain(".node");
    expect(container.querySelector("text")?.textContent).toBe("Label");
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("[data-mermaid-rendered]")).not.toBeNull();
  });

  it("renders placeholders inserted between passes", async () => {
    render.mockResolvedValue({ svg: "<svg><text>new</text></svg>" });
    const container = document.createElement("div");
    await renderMermaidIn(container);
    container.innerHTML =
      '<div class="mermaid" data-mermaid-placeholder><pre>flowchart TD\nA --> B</pre></div>';
    await renderMermaidIn(container);
    expect(render).toHaveBeenCalledTimes(1);
    expect(container.querySelector("text")?.textContent).toBe("new");
  });

  it("deduplicates overlapping passes and cleans up failed attempts", async () => {
    let finish!: (result: { svg: string }) => void;
    render.mockImplementation((id: string) => {
      const orphan = document.createElement("div");
      orphan.id = `d${id}`;
      document.body.append(orphan);
      return new Promise<{ svg: string }>((resolve) => {
        finish = resolve;
      });
    });
    const container = document.createElement("div");
    container.innerHTML =
      '<div class="mermaid" data-mermaid-placeholder><pre>flowchart TD\nA --> B</pre></div>';
    const first = renderMermaidIn(container);
    await Promise.resolve();
    const second = renderMermaidIn(container);
    await Promise.resolve();
    expect(render).toHaveBeenCalledTimes(1);
    finish({ svg: "<svg><text>once</text></svg>" });
    await Promise.all([first, second]);
    expect(initialize).toHaveBeenCalledTimes(1);
    expect(container.querySelectorAll("svg")).toHaveLength(1);
    expect(document.querySelector('[id^="darti-mermaid-"]')).toBeNull();
  });

  it("keeps source and adds a compact error when rendering fails", async () => {
    render.mockImplementation((id: string) => {
      const orphan = document.createElement("div");
      orphan.id = `d${id}`;
      orphan.innerHTML = "<svg>mermaid error</svg>";
      document.body.append(orphan);
      return Promise.reject(new Error("bad syntax"));
    });
    const container = document.createElement("div");
    container.innerHTML =
      '<div class="mermaid" data-mermaid-placeholder><pre>not valid</pre></div>';
    await expect(renderMermaidIn(container)).resolves.toBeUndefined();
    expect(container.querySelector("pre")?.textContent).toBe("not valid");
    expect(container.querySelector("[data-mermaid-error]")?.textContent).toContain("bad syntax");
    expect(document.querySelector('[id^="darti-mermaid-"]')).toBeNull();
    expect(document.querySelector('svg')).toBeNull();
    expect(render).toHaveBeenCalledTimes(1);
    await renderMermaidIn(container);
    expect(render).toHaveBeenCalledTimes(1);
  });

  it("retries an attempted block only when its source changes", async () => {
    render.mockRejectedValue(new Error("bad syntax"));
    const container = document.createElement("div");
    container.innerHTML =
      '<div class="mermaid" data-mermaid-placeholder><pre>not valid</pre></div>';
    await renderMermaidIn(container);
    container.querySelector("pre")!.textContent = "flowchart TD\nA --> B";
    render.mockResolvedValue({ svg: "<svg><text>fixed</text></svg>" });
    await renderMermaidIn(container);
    expect(render).toHaveBeenCalledTimes(2);
  });
});
