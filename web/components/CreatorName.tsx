"use client";

import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

// CreatorName renders just the local-part of a creator's email (the text
// before "@") to keep the creator column compact, revealing the full address
// in a tooltip after a brief hover. The tooltip is portaled to <body> so the
// table's `overflow-x-auto` wrapper can't clip it, and a ~0.25s delay keeps it
// from flickering as the pointer passes over the column.
export default function CreatorName({ email }: { email: string }) {
  const ref = useRef<HTMLSpanElement>(null);
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState({ top: 0, left: 0 });
  const timer = useRef<number | null>(null);

  // Clear a pending show-timer if the row unmounts mid-hover. `open` can only
  // flip true from a client-side mouse event, so the portal (and its
  // document.body reference) is never reached during SSR — no mounted guard
  // needed.
  useEffect(() => {
    return () => {
      if (timer.current) window.clearTimeout(timer.current);
    };
  }, []);

  const at = email.indexOf("@");
  // No local/host split to collapse — show the value verbatim, no tooltip.
  if (at <= 0) return <span>{email}</span>;
  const local = email.slice(0, at);

  const show = () => {
    if (timer.current) window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => {
      const r = ref.current?.getBoundingClientRect();
      if (r) setPos({ top: r.bottom + 4, left: r.left });
      setOpen(true);
    }, 250);
  };
  const hide = () => {
    if (timer.current) window.clearTimeout(timer.current);
    setOpen(false);
  };

  return (
    <span
      ref={ref}
      onMouseEnter={show}
      onMouseLeave={hide}
      className="cursor-default"
    >
      {local}
      {open
        ? createPortal(
            <div
              role="tooltip"
              style={{ position: "fixed", top: pos.top, left: pos.left }}
              className="pointer-events-none z-50 rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 font-mono text-[11px] text-neutral-100 shadow-lg"
            >
              {email}
            </div>,
            document.body,
          )
        : null}
    </span>
  );
}
