import { afterEach, describe, expect, it, vi } from "vitest";
import { createLocalDictation, spliceAtCaret, startClock, formatElapsed, MAX_SECONDS } from "./dictation";

describe("spliceAtCaret", () => {
  it("dictating into an empty box gives the text and no leading space", () => {
    expect(spliceAtCaret("", 0, 0, "rebase this")).toEqual({ value: "rebase this", caret: 11 });
  });

  it("a draft survives being spoken over — text lands at the caret, not on top of it", () => {
    const { value, caret } = spliceAtCaret("Have the coder agent ", 21, 21, "rebase this branch");
    expect(value).toBe("Have the coder agent rebase this branch");
    expect(caret).toBe(value.length);
  });

  it("speaking mid-sentence keeps the tail after the caret, separated from it", () => {
    const { value, caret } = spliceAtCaret("open the PR", 5, 5, "draft");
    expect(value).toBe("open draft the PR");
    expect(caret).toBe(10);
  });

  it("an existing space on either side is not doubled", () => {
    expect(spliceAtCaret("open  the PR", 5, 5, "draft").value).toBe("open draft the PR");
  });

  it("a caret tight against a word gets a separating space, an already-spaced one does not", () => {
    expect(spliceAtCaret("rebase", 6, 6, "this").value).toBe("rebase this");
    expect(spliceAtCaret("rebase ", 7, 7, "this").value).toBe("rebase this");
    expect(spliceAtCaret("rebase\n", 7, 7, "this").value).toBe("rebase\nthis");
  });

  it("a selection is replaced, not pushed aside", () => {
    expect(spliceAtCaret("send it to alice", 11, 16, "bob")).toEqual({ value: "send it to bob", caret: 14 });
  });
});

describe("recording clock", () => {
  afterEach(() => vi.useRealTimers());

  it("reads m:ss, zero-padded, from the first second to the cap", () => {
    expect(formatElapsed(0)).toBe("0:00");
    expect(formatElapsed(65)).toBe("1:05");
    expect(formatElapsed(MAX_SECONDS)).toBe("5:00");
  });

  it("reports every second and caps the clip at five minutes, exactly once", () => {
    vi.useFakeTimers();
    const seen: number[] = [];
    let capped = 0;
    startClock((s) => seen.push(s), () => (capped += 1));
    vi.advanceTimersByTime(2_500);
    expect(seen).toEqual([1, 2]);
    vi.advanceTimersByTime((MAX_SECONDS - 2) * 1000);
    expect(seen.at(-1)).toBe(MAX_SECONDS);
    expect(capped).toBe(1);
    vi.advanceTimersByTime(60_000);
    expect(capped).toBe(1);
    expect(seen.at(-1)).toBe(MAX_SECONDS);
  });

  it("stopped early, never reaches the cap", () => {
    vi.useFakeTimers();
    const seen: number[] = [];
    let capped = 0;
    const stop = startClock((s) => seen.push(s), () => (capped += 1));
    vi.advanceTimersByTime(3_000);
    stop();
    vi.advanceTimersByTime(MAX_SECONDS * 1000);
    expect(seen).toEqual([1, 2, 3]);
    expect(capped).toBe(0);
  });
});

describe("local recorder", () => {
  const handlers = () => ({ onState: vi.fn(), onLevel: vi.fn(), onText: vi.fn(), onError: vi.fn() });
  const fakeStream = () => {
    const track = { stop: vi.fn() };
    return { track, stream: { getTracks: () => [track] } as unknown as MediaStream };
  };
  afterEach(() => vi.unstubAllGlobals());

  it("a stop pressed during the permission prompt keeps the mic from opening", async () => {
    const { track, stream } = fakeStream();
    let grant!: (s: MediaStream) => void;
    vi.stubGlobal("navigator", { mediaDevices: { getUserMedia: () => new Promise((r) => (grant = r)) } });
    const Rec = vi.fn();
    vi.stubGlobal("MediaRecorder", Rec);
    const h = handlers();
    const d = createLocalDictation();
    d.start(h);
    d.stop();
    expect(h.onState).toHaveBeenLastCalledWith("idle");
    grant(stream);
    await new Promise((r) => setTimeout(r, 0));
    expect(Rec).not.toHaveBeenCalled();
    expect(track.stop).toHaveBeenCalled();
  });

  it("a recorder that fails to start releases the mic and returns to idle", async () => {
    const { track, stream } = fakeStream();
    vi.stubGlobal("navigator", { mediaDevices: { getUserMedia: async () => stream } });
    vi.stubGlobal("MediaRecorder", vi.fn(() => { throw new Error("unsupported"); }));
    const h = handlers();
    createLocalDictation().start(h);
    await new Promise((r) => setTimeout(r, 0));
    expect(track.stop).toHaveBeenCalled();
    expect(h.onError).toHaveBeenCalled();
    expect(h.onState).toHaveBeenLastCalledWith("idle");
  });
});
