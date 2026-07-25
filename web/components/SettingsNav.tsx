"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

// SettingsNav is the left sub-navigation inside the Settings area. User Groups
// and API Keys are visible to everyone; Roles and Permissions only when the
// caller holds MANAGE_ROLES (the server layout decides and passes canManageRoles).
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
      {item("/settings/groups", "User Groups")}
      {showApiKeys ? item("/settings/keys", "API Keys") : null}
      {canManageRoles ? item("/settings/roles", "Roles and Permissions") : null}
    </nav>
  );
}
