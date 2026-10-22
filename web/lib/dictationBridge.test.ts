// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import type { DictationHandlers } from "./dictation";

vi.mock("./dictation", async (orig) => ({ ...(await orig<typeof import("./dictation")>()), canRecord: () => true }));
const { hostDictation } = await import("./dictationBridge");

const setup = () => {
  const frameWin = { postMessage: vi.fn() } as unknown as Window & { postMessage: ReturnType<typeof vi.fn> };
  const d = { h: null as DictationHandlers | null, start: vi.fn((h: DictationHandlers) => { d.h = h; }), stop: vi.fn(), cancel: vi.fn() };
  const dispose = hostDictation(() => ({ contentWindow: frameWin }) as unknown as HTMLIFrameElement, d);
  const from = (source: unknown, data: unknown) => window.dispatchEvent(new MessageEvent("message", { source: source as Window, data }));
  const sent = () => frameWin.postMessage.mock.calls.map((c) => c[0]);
  return { frameWin, d, dispose, from, sent };
};

describe("dictation bridge host", () => {
  afterEach(() => { document.body.innerHTML = ""; });

  it("records only for its own frame", () => {
    const { d, dispose, from } = setup();
    from(window, { source: "arti-dictate", op: "start", id: 1 });
    from({ postMessage() {} }, { source: "arti-dictate", op: "start", id: 1 });
    expect(d.start).not.toHaveBeenCalled();
    dispose();
  });

  const pillButton = (label: string) =>
    [...document.querySelectorAll("button")].find((b) => b.textContent === label)!;

  it("opens the mic only on a click in the parent, never on the frame's message alone", () => {
    const { frameWin, d, dispose, from } = setup();
    from(frameWin, { source: "arti-dictate", op: "start", id: 7 });
    expect(d.start).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("Record a comment?");
    // Focus on Record would let a keystroke meant for the frame press it.
    expect(document.activeElement).not.toBe(pillButton("Record"));
    pillButton("Record").click();
    expect(d.start).toHaveBeenCalledTimes(1);
    dispose();
  });

  it("tells the frame the clip is over when the user declines", () => {
    const { frameWin, d, dispose, from, sent } = setup();
    from(frameWin, { source: "arti-dictate", op: "start", id: 3 });
    pillButton("Cancel").click();
    expect(d.start).not.toHaveBeenCalled();
    expect(sent()).toContainEqual({ source: "arti-dictate", op: "state", id: 3, state: "idle" });
    expect(document.body.textContent).not.toContain("Record a comment?");
    dispose();
  });

  it("withdraws the prompt when the frame stops before the user answers", () => {
    const { frameWin, d, dispose, from, sent } = setup();
    from(frameWin, { source: "arti-dictate", op: "start", id: 4 });
    from(frameWin, { source: "arti-dictate", op: "stop", id: 4 });
    expect(d.start).not.toHaveBeenCalled();
    expect(sent()).toContainEqual({ source: "arti-dictate", op: "state", id: 4, state: "idle" });
    expect(document.querySelector("button")).toBeNull();
    dispose();
  });

  it("shows its own recording pill and posts the transcript back to the frame", () => {
    const { frameWin, d, dispose, from, sent } = setup();
    from(frameWin, { source: "arti-dictate", op: "start", id: 7 });
    pillButton("Record").click();
    d.h!.onState("recording");
    expect(document.body.textContent).toContain("Recording a comment");
    d.h!.onText("hello");
    d.h!.onState("idle");
    expect(sent()).toContainEqual({ source: "arti-dictate", op: "text", id: 7, text: "hello" });
    expect(document.body.textContent).not.toContain("Recording a comment");
    dispose();
  });

  it("shows the recorder's clock against the cap, and relays it to the frame", () => {
    const { frameWin, d, dispose, from, sent } = setup();
    from(frameWin, { source: "arti-dictate", op: "start", id: 5 });
    pillButton("Record").click();
    d.h!.onState("recording");
    d.h!.onSeconds!(42);
    expect(document.body.textContent).toContain("Recording a comment · 0:42 / 5:00");
    expect(sent()).toContainEqual({ source: "arti-dictate", op: "seconds", id: 5, s: 42 });
    dispose();
  });

  it("drops a live clip when the frame loads a new document", () => {
    const { frameWin, d, dispose, from, sent } = setup();
    from(frameWin, { source: "arti-dictate", op: "start", id: 1 });
    pillButton("Record").click();
    d.h!.onState("recording");
    from(frameWin, { source: "arti-dictate", op: "hello" });
    expect(d.cancel).toHaveBeenCalled();
    expect(document.body.textContent).not.toContain("Recording a comment");
    expect(sent().at(-1)).toEqual({ source: "arti-dictate", op: "ready" });
    dispose();
  });
});
