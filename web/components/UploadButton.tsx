"use client";

import { useUpload } from "@/lib/upload-context";

// UploadButton is the primary "create artifact" affordance in the left
// rail. It opens the shared upload modal (owned by UploadProvider, which
// also handles page drag-and-drop). Rendered below the search box (search
// mode) and above the file tree (package mode) so it's always reachable.
export default function UploadButton() {
  const { open } = useUpload();
  return (
    <button
      type="button"
      onClick={() => open()}
      className="flex w-full items-center justify-center gap-1.5 rounded-md bg-neutral-200 px-3 py-1.5 text-[13px] font-medium text-neutral-800 transition hover:bg-neutral-300"
    >
      <span className="text-[15px] leading-none">+</span> Upload
    </button>
  );
}
