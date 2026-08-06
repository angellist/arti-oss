// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { renderMermaidIn } = vi.hoisted(() => ({
  renderMermaidIn: vi.fn(() => Promise.resolve()),
}));

vi.mock("@/lib/mermaid", () => ({ renderMermaidIn }));

import MarkdownBody from "./MarkdownBody";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("MarkdownBody Mermaid scheduling", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    renderMermaidIn.mockClear();
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    root.unmount();
    container.remove();
    vi.useRealTimers();
  });

  it("renders immediately for viewer updates, including a second artifact", async () => {
    await act(async () => {
      root.render(<MarkdownBody body={"```mermaid\nflowchart TD\nA --> B\n```"} />);
    });
    expect(renderMermaidIn).toHaveBeenCalledTimes(1);

    await act(async () => {
      root.render(<MarkdownBody body={"```mermaid\nflowchart TD\nB --> C\n```"} />);
    });
    expect(renderMermaidIn).toHaveBeenCalledTimes(2);
  });

  it("debounces only when explicitly enabled for editor preview", async () => {
    vi.useFakeTimers();
    await act(async () => {
      root.render(<MarkdownBody debounceMermaid body={"```mermaid\nflowchart TD\nA --> B\n```"} />);
    });
    expect(renderMermaidIn).not.toHaveBeenCalled();

    await act(async () => {
      vi.advanceTimersByTime(200);
    });
    expect(renderMermaidIn).toHaveBeenCalledTimes(1);
  });
});
