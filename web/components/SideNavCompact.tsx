"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useOpenSearch, useStickyFilter } from "@/lib/catalog-nav";
import type { Me } from "@/lib/types";
import NewMenu from "./NewMenu";
import { RailTip, railTileClass } from "./RailTile";
import { SearchIcon } from "./SearchIcon";
import SideNavAccount from "./SideNavAccount";
import { AppsIcon } from "./SideNavSearch";
import { GearIcon, expiringKeyMessage, useExpiringKey } from "./SideNavSettings";

// The collapsed rail: the arti logo (hover reveals the expand glyph), the rail's
// links as centred icon tiles, NEW below a divider, and the avatar at the foot.
// The filter sections have no icon form, so they only live in the full rail.
export default function SideNavCompact({
  me,
  onExpand,
  expandIcon,
}: {
  me: Me | null;
  onExpand: () => void;
  expandIcon: React.ReactNode;
}) {
  const pathname = usePathname();
  const openSearch = useOpenSearch(useStickyFilter());
  const expiringKey = useExpiringKey();

  return (
    <>
      <button
        type="button"
        onClick={onExpand}
        aria-label="expand navigation"
        className="group relative mt-2 grid h-9 w-9 place-items-center rounded-lg hover:bg-neutral-100 focus-visible:bg-neutral-100 focus-visible:outline-none"
      >
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img
          src="/logo.png"
          alt=""
          width={28}
          height={28}
          className="rounded transition-opacity duration-150 group-hover:opacity-0 group-focus-visible:opacity-0"
        />
        <span className="absolute inset-0 grid place-items-center text-[#2a8a8a] opacity-0 transition-opacity duration-150 group-hover:opacity-100 group-focus-visible:opacity-100">
          {expandIcon}
        </span>
        <RailTip>Expand sidebar</RailTip>
      </button>

      <nav className="flex flex-col items-center gap-0.5 pt-4" aria-label="navigation">
        <button type="button" onClick={openSearch} aria-label="Search" className={railTileClass()}>
          <SearchIcon className="h-4 w-4" />
          <RailTip>Search</RailTip>
        </button>
        <Link href="/apps" aria-label="Apps" className={railTileClass({ active: pathname === "/apps" })}>
          <AppsIcon className="h-4 w-4" />
          <RailTip>Apps</RailTip>
        </Link>
        <Link
          href="/settings"
          aria-label={expiringKey ? `Settings: ${expiringKeyMessage(expiringKey)}` : "Settings"}
          className={railTileClass({ active: pathname.startsWith("/settings") })}
        >
          <GearIcon className="h-4 w-4" />
          {expiringKey && <span className="absolute right-1.5 top-1.5 h-[5px] w-[5px] rounded-full bg-yellow-400" />}
          <RailTip>{expiringKey ? "Settings · API key expiring" : "Settings"}</RailTip>
        </Link>
        <span className="my-1.5 h-px w-6 bg-neutral-200" aria-hidden="true" />
        <NewMenu compact />
      </nav>

      <div className="mt-auto flex w-full justify-center border-t border-neutral-100 py-3">
        <SideNavAccount me={me} compact />
      </div>
    </>
  );
}
