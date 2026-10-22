// Voice input for the comment box. The overlay drives a Dictation and never
// touches the microphone itself: in arti's own page the recorder below does the
// work, and in a served HTML page (an opaque-origin iframe that must not get
// the mic) dictationBridge.ts asks the parent page to record instead.

import { errorFrom } from "./arti";

export type DictationState = "idle" | "recording" | "working";

export interface DictationHandlers {
  onState(s: DictationState): void;
  /** Loudness 0..1, a few times a second while recording. */
  onLevel(v: number): void;
  /** Whole seconds recorded so far, once a second while recording. */
  onSeconds?(s: number): void;
  onText(text: string): void;
  onError(message: string): void;
}

// One clip at a time: start() while a clip is live throws the old one away.
// cancel() discards the live clip, recording or transcribing, and reports
// nothing more for it.
export interface Dictation {
  start(h: DictationHandlers): void;
  stop(): void;
  cancel(): void;
}

// spliceAtCaret — dictated text goes in at the caret, keeping what is already
// typed and replacing any selection. Both sides get a separating space when the
// insertion would otherwise run into a word, so dictating into "open |the PR"
// gives "open draft the PR" and not "open draftthe PR". The caret lands after
// the dictated text, before any trailing pad, so speaking twice appends.
export function spliceAtCaret(
  value: string,
  start: number,
  end: number,
  text: string,
): { value: string; caret: number } {
  const before = start > 0 && !/\s$/.test(value.slice(0, start)) ? " " : "";
  const after = end < value.length && !/^\s/.test(value.slice(end)) ? " " : "";
  return {
    value: value.slice(0, start) + before + text + after + value.slice(end),
    caret: start + before.length + text.length,
  };
}

/** Longest single recording. A forgotten microphone would otherwise bill for an hour. */
export const MAX_SECONDS = 300;
/** Seconds left at which the recording clock starts warning. */
export const WARN_SECONDS = 20;

// formatElapsed — a recording clock, m:ss.
export function formatElapsed(seconds: number): string {
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

// startClock — counts the seconds a recording has been running and enforces the
// cap, returning the stop. `onCap` fires exactly once, on the tick that reaches
// MAX_SECONDS, and the ticking ends there.
export function startClock(onSecond: (s: number) => void, onCap: () => void): () => void {
  let elapsed = 0;
  // `clearInterval` from inside the callback is not honoured by every fake-timer
  // implementation, and either way `stop()` must be idempotent.
  let done = false;
  const id = setInterval(() => {
    if (done) return;
    onSecond((elapsed += 1));
    if (elapsed < MAX_SECONDS) return;
    done = true;
    clearInterval(id);
    onCap();
  }, 1000);
  return () => {
    done = true;
    clearInterval(id);
  };
}

/** Whether this page can record at all: getUserMedia needs a secure context. */
export function canRecord(): boolean {
  return (
    typeof window !== "undefined" &&
    window.isSecureContext &&
    !!navigator.mediaDevices?.getUserMedia &&
    typeof MediaRecorder !== "undefined"
  );
}

const SAMPLE_MS = 60;
// Speech sits well under full scale, so the RMS is gained up to fill the meter.
const LEVEL_GAIN = 3.2;

function startMeter(stream: MediaStream, onLevel: (v: number) => void): () => void {
  const audio = new AudioContext();
  const analyser = audio.createAnalyser();
  analyser.fftSize = 1024;
  audio.createMediaStreamSource(stream).connect(analyser);
  const buf = new Uint8Array(analyser.fftSize);
  let raf = 0;
  let last = 0;
  const draw = (t: number) => {
    raf = requestAnimationFrame(draw);
    if (t - last < SAMPLE_MS) return;
    last = t;
    analyser.getByteTimeDomainData(buf);
    let sum = 0;
    for (let i = 0; i < buf.length; i++) {
      const d = (buf[i] - 128) / 128;
      sum += d * d;
    }
    onLevel(Math.min(1, Math.sqrt(sum / buf.length) * LEVEL_GAIN));
  };
  raf = requestAnimationFrame(draw);
  return () => {
    cancelAnimationFrame(raf);
    void audio.close().catch(() => {});
  };
}

// postDictate — one clip in, the transcript out, through arti-server (which
// holds the provider key). Throws on any failure.
export async function postDictate(blob: Blob): Promise<string> {
  const res = await fetch("/api/dictate", {
    method: "POST",
    body: blob,
    headers: { "Content-Type": blob.type || "audio/webm" },
    credentials: "same-origin",
  });
  if (!res.ok) throw await errorFrom(res);
  const body = (await res.json().catch(() => ({}))) as { text?: string };
  return body.text ?? "";
}

// createLocalDictation records in THIS page. Every press bumps `gen`, so a
// press superseded while its permission prompt was open takes its own
// microphone back down, and a cancelled clip's late callbacks go nowhere.
export function createLocalDictation(transcribe: (b: Blob) => Promise<string> = postDictate): Dictation {
  let gen = 0;
  let live: { rec: MediaRecorder; discard: () => void } | null = null;
  // Set while getUserMedia is pending, so a stop in that window can still win.
  let opening: DictationHandlers | null = null;
  const cancel = () => {
    gen += 1;
    live?.discard();
    live = null;
    opening = null;
  };
  return {
    cancel,
    stop() {
      // The recorder goes inactive the instant stop() is called, while onstop is
      // still pending: a second stop in that gap would throw.
      if (live?.rec.state === "recording") return live.rec.stop();
      const h = opening;
      if (!h) return;
      cancel();
      h.onState("idle");
    },
    start(h) {
      cancel();
      const mine = gen;
      const alive = () => mine === gen;
      opening = h;
      void (async () => {
        let stream: MediaStream;
        try {
          stream = await navigator.mediaDevices.getUserMedia({ audio: true });
        } catch {
          if (!alive()) return;
          opening = null;
          h.onError("Microphone blocked — allow access in your browser to dictate.");
          h.onState("idle");
          return;
        }
        if (!alive()) return stream.getTracks().forEach((t) => t.stop());
        opening = null;
        const chunks: Blob[] = [];
        let rec: MediaRecorder;
        let stopMeter: () => void;
        try {
          rec = new MediaRecorder(stream);
          stopMeter = startMeter(stream, (v) => alive() && h.onLevel(v));
        } catch {
          stream.getTracks().forEach((t) => t.stop());
          h.onError("Could not start recording in this browser.");
          h.onState("idle");
          return;
        }
        let released = false;
        const stopClock = startClock((s) => alive() && h.onSeconds?.(s), () => {
          if (rec.state === "recording") rec.stop();
        });
        const release = () => {
          if (released) return;
          released = true;
          stopClock();
          stopMeter();
          stream.getTracks().forEach((t) => t.stop());
        };
        let discarded = false;
        rec.ondataavailable = (e) => chunks.push(e.data);
        rec.onstop = () => {
          release();
          if (discarded || !alive()) return;
          live = null;
          h.onState("working");
          transcribe(new Blob(chunks, { type: rec.mimeType }))
            .then((text) => {
              if (alive() && text) h.onText(text);
            })
            .catch((e: Error) => {
              if (alive()) h.onError(`Could not transcribe — ${e.message}. Your text is unchanged.`);
            })
            .finally(() => {
              if (alive()) h.onState("idle");
            });
        };
        rec.start();
        live = {
          rec,
          discard: () => {
            discarded = true;
            if (rec.state === "recording") rec.stop();
            else release();
          },
        };
        h.onState("recording");
      })();
    },
  };
}
