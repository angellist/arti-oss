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

describe("MarkdownBody frontmatter", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    root.unmount();
    container.remove();
  });

  it("renders frontmatter as a disclosure that starts closed but keeps the text", async () => {
    await act(async () => {
      root.render(<MarkdownBody body={"---\nid: DD-0057\n---\n\n# Body\n"} />);
    });
    const details = container.querySelector("details");
    expect(details).not.toBeNull();
    // Closed by default: the YAML must not push the document off screen…
    expect(details!.open).toBe(false);
    // …but it stays in the DOM so find-in-page and copy still reach it.
    expect(details!.querySelector("pre")?.textContent).toContain("id: DD-0057");
    expect(details!.querySelector("summary")?.textContent).toContain("metadata");
    expect(container.querySelector("h1")?.textContent).toBe("Body");
  });

  it("does not carry an expanded block onto the next document", async () => {
    // The viewer stays mounted across artifact→artifact and package file→file
    // navigation, so <details> open state would otherwise survive the change of
    // document (the block would be expanded before the reader asked).
    const render = (docKey: string, id: string) =>
      act(async () => {
        root.render(<MarkdownBody docKey={docKey} body={`---\nid: ${id}\n---\n\n# Body\n`} />);
      });

    await render("art-1", "DD-1");
    container.querySelector("details")!.open = true; // reader expands it

    await render("art-2", "DD-2");
    expect(container.querySelector("details")!.open).toBe(false);
    expect(container.querySelector("pre")?.textContent).toContain("id: DD-2");
  });

  it("keeps the block open while the same document is edited", async () => {
    // The editor preview re-renders on every keystroke; keying on the frontmatter
    // text instead of the document would close the block as it's being edited.
    const render = (id: string) =>
      act(async () => {
        root.render(<MarkdownBody docKey="art-1" body={`---\nid: ${id}\n---\n\n# Body\n`} />);
      });

    await render("DD-1");
    container.querySelector("details")!.open = true;

    await render("DD-12");
    expect(container.querySelector("details")!.open).toBe(true);
  });
});
