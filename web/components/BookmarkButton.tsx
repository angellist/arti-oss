"use client";

import { useEffect, useState } from "react";
import { getBookmarks, setBookmark, type Bookmarks } from "@/lib/arti";

function BookmarkIcon({ filled }: { filled: boolean }) {
  return (
    <svg
      width="13"
      height="13"
      viewBox="0 0 24 24"
      fill={filled ? "currentColor" : "none"}
      stroke="currentColor"
      strokeWidth="2"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M6 3h12a1 1 0 0 1 1 1v17l-7-5-7 5V4a1 1 0 0 1 1-1z" />
    </svg>
  );
}

export function BookmarkToggle({
  count,
  bookmarked,
  onToggle,
}: Bookmarks & { onToggle: () => void }) {
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-pressed={bookmarked}
      aria-label={bookmarked ? "remove bookmark" : "bookmark"}
      title={
        (bookmarked ? "remove bookmark" : "bookmark — find it under Filters › Bookmarked") +
        (count > 0 ? ` · ${count} bookmarked` : "")
      }
      className={
        "inline-flex items-center gap-1 leading-none transition " +
        (bookmarked ? "text-amber-500" : "text-neutral-300 hover:text-amber-500")
      }
    >
      <BookmarkIcon filled={bookmarked} />
      {count > 0 ? <span className="text-[11px] text-neutral-500">{count}</span> : null}
    </button>
  );
}

// The viewer's bookmark, slug-scoped. Optimistic: the server's answer replaces
// the guess, a failure restores the previous state. Key it on the artifact id
// so a document switch starts from a clean state.
export default function BookmarkButton({ artifactID }: { artifactID: string }) {
  const [b, setB] = useState<Bookmarks | null>(null);
  useEffect(() => {
    let live = true;
    getBookmarks(artifactID)
      .then((x) => {
        if (live) setB(x);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [artifactID]);
  if (!b) return null;

  const toggle = () => {
    const prev = b;
    const on = !b.bookmarked;
    setB({ count: b.count + (on ? 1 : -1), bookmarked: on });
    setBookmark(artifactID, on)
      .then(setB)
      .catch(() => setB(prev));
  };
  return <BookmarkToggle {...b} onToggle={toggle} />;
}
