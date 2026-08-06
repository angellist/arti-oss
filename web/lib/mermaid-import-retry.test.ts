// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";

const { initialize, render, attempts } = vi.hoisted(() => ({
  initialize: vi.fn(),
  render: vi.fn(),
  attempts: { count: 0 },
}));

vi.mock("mermaid", async () => {
  if (attempts.count++ === 0) throw new Error("chunk unavailable");
  return { default: { initialize, render } };
});

import { renderMermaidIn } from "./mermaid";

describe("Mermaid import retry", () => {
  it("retries a failed module load without reinitializing after success", async () => {
    render.mockResolvedValue({ svg: "<svg><text>retried</text></svg>" });
    const container = document.createElement("div");
    container.innerHTML =
      '<div class="mermaid" data-mermaid-placeholder><pre>flowchart TD\nA --> B</pre></div>';

    await renderMermaidIn(container);
    expect(container.querySelector("pre")?.textContent).toContain("flowchart TD");
    expect(initialize).not.toHaveBeenCalled();

    await renderMermaidIn(container);
    expect(container.querySelector("text")?.textContent).toBe("retried");
    expect(initialize).toHaveBeenCalledTimes(1);
  });
});
