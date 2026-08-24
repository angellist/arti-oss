"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

// SettingsNav is the left sub-navigation inside the Settings area. API Keys
// leads (it's the most-visited surface) and User Groups follows; both are
// visible to everyone. Users and Roles and Permissions show only when the
// caller holds MANAGE_ROLES (the server layout decides and passes
// canManageRoles) — a roster of every colleague's email and role is admin
// information.
//
// Hiding the entry is the whole of the UI gating: both pages still RENDER for a
// caller without the permission, showing a "you need MANAGE_ROLES" notice rather
// than a 404. What is withheld is the data — the server 404s /api/users, and the
// page skips the fetch entirely — so the notice reveals nothing beyond the
// feature's existence.
export default function SettingsNav({ canManageRoles, showApiKeys }: { canManageRoles: boolean; showApiKeys?: boolean }) {
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
  return (
    <nav className="w-full shrink-0 space-y-0.5 border-b border-neutral-200 p-2 md:w-52 md:border-b-0 md:border-r">
      {showApiKeys ? item("/settings/keys", "API Keys") : null}
      {canManageRoles ? item("/settings/users", "Users") : null}
      {item("/settings/groups", "User Groups")}
      {canManageRoles ? item("/settings/roles", "Roles and Permissions") : null}
    </nav>
  );
}
