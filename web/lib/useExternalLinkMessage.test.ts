import { describe, expect, it, vi } from "vitest";
import { handleExternalLinkMessage } from "./useExternalLinkMessage";

describe("handleExternalLinkMessage", () => {
  it("opens a valid message from the expected frame", () => {
    const contentWindow = {};
    const frame = { contentWindow } as HTMLIFrameElement;
    const open = vi.fn().mockReturnValue(null);
    vi.stubGlobal("window", { open });

    handleExternalLinkMessage(
      { source: contentWindow, data: { source: "arti-open-external", url: "https://example.com/path" } } as MessageEvent,
      frame,
    );

    expect(open).toHaveBeenCalledWith("https://example.com/path", "_blank", "noopener");
    vi.unstubAllGlobals();
  });

  it.each([
    ["wrong frame", { source: {}, data: { source: "arti-open-external", url: "https://example.com" } }],
    ["wrong source field", { source: {}, data: { source: "other", url: "https://example.com" } }],
    ["non-http URL", { source: {}, data: { source: "arti-open-external", url: "javascript:alert(1)" } }],
  ])("ignores %s", (_name, init) => {
    const contentWindow = {};
    const frame = { contentWindow } as HTMLIFrameElement;
    const open = vi.fn().mockReturnValue(null);
    vi.stubGlobal("window", { open });
    const source = _name === "wrong frame" ? init.source : contentWindow;

    handleExternalLinkMessage({ source, data: init.data } as MessageEvent, frame);

    expect(open).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });
});
