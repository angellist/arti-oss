// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { mountCommentsOverlay, type CommentsApi } from "./commentsOverlay";
import type { ThreadDTO } from "./arti";

class TestResizeObserver {
  observe() {}
  disconnect() {}
}

const thread = (anchor: ThreadDTO["anchor"], id: string): ThreadDTO => ({
  id,
  anchor,
  status: "open",
  created_by: "test@example.com",
  created_at: "2026-01-01T00:00:00Z",
  comments: [
    {
    id: `${id}-comment`,
      author: "test@example.com",
      author_name: "Test",
      body: "Test comment",
      created_at: "2026-01-01T00:00:00Z",
    },
  ],
});

describe("comments overlay reflow", () => {
  afterEach(() => {
    document.documentElement.innerHTML = "";
    vi.restoreAllMocks();
  });

  it("repositions text cards and wires rendered Mermaid media after asynchronous replacement", async () => {
    vi.stubGlobal("ResizeObserver", TestResizeObserver);
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 0;
    });
    let markTop = 120;
    const doc = document.createElement("article");
    doc.dataset.artiDoc = "";
    doc.innerHTML =
      '<p>Before diagram</p><div class="mermaid" data-arti-zoom data-mermaid-placeholder><pre>flowchart TD</pre></div><img src="image.png" alt="Later image"><p>Below diagram</p>';
    document.body.append(doc);
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      if (this.matches("article")) return new DOMRect(0, 0, 800, 1000);
      if (this.matches("mark")) return new DOMRect(0, markTop, 100, 20);
      if (this.matches(".mermaid")) {
        const rendered = this.hasAttribute("data-mermaid-rendered");
        return new DOMRect(100, 300, 400, rendered ? 200 : 20);
      }
      if (this.matches("img")) return new DOMRect(100, 700, 400, 200);
      return new DOMRect(0, 0, 100, 20);
    });

    const api: CommentsApi = {
      list: async () => ({
        threads: [
          thread({ type: "text", quote: "Below diagram" }, "text-thread"),
          thread({ type: "pin", media: 1, rx: 0.5, ry: 0.5 }, "pin-thread"),
        ],
      }),
      create: vi.fn(),
      reply: vi.fn(),
      resolve: vi.fn(),
      reopen: vi.fn(),
      del: vi.fn(),
      edit: vi.fn(),
    };
    const dispose = mountCommentsOverlay({
      container: doc,
      artifactId: "artifact",
      me: { email: "test@example.com", name: "Test", is_admin: false },
      allowPin: true,
      api,
    });
    await Promise.resolve();
    document.querySelector<HTMLElement>("[data-fab='comments']")!.click();
    const card = document.querySelector<HTMLElement>("[data-tid='text-thread']")!;
    const before = card.style.top;
    const pin = document.querySelector<HTMLElement>("[data-pintid='pin-thread']")!;
    const pinBefore = `${pin.style.left},${pin.style.top}`;

    markTop = 420;
    const media = doc.querySelector<HTMLElement>(".mermaid")!;
    media.innerHTML = "<svg><text>Rendered</text></svg>";
    media.dataset.mermaidRendered = "true";
    await new Promise((resolve) => setTimeout(resolve, 300));

    expect(card.style.top).not.toBe(before);
    expect(`${pin.style.left},${pin.style.top}`).toBe(pinBefore);
    media.click();
    expect(document.querySelector(".ac-lb svg text")?.textContent).toBe("Rendered");
    dispose();
  });
});
