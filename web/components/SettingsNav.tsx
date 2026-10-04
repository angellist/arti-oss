"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

// SettingsNav is the left sub-navigation inside the Settings area, in two
// sections. General holds what every signed-in user sees. Admin holds the
// permission-gated entries (Users and Roles for MANAGE_ROLES, Blocked
// Documents for MANAGE_ARTIFACTS, App Connectors for MANAGE_CONNECTORS), and
// its heading renders only when at least
// one of them does, so a non-admin never sees an empty Admin section.
//
// Hiding the entry is the whole of the UI gating: both pages still RENDER for a
// caller without the permission, showing a "you need MANAGE_ROLES" notice rather
// than a 404. What is withheld is the data — the server 404s /api/users, and the
// page skips the fetch entirely — so the notice reveals nothing beyond the
// feature's existence.
//
// Help & Docs is the one entry that leaves this layout: the docs site has its
// own rail, so it opens full-screen rather than inside the settings frame.
export default function SettingsNav({
  canManageRoles,
  canManageArtifacts,
  canManageConnectors,
  showApiKeys,
}: {
  canManageRoles: boolean;
  canManageArtifacts?: boolean;
  canManageConnectors?: boolean;
  showApiKeys?: boolean;
}) {
  const pathname = usePathname();
  const item = (href: string, label: string) => {
    const active = pathname === href || pathname.startsWith(href + "/");
    return (
      <Link
        href={href}
        className={
          "block rounded px-3 py-1.5 text-[13px] " +
          (active ? "bg-neutral-100 font-medium text-neutral-900" : "text-neutral-600 hover:bg-neutral-50")
        }
      >
        {label}
      </Link>
    );
  };
  const heading = (label: string) => (
    <div className="px-3 pb-1 pt-2 text-[11px] font-semibold uppercase tracking-wider text-neutral-400">{label}</div>
  );
  return (
    <nav className="w-full shrink-0 space-y-0.5 border-b border-neutral-200 p-2 md:w-52 md:border-b-0 md:border-r">
      {heading("General")}
      {showApiKeys ? item("/settings/keys", "API Keys") : null}
      {item("/settings/groups", "User Groups")}
      {item("/settings/notifications", "Notifications")}
      {item("/settings/appearance", "Appearance")}
      {item("/settings/archived", "Archived")}
      <Link
        href="/help"
        className="block rounded px-3 py-1.5 text-[13px] text-neutral-600 hover:bg-neutral-50"
      >
        Help &amp; Docs
      </Link>
      {canManageRoles || canManageArtifacts || canManageConnectors ? <div className="pt-2">{heading("Admin")}</div> : null}
      {canManageRoles ? item("/settings/users", "Users") : null}
      {canManageRoles ? item("/settings/roles", "Roles and Permissions") : null}
      {canManageArtifacts ? item("/settings/blocks", "Blocked Documents") : null}
      {canManageConnectors ? item("/settings/connectors", "App Connectors") : null}
    </nav>
  );
}
