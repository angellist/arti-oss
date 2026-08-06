"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { applyMarkdownAction, type MarkdownAction } from "@/lib/mdtoolbar";
import MarkdownBody from "./MarkdownBody";

// The markdown authoring surface, used both by /new/text and by the viewer's
// Edit action on a markdown artifact.
//
// It edits REAL markdown in a textarea — the bytes you type are the bytes
// stored. The toolbar only rewrites that text (see lib/mdtoolbar.ts), and the
// preview renders through the very same MarkdownBody the viewer uses, so there
// is no serializer to reformat your document and no second renderer to drift
// from what readers will see.

type Mode = "write" | "split" | "preview";

const MODE_KEY = "arti.mdEditorMode";

interface ToolItem {
  action: MarkdownAction;
  label: string;
  title: string;
  // Rendered in the toolbar; a glyph for most, styled text for a few.
  render?: () => React.ReactNode;
}

const GROUPS: ToolItem[][] = [
  [
    { action: "bold", label: "B", title: "Bold  ⌘B" },
    { action: "italic", label: "I", title: "Italic  ⌘I" },
    { action: "code", label: "‹›", title: "Inline code" },
  ],
  [
    { action: "h1", label: "H1", title: "Heading 1" },
    { action: "h2", label: "H2", title: "Heading 2" },
    { action: "h3", label: "H3", title: "Heading 3" },
  ],
  [
    { action: "bullet", label: "•", title: "Bulleted list" },
    { action: "ordered", label: "1.", title: "Numbered list" },
    { action: "quote", label: "❝", title: "Blockquote" },
  ],
  [
    { action: "link", label: "🔗", title: "Link  ⌘K" },
    { action: "codeblock", label: "⌗", title: "Code block" },
    { action: "table", label: "▦", title: "Table" },
    { action: "hr", label: "─", title: "Horizontal rule" },
  ],
];

// Actions reachable by keyboard, so the common ones don't require the mouse.
const SHORTCUTS: Record<string, MarkdownAction> = {
  b: "bold",
  i: "italic",
  k: "link",
};

export default function MarkdownEditor({
  value,
  onChange,
  autoFocus = false,
  disabled = false,
  minHeight = "60vh",
  onModeChange,
}: {
  value: string;
  onChange: (next: string) => void;
  autoFocus?: boolean;
  disabled?: boolean;
  minHeight?: string;
  // Reports the current pane layout (including the restored initial one) so a
  // host can react to it — Split needs the full page width, and the host owns
  // the width control.
  onModeChange?: (mode: Mode) => void;
}) {
  const [mode, setMode] = useState<Mode>("write");
  const taRef = useRef<HTMLTextAreaElement>(null);
  // A selection set by a toolbar action, applied after React has committed the
  // new value. Setting selectionStart/End before that commit would be undone by
  // the re-render, dropping the caret to the end of the document.
  const pendingSel = useRef<{ start: number; end: number } | null>(null);

  // Wrapped: localStorage is not merely absent in some contexts (SSR, private
  // browsing, storage-blocking policies) — accessing it can THROW, and losing a
  // remembered pane layout must never take the editor down with it.
  useEffect(() => {
    try {
      const m = window.localStorage.getItem(MODE_KEY);
      if (m === "write" || m === "split" || m === "preview") setMode(m);
    } catch {
      /* keep the default layout */
    }
  }, []);

  const pickMode = (m: Mode) => {
    setMode(m);
    try {
      window.localStorage.setItem(MODE_KEY, m);
    } catch {
      /* the choice still applies to this session */
    }
  };

  useEffect(() => {
    if (autoFocus) taRef.current?.focus();
  }, [autoFocus]);

  // Fires for the restored initial mode too, so a host that keys layout off the
  // mode is correct on first paint rather than one interaction later.
  useEffect(() => {
    onModeChange?.(mode);
  }, [mode, onModeChange]);

  useEffect(() => {
    const sel = pendingSel.current;
    if (!sel) return;
    pendingSel.current = null;
    const ta = taRef.current;
    if (!ta) return;
    ta.focus();
    ta.setSelectionRange(sel.start, sel.end);
  }, [value]);

  const run = useCallback(
    (action: MarkdownAction) => {
      const ta = taRef.current;
      if (!ta || disabled) return;
      const next = applyMarkdownAction(
        { text: ta.value, selStart: ta.selectionStart, selEnd: ta.selectionEnd },
        action,
      );
      pendingSel.current = { start: next.selStart, end: next.selEnd };
      onChange(next.text);
      // Previewing means the textarea is unmounted and the toolbar would act on
      // nothing visible; switch back so the edit is seen where it happened.
      if (mode === "preview") pickMode("write");
    },
    [disabled, mode, onChange],
  );

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (!(e.metaKey || e.ctrlKey) || e.altKey) return;
    const action = SHORTCUTS[e.key.toLowerCase()];
    if (!action) return;
    e.preventDefault();
    run(action);
  };

  const showWrite = mode !== "preview";
  const showPreview = mode !== "write";

  return (
    <div className="rounded-md border border-neutral-200 bg-white">
      {/* Toolbar. Grouped with hairline dividers so the four families
          (emphasis, headings, lists, blocks) read apart at a glance. */}
      <div className="flex flex-wrap items-center gap-1 border-b border-neutral-200 px-2 py-1.5">
        {GROUPS.map((group, gi) => (
          <span key={gi} className="flex items-center gap-0.5">
            {gi > 0 ? <span className="mx-1 h-4 w-px bg-neutral-200" aria-hidden="true" /> : null}
            {group.map((t) => (
              <button
                key={t.action}
                type="button"
                onClick={() => run(t.action)}
                disabled={disabled}
                title={t.title}
                aria-label={t.title}
                className={
                  "min-w-[26px] rounded px-1.5 py-0.5 text-[12px] leading-5 text-neutral-600 transition hover:bg-neutral-100 hover:text-neutral-900 disabled:opacity-40 " +
                  (t.action === "bold" ? "font-bold " : "") +
                  (t.action === "italic" ? "italic " : "")
                }
              >
                {t.label}
              </button>
            ))}
          </span>
        ))}

        <span className="ml-auto inline-flex overflow-hidden rounded-md border border-neutral-200">
          {(["write", "split", "preview"] as Mode[]).map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => pickMode(m)}
              aria-pressed={mode === m}
              className={
                "px-2 py-0.5 text-[11px] capitalize transition " +
                (mode === m
                  ? "bg-neutral-800 text-white"
                  : "bg-white text-neutral-600 hover:bg-neutral-50")
              }
            >
              {m}
            </button>
          ))}
        </span>
      </div>

      <div className={showWrite && showPreview ? "grid md:grid-cols-2" : ""}>
        {showWrite ? (
          <textarea
            ref={taRef}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            onKeyDown={onKeyDown}
            disabled={disabled}
            spellCheck
            aria-label="markdown source"
            placeholder="Write markdown…"
            style={{ minHeight }}
            className={
              "w-full resize-y whitespace-pre-wrap break-words bg-white px-5 py-4 font-mono text-[12px] leading-relaxed text-neutral-800 focus:outline-none " +
              (showPreview ? "border-b border-neutral-200 md:border-b-0 md:border-r" : "")
            }
          />
        ) : null}
        {showPreview ? (
          <div className="overflow-auto px-5 py-4" style={{ minHeight }} aria-label="preview">
            {value.trim() === "" ? (
              <p className="text-[12px] text-neutral-400">Nothing to preview yet.</p>
            ) : (
              <MarkdownBody body={value} debounceMermaid />
            )}
          </div>
        ) : null}
      </div>
    </div>
  );
}
