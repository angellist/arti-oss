// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it, vi } from "vitest";

const hostDictation = vi.fn(() => () => {});
vi.mock("./dictationBridge", () => ({ hostDictation }));
const getMe = vi.fn(async () => ({ email: "a@x.com", dictation_enabled: false }));
vi.mock("./arti", () => ({ getMe }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("useDictationBridge", () => {
  it("never arms the parent recorder on a deployment without dictation", async () => {
    const { useDictationBridge } = await import("./useDictationBridge");
    function Probe() {
      useDictationBridge({ current: null });
      return null;
    }
    const root = createRoot(document.createElement("div"));
    await act(async () => root.render(<Probe />));
    await act(async () => new Promise((r) => setTimeout(r, 0)));
    expect(hostDictation).not.toHaveBeenCalled();
    act(() => root.unmount());
  });

  it("retries after a failed lookup instead of staying off for the tab", async () => {
    vi.resetModules();
    getMe.mockRejectedValueOnce(new Error("blip")).mockResolvedValueOnce({ email: "a@x.com", dictation_enabled: true });
    hostDictation.mockClear();
    const { useDictationBridge } = await import("./useDictationBridge");
    function Probe() {
      useDictationBridge({ current: null });
      return null;
    }
    const mountOnce = async () => {
      const root = createRoot(document.createElement("div"));
      await act(async () => root.render(<Probe />));
      await act(async () => new Promise((r) => setTimeout(r, 0)));
      act(() => root.unmount());
    };
    await mountOnce();
    expect(hostDictation).not.toHaveBeenCalled();
    await mountOnce();
    expect(hostDictation).toHaveBeenCalledTimes(1);
  });
});
