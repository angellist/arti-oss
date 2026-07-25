import { headers } from "next/headers";
import { listGroups } from "@/lib/arti";
import GroupsManager from "@/components/GroupsManager";

export const dynamic = "force-dynamic";

export default async function SettingsGroupsPage() {
  const h = await headers();
  const cookie = h.get("cookie") ?? undefined;
  // Scoped server-side: you see/create the groups you own (admins see all).
  const groups = await listGroups(cookie).catch(() => []);

  return (
    <div>
      <div className="px-6 pt-4">
        <h2 className="text-[14px] font-semibold text-neutral-900">User Groups</h2>
        <p className="mt-0.5 text-[12px] text-neutral-500">
          named collections of people you create. grant a group access to an artifact and every
          member can read it; edits to membership take effect immediately.
        </p>
      </div>
      <GroupsManager initialGroups={groups} />
    </div>
  );
}
