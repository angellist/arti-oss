"use client";

import { useUpload } from "@/lib/upload-context";

// A non-emoji upload glyph that inherits text color (stroke = currentColor),
// matching SearchIcon's weight so the rail's SEARCH / UPLOAD entries read as
// one set.
function UploadIcon({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className={className}
    >
      <line x1="12" y1="19" x2="12" y2="5" />
      <polyline points="5 12 12 5 19 12" />
    </svg>
  );
}

// UploadButton is the "create artifact" affordance in the left rail. It opens
// the shared upload modal (owned by UploadProvider, which also handles page
// drag-and-drop). Styled as a rail section link — same type scale and leading
// icon as SEARCH / BROWSE ALL — and rendered directly below "Browse All"
// (search mode) or above the file tree (package mode).
export default function UploadButton() {
  const { open } = useUpload();
  return (
    <button
      type="button"
      onClick={() => open()}
      className="flex w-full items-center gap-1.5 text-[10px] font-semibold uppercase tracking-widest text-neutral-400 transition hover:text-neutral-600"
    >
      <UploadIcon className="h-3 w-3 shrink-0" />
      Upload
    </button>
  );
}
