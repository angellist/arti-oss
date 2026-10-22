// Dictation across the served-HTML iframe. The iframe is an opaque origin that
// runs the artifact author's scripts, so it never gets the microphone: the
// comment box inside it asks the parent (arti's own page) to record, and the
// parent posts the transcript back. The author's scripts can post these
// messages too, so a "start" only asks: the mic opens on a click on the
// parent's own pill, which the framed page can neither hide nor click.
import { canRecord, createLocalDictation, formatElapsed, MAX_SECONDS, WARN_SECONDS, type Dictation, type DictationHandlers } from "./dictation";

const SRC = "arti-dictate";

type ToParent = { source: typeof SRC; op: "hello" } | { source: typeof SRC; op: "start" | "stop" | "cancel"; id: number };
type ToFrame =
  | { source: typeof SRC; op: "ready" }
  | { source: typeof SRC; op: "state"; id: number; state: "idle" | "recording" | "working" }
  | { source: typeof SRC; op: "level"; id: number; v: number }
  | { source: typeof SRC; op: "seconds"; id: number; s: number }
  | { source: typeof SRC; op: "text"; id: number; text: string }
  | { source: typeof SRC; op: "error"; id: number; message: string };

// connectParentDictation — the framed side. Resolves once the parent answers
// the handshake; never resolves under a parent that is not arti (a third-party
// embedder), so the box simply has no mic there.
export function connectParentDictation(): Promise<Dictation> {
  return new Promise((resolve) => {
    if (window.parent === window) return;
    const post = (m: ToParent) => window.parent.postMessage(m, "*");
    let id = 0;
    let handlers: DictationHandlers | null = null;
    const d: Dictation = {
      start(h) {
        handlers = h;
        post({ source: SRC, op: "start", id: ++id });
      },
      stop() {
        post({ source: SRC, op: "stop", id });
      },
      cancel() {
        post({ source: SRC, op: "cancel", id });
        handlers = null;
      },
    };
    window.addEventListener("message", (e: MessageEvent) => {
      if (e.source !== window.parent) return;
      const m = e.data as ToFrame | null;
      if (!m || m.source !== SRC) return;
      if (m.op === "ready") return resolve(d);
      if (m.id !== id || !handlers) return;
      if (m.op === "state") handlers.onState(m.state);
      else if (m.op === "level") handlers.onLevel(m.v);
      else if (m.op === "seconds") handlers.onSeconds?.(m.s);
      else if (m.op === "text") handlers.onText(m.text);
      else if (m.op === "error") handlers.onError(m.message);
    });
    post({ source: SRC, op: "hello" });
  });
}

const PILL_CSS =
  "position:fixed;left:50%;bottom:20px;transform:translateX(-50%);z-index:2147483647;display:flex;align-items:center;gap:10px;" +
  "padding:7px 8px 7px 14px;border-radius:999px;background:rgba(22,22,20,.92);color:#fff;font:500 12.5px Inter,system-ui,sans-serif;" +
  "box-shadow:0 8px 24px -8px rgba(0,0,0,.5)";

// hostDictation — the parent side, for one iframe. Records on the frame's
// behalf and answers only that frame's window. Returns the teardown.
export function hostDictation(
  frame: () => HTMLIFrameElement | null,
  d: Dictation = createLocalDictation(),
): () => void {
  if (!canRecord()) return () => {};
  const target = () => frame()?.contentWindow ?? null;
  const post = (m: ToFrame) => target()?.postMessage(m, "*");
  let pill: HTMLElement | null = null;
  let asking: number | null = null; // the frame clip id waiting on the user's Record click
  const hidePill = () => {
    pill?.remove();
    pill = null;
    asking = null;
  };
  const button = (label: string, primary: boolean) =>
    `<button type="button" style="font:inherit;font-weight:600;border:0;border-radius:999px;padding:4px 11px;cursor:pointer;` +
    (primary ? "background:#fff;color:#1c1c1a" : "background:transparent;color:#fff") +
    `">${label}</button>`;
  const mountPill = (html: string) => {
    hidePill();
    pill = document.createElement("div");
    pill.setAttribute("role", "status");
    pill.style.cssText = PILL_CSS;
    pill.innerHTML = html;
    document.body.appendChild(pill);
    return pill;
  };
  const showRecording = () => {
    const p = mountPill(
      '<span style="width:8px;height:8px;border-radius:50%;background:#ef4444"></span>' +
        `<span>Recording a comment · <b data-t style="font-weight:600;font-variant-numeric:tabular-nums">0:00</b> / ${formatElapsed(MAX_SECONDS)}</span>` +
        button("Stop", true),
    );
    p.querySelector("button")!.onclick = () => d.stop();
  };
  const showSeconds = (s: number) => {
    const el = pill?.querySelector<HTMLElement>("[data-t]");
    if (!el) return;
    el.textContent = formatElapsed(s);
    el.style.color = MAX_SECONDS - s <= WARN_SECONDS ? "#fca5a5" : "";
  };
  const decline = (id: number) => {
    hidePill();
    post({ source: SRC, op: "state", id, state: "idle" });
  };
  const record = (id: number) => {
    hidePill();
    d.start({
      onState: (state) => {
        if (state === "recording") showRecording();
        else hidePill();
        post({ source: SRC, op: "state", id, state });
      },
      onLevel: (v) => post({ source: SRC, op: "level", id, v }),
      onSeconds: (s) => {
        showSeconds(s);
        post({ source: SRC, op: "seconds", id, s });
      },
      onText: (text) => post({ source: SRC, op: "text", id, text }),
      onError: (message) => post({ source: SRC, op: "error", id, message }),
    });
  };
  const ask = (id: number) => {
    const p = mountPill(
      '<span style="width:8px;height:8px;border-radius:50%;background:#ef4444"></span>' +
        "<span>Record a comment?</span>" +
        button("Record", true) +
        button("Cancel", false),
    );
    asking = id;
    const [rec, no] = p.querySelectorAll("button");
    // No focus() here: focus moved into this page would let a keystroke the
    // user meant for the framed page land on Record.
    rec.onclick = () => record(id);
    no.onclick = () => decline(id);
  };

  const onMessage = (e: MessageEvent) => {
    const win = target();
    if (!win || e.source !== win) return;
    const m = e.data as ToParent | null;
    if (!m || m.source !== SRC) return;
    if (m.op === "hello") {
      // A hello is a fresh document in the frame: nothing it did not start may
      // keep the mic, and no transcript may land in it.
      d.cancel();
      hidePill();
      return post({ source: SRC, op: "ready" });
    }
    if (typeof m.id !== "number") return;
    if (asking !== null && m.op !== "start") return decline(asking);
    if (m.op === "stop") return d.stop();
    if (m.op === "cancel") {
      d.cancel();
      return hidePill();
    }
    if (m.op !== "start") return;
    d.cancel();
    ask(m.id);
  };
  window.addEventListener("message", onMessage);
  // The frame's hello may have gone out before this listener existed.
  post({ source: SRC, op: "ready" });
  return () => {
    window.removeEventListener("message", onMessage);
    d.cancel();
    hidePill();
  };
}
