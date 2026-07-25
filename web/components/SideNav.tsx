"use client";

import { Suspense, useEffect, useRef, useState } from "react";
import { usePathname } from "next/navigation";
import Link from "next/link";
import { getMe } from "@/lib/arti";
import type { Me } from "@/lib/types";
import { useRailMode } from "@/lib/rail-context";
import SideNavSearch from "./SideNavSearch";
import SideNavPackage from "./SideNavPackage";
import SideNavHelp from "./SideNavHelp";
import SideNavUserMenu from "./SideNavUserMenu";
import UploadButton from "./UploadButton";

const COLLAPSE_KEY = "arti.rail.collapsed";
// Width is remembered separately per rail mode: package pages tend to
// want a wider rail (full file tree visible), so resizing on one mode
// shouldn't clobber the preference for the other.
const WIDTH_KEY_PACKAGE = "arti.rail.width.package";
const WIDTH_KEY_DEFAULT = "arti.rail.width.default";

const DEFAULT_WIDTH_DEFAULT = 240; // search mode: "all / TEXT / PACKAGE" don't wrap
const DEFAULT_WIDTH_PACKAGE = 320; // package mode: roomier file tree
const MIN_WIDTH = 200;
const MAX_WIDTH = 480;

function clamp(n: number): number {
  return Math.max(MIN_WIDTH, Math.min(MAX_WIDTH, n));
}

function widthKeyFor(kind: string): string {
  return kind === "package" ? WIDTH_KEY_PACKAGE : WIDTH_KEY_DEFAULT;
}

function defaultWidthFor(kind: string): number {
  return kind === "package" ? DEFAULT_WIDTH_PACKAGE : DEFAULT_WIDTH_DEFAULT;
}

function readInitialCollapsed(): boolean {
  if (typeof window === "undefined") return false;
  return window.localStorage.getItem(COLLAPSE_KEY) === "1";
}

function readWidthFor(kind: string): number {
  if (typeof window === "undefined") return defaultWidthFor(kind);
  const raw = window.localStorage.getItem(widthKeyFor(kind));
  const n = raw ? parseInt(raw, 10) : NaN;
  return isNaN(n) ? defaultWidthFor(kind) : clamp(n);
}

