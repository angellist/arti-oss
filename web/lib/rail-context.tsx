"use client";

import { createContext, useCallback, useContext, useEffect, useState } from "react";
import type { PackageManifest } from "./types";
import type { HelpNav } from "./help-docs";
import { fileParamSearch } from "./viewer";

export type RailMode =
  | { kind: "search" }
  | {
      kind: "package";
      manifest: PackageManifest;
      selected: string | null;
      setSelected: (path: string | null) => void;
    }
  | { kind: "help"; nav: HelpNav };

// Two separate contexts so the setter stays referentially stable across
// renders. Effects in per-route providers depend on `setMode` only; they
// fire on mount and on real input changes, not on every shell re-render.
const ModeCtx = createContext<RailMode>({ kind: "search" });
const SetModeCtx = createContext<(m: RailMode) => void>(() => {});

export function RailModeShell({ children }: { children: React.ReactNode }) {
  const [mode, setMode] = useState<RailMode>({ kind: "search" });
  return (
    <SetModeCtx.Provider value={setMode}>
      <ModeCtx.Provider value={mode}>{children}</ModeCtx.Provider>
    </SetModeCtx.Provider>
  );
}

export function useRailMode(): RailMode {
  return useContext(ModeCtx);
}

export function SearchRailProvider({
  children,
}: {
  children: React.ReactNode;
}) {
  const setMode = useContext(SetModeCtx);
  useEffect(() => {
    setMode({ kind: "search" });
  }, [setMode]);
  return <>{children}</>;
}

export function HelpRailProvider({
  nav,
  children,
}: {
  nav: HelpNav;
  children: React.ReactNode;
}) {
  const setMode = useContext(SetModeCtx);
  useEffect(() => {
    setMode({ kind: "help", nav });
    // Reset to the default search rail on unmount so leaving /help (e.g. to a
    // route that doesn't set its own rail mode, like /groups or /roles) doesn't
    // leave the help doc tree showing.
    return () => setMode({ kind: "search" });
  }, [setMode, nav]);
  return <>{children}</>;
}

export function PackageRailProvider({
  manifest,
  initialSelected,
  children,
}: {
  manifest: PackageManifest;
  initialSelected: string | null;
  children: React.ReactNode;
}) {
  const setMode = useContext(SetModeCtx);
  const [selected, setSelected] = useState<string | null>(initialSelected);

  // Selecting a file also reflects it in the URL (?file=<path>) so the address
  // bar is shareable and correct. We use the NATIVE history API, not
  // next/navigation's router.replace: this is a shallow, cosmetic update that
  // must NOT re-run the server component — a router navigation would refetch
  // the page and remount the viewer, reloading the file iframe on every click.
  // Next supports app-driven history.replaceState as shallow routing. Back
  // deliberately just leaves the artifact (no per-file history) — see the
  // package-file-url-sync design doc. Mount seeds `selected` via useState, which
  // does NOT call this, so there's no spurious URL write on load.
  const selectFile = useCallback(
    (path: string | null) => {
      setSelected(path);
      if (typeof window !== "undefined") {
        const search = fileParamSearch(window.location.search, path, manifest.entry_point ?? null);
        window.history.replaceState(null, "", window.location.pathname + search);
      }
    },
    [manifest.entry_point],
  );

  useEffect(() => {
    setMode({ kind: "package", manifest, selected, setSelected: selectFile });
  }, [setMode, manifest, selected, selectFile]);
  return <>{children}</>;
}
