"use client";

import Link from "next/link";

// A boxes-and-connector glyph, drawn at the same weight as SearchIcon /
// BrowseIcon / UploadButton's arrow so the rail links read as one set.
function DiagramIcon({ className }: { className?: string }) {
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
      <rect x="3" y="3" width="7" height="6" rx="1" />
      <rect x="14" y="15" width="7" height="6" rx="1" />
      <path d="M6.5 9v4a2 2 0 0 0 2 2h9" />
    </svg>
  );
}

// NewDiagramButton sits directly below Upload in the rail — the second way to
// bring an artifact into arti: draw one instead of uploading a file. Styled as
// a rail section link to match SEARCH / BROWSE ALL / UPLOAD.
export default function NewDiagramButton() {
  return (
    <Link
      href="/diagrams/new"
      className="flex w-full items-center gap-1.5 text-[10px] font-semibold uppercase tracking-widest text-neutral-400 transition hover:text-neutral-600"
    >
      <DiagramIcon className="h-3 w-3 shrink-0" />
      New diagram
    </Link>
  );
}