export default function SideNav() {
  const mode = useRailMode();
  const [collapsed, setCollapsed] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);
  const [width, setWidth] = useState(DEFAULT_WIDTH_DEFAULT);
  const [me, setMe] = useState<Me | null>(null);
  const widthRef = useRef(width);
  widthRef.current = width;
  // Track which mode owns the current width value so the resize handler
  // saves to the right key even if mode flips mid-drag (rare but real:
  // SPA nav into a package page during a resize).
  const modeKindRef = useRef(mode.kind);
  modeKindRef.current = mode.kind;
  // Close the mobile drawer on any pathname change — covers most
  // navigations out of the drawer (clicking an artifact link). Filter
  // toggles inside SideNavSearch change query params only, so we also
  // delegate-close on click of any <a>/<button> inside the drawer
  // body (see the onClick on the drawer aside below). We avoid
  // useSearchParams() at this layer because that forces dynamic
  // rendering on every layout consumer — including /_not-found,
  // which Next pre-renders statically.
  const pathname = usePathname();
  useEffect(() => {
    setMobileOpen(false);
  }, [pathname]);

  useEffect(() => {
    setCollapsed(readInitialCollapsed());
    getMe().then(setMe).catch(() => setMe(null));
  }, []);

  // Switch the stored width whenever the rail mode changes (e.g. user
  // navigates from a search page into a package view). On first mount
  // this also fires once for the initial mode.
  useEffect(() => {
    setWidth(readWidthFor(mode.kind));
  }, [mode.kind]);

  const toggle = () => {
    setCollapsed((c) => {
      const next = !c;
      try {
        window.localStorage.setItem(COLLAPSE_KEY, next ? "1" : "0");
      } catch {
        // localStorage may be disabled; UI keeps working, just no persistence.
      }
      return next;
    });
  };

  // Drag handle: capture mousemove on document so the cursor doesn't have
  // to stay perfectly on the 1px-wide handle to keep resizing.
  const onResizeStart = (e: React.MouseEvent) => {
    e.preventDefault();
    const startX = e.clientX;
    const startW = widthRef.current;
    const onMove = (ev: MouseEvent) => {
      setWidth(clamp(startW + (ev.clientX - startX)));
    };
    const onUp = () => {
      document.removeEventListener("mousemove", onMove);
      document.removeEventListener("mouseup", onUp);
      try {
        window.localStorage.setItem(widthKeyFor(modeKindRef.current), String(widthRef.current));
      } catch {
        // ignore
      }
    };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  };

  // Body of the rail (everything between the header and the resize
  // handle). Same content in desktop sidebar and mobile drawer.
  const railBody = (
    <>
      <div className="flex-1 overflow-y-auto overflow-x-hidden px-4 pb-6 pt-4">
        {mode.kind === "package" ? (
          // In package mode there's no search box, so the upload button rides
          // at the top of the rail to stay reachable. In search mode it lives
          // just below the search box (see SideNavSearch).
          <div className="space-y-4">
            <UploadButton />
            <SideNavPackage manifest={mode.manifest} selected={mode.selected} onSelect={mode.setSelected} />
          </div>
        ) : mode.kind === "help" ? (
          <SideNavHelp nav={mode.nav} />
        ) : (
          // useSearchParams() inside SideNavSearch forces every page that
          // includes this layout to opt into client-side rendering during
          // static export; a Suspense boundary tells Next.js to defer it.
          <Suspense fallback={null}>
            <SideNavSearch />
          </Suspense>
        )}
      </div>
      <div className="border-t border-neutral-100 px-4 py-3 text-[12px] text-neutral-500">
        <SideNavUserMenu me={me} />
      </div>
    </>
  );

  // ─── Mobile (<md): fixed top bar + slide-down drawer ──────────────
  // The desktop <aside> below is hidden via `hidden md:flex`, so on
  // mobile the only visible affordance is this bar + the drawer it
  // opens. The drawer is `fixed`, so it doesn't disturb flow layout.
  const mobile = (
    <>
      <div
        className="fixed inset-x-0 top-0 z-30 flex h-12 items-center justify-between border-b border-neutral-200 bg-white px-3 md:hidden"
        aria-label="mobile navigation bar"
      >
        <Link href="/" className="flex items-center gap-2">
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img src="/logo.png" alt="arti" width={24} height={24} className="rounded" />
          <span
            className="text-lg font-bold text-neutral-900"
            style={{ fontFamily: "var(--font-petrona)", letterSpacing: "-0.03em" }}
          >
            arti
          </span>
        </Link>
        <button
          type="button"
          onClick={() => setMobileOpen((o) => !o)}
          aria-label={mobileOpen ? "close navigation" : "open navigation"}
          aria-expanded={mobileOpen}
          className="rounded p-2 text-neutral-600 hover:bg-neutral-100"
        >
          {mobileOpen ? <CloseIcon /> : <HamburgerIcon />}
        </button>
      </div>
      {mobileOpen ? (
        <>
          {/* Backdrop: tap-outside dismiss + dims the content behind. */}
          <div
            className="fixed inset-0 top-12 z-30 bg-black/30 md:hidden"
            onClick={() => setMobileOpen(false)}
            aria-hidden="true"
          />
          <aside
            className="fixed inset-x-0 top-12 z-40 flex max-h-[calc(100dvh-3rem)] flex-col border-b border-neutral-200 bg-white shadow-lg md:hidden"
            aria-label="navigation drawer"
            onClick={(e) => {
              // Delegate-close: any <a>/<button> click bubbles up
              // here. Includes type-filter chips that change query
              // params (no pathname change), so usePathname alone
              // wouldn't catch them.
              const t = e.target as HTMLElement;
              if (t.closest("a, button")) setMobileOpen(false);
            }}
          >
            {railBody}
          </aside>
        </>
      ) : null}
    </>
  );

  // ─── Desktop (md+) collapsed rail ─────────────────────────────────
  if (collapsed) {
    return (
      <>
        {mobile}
        <aside
          className="sticky top-0 hidden h-screen w-10 shrink-0 flex-col items-center border-r border-neutral-200 bg-white pt-3 md:flex"
          aria-label="navigation rail (collapsed)"
        >
          <button
            type="button"
            onClick={toggle}
            aria-label="expand navigation"
            className="rounded p-1 text-neutral-500 hover:bg-neutral-100"
          >
            <CollapseIcon expanded={false} />
          </button>
        </aside>
      </>
    );
  }

  // ─── Desktop (md+) full rail ──────────────────────────────────────
  return (
    <>
      {mobile}
      <aside
        style={{ width: `${width}px` }}
        className="relative sticky top-0 hidden h-screen shrink-0 flex-col border-r border-neutral-200 bg-white md:flex"
        aria-label="navigation rail"
        suppressHydrationWarning
      >
        <div className="flex items-center justify-between px-4 pt-3">
          <Link href="/" className="flex items-center gap-2">
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img src="/logo.png" alt="arti" width={28} height={28} className="rounded" />
            <span
              className="text-2xl font-bold text-neutral-900"
              style={{ fontFamily: "var(--font-petrona)", letterSpacing: "-0.03em" }}
            >
              arti
            </span>
          </Link>
          <button
            type="button"
            onClick={toggle}
            aria-label="collapse navigation"
            className="rounded p-1 text-neutral-500 hover:bg-neutral-100"
          >
            <CollapseIcon expanded={true} />
          </button>
        </div>
        {railBody}
        <div
          role="separator"
          aria-orientation="vertical"
          aria-label="resize navigation"
          onMouseDown={onResizeStart}
          className="absolute right-0 top-0 z-10 h-full w-1 cursor-col-resize bg-transparent transition hover:bg-blue-200/60"
        />
      </aside>
    </>
  );
}

function HamburgerIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
      <line x1="3" y1="6" x2="21" y2="6" />
      <line x1="3" y1="12" x2="21" y2="12" />
      <line x1="3" y1="18" x2="21" y2="18" />
    </svg>
  );
}

function CloseIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
      <line x1="6" y1="6" x2="18" y2="18" />
      <line x1="18" y1="6" x2="6" y2="18" />
    </svg>
  );
}

function CollapseIcon({ expanded }: { expanded: boolean }) {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <rect x="3" y="4" width="18" height="16" rx="2" />
      <line x1="9" y1="4" x2="9" y2="20" />
      {expanded ? null : <polyline points="14 10 17 12 14 14" />}
    </svg>
  );
}
