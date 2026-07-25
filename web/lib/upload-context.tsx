"use client";

import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import UploadModal from "@/components/UploadModal";
import { bundleFiles } from "@/lib/upload";

// UploadProvider owns the single upload modal and a window-level drop
// target, so the upload flow can be triggered two ways that share one
// modal: the rail's "+ Upload" button (open with no file) and dragging a
// file anywhere onto the page (open pre-filled with the dropped file).

interface UploadAPI {
  open: (file?: File | null) => void;
}

const Ctx = createContext<UploadAPI>({ open: () => {} });

export function useUpload(): UploadAPI {
  return useContext(Ctx);
}

export function UploadProvider({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = useState(false);
  const [initialFile, setInitialFile] = useState<File | null>(null);
  const [dragging, setDragging] = useState(false);
  // Bumped on every open() so the modal remounts fresh each time — a second
  // open() (e.g. a new drop) reliably picks up the new initialFile and clears
  // any prior in-modal state, rather than reusing a mount-only effect.
  const [openSeq, setOpenSeq] = useState(0);

  // dragDepth counts dragenter/leave so the overlay doesn't flicker as the
  // cursor crosses child elements. openRef lets the window listeners read
  // the latest modal state without re-subscribing.
  const dragDepth = useRef(0);
  // Mirror `open` into a ref so the window listeners (subscribed once) read
  // the latest value without re-subscribing. Updated in an effect so we
  // don't write a ref during render.
  const openRef = useRef(open);
  useEffect(() => {
    openRef.current = open;
  }, [open]);

  const openUpload = useCallback((file?: File | null) => {
    setInitialFile(file ?? null);
    setOpenSeq((n) => n + 1);
    setOpen(true);
  }, []);

  useEffect(() => {
    // Only react to actual file drags (not text/element drags).
    const hasFiles = (e: DragEvent) =>
      Array.from(e.dataTransfer?.types ?? []).includes("Files");

    const onEnter = (e: DragEvent) => {
      if (openRef.current || !hasFiles(e)) return; // modal handles its own drops
      dragDepth.current += 1;
      setDragging(true);
    };
    const onOver = (e: DragEvent) => {
      if (hasFiles(e)) e.preventDefault(); // required to allow a drop
    };
    const onLeave = (e: DragEvent) => {
      if (openRef.current || !hasFiles(e)) return;
      dragDepth.current = Math.max(0, dragDepth.current - 1);
      if (dragDepth.current === 0) setDragging(false);
    };
    const onDrop = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      dragDepth.current = 0;
      setDragging(false);
      if (openRef.current) return; // let the open modal's own zone take it
      const files = e.dataTransfer?.files ? Array.from(e.dataTransfer.files) : [];
      // 1 file → as-is; 2+ → zipped into a flat PACKAGE.
      if (files.length) void bundleFiles(files).then(openUpload);
    };

    window.addEventListener("dragenter", onEnter);
    window.addEventListener("dragover", onOver);
    window.addEventListener("dragleave", onLeave);
    window.addEventListener("drop", onDrop);
    return () => {
      window.removeEventListener("dragenter", onEnter);
      window.removeEventListener("dragover", onOver);
      window.removeEventListener("dragleave", onLeave);
      window.removeEventListener("drop", onDrop);
    };
  }, [openUpload]);

  return (
    <Ctx.Provider value={{ open: openUpload }}>
      {children}
      {dragging && !open ? (
        // pointer-events-none so the cursor's drop target stays the page
        // (the window-level drop listener does the work); this is purely
        // the visual affordance.
        <div className="pointer-events-none fixed inset-0 z-[60] flex items-center justify-center bg-blue-600/10 p-4 backdrop-blur-sm">
          <div className="flex h-full w-full items-center justify-center rounded-2xl border-4 border-dashed border-blue-500 bg-white/70">
            <div className="text-center">
              <div className="text-2xl font-semibold text-blue-700">Drop files to upload</div>
              <div className="mt-1 text-sm text-blue-600/80">
                Release to open the upload form, pre-filled
              </div>
            </div>
          </div>
        </div>
      ) : null}
      {open ? (
        <UploadModal
          key={openSeq}
          initialFile={initialFile}
          onClose={() => {
            setOpen(false);
            setInitialFile(null);
          }}
        />
      ) : null}
    </Ctx.Provider>
  );
}
