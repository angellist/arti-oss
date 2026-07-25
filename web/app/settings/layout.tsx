import { headers } from "next/headers";
import type { Metadata } from "next";
import { SearchRailProvider } from "@/lib/rail-context";
import { getMe } from "@/lib/arti";
import SettingsNav from "@/components/SettingsNav";

export const dynamic = "force-dynamic";

export const metadata: Metadata = { title: "settings" };

// Settings groups the admin/account surfaces under one area with its own left
// sub-nav (User Groups for everyone; Roles and Permissions for MANAGE_ROLES).
export default async function SettingsLayout({ children }: { children: React.ReactNode }) {
  const h = await headers();
  const cookie = h.get("cookie") ?? undefined;
  const me = await getMe(cookie).catch(() => ({ email: "", name: "", is_admin: false, permissions: [] as string[] }));
  const canManageRoles = me.permissions?.includes("MANAGE_ROLES") ?? false;

  return (
    <SearchRailProvider>
      <main className="min-h-screen pb-12">
        <div className="border-b border-neutral-200 bg-neutral-50 px-6 py-3">
          <h1 className="text-base font-semibold text-neutral-900">settings</h1>
        </div>
        <div className="flex flex-col md:flex-row">
          <SettingsNav canManageRoles={canManageRoles} showApiKeys />
          <div className="min-w-0 flex-1">{children}</div>
        </div>
      </main>
    </SearchRailProvider>
  );
}
