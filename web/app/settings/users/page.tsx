import { headers } from "next/headers";
import { getMe, listUsers } from "@/lib/arti";
import UsersManager from "@/components/UsersManager";

export const dynamic = "force-dynamic";

export default async function SettingsUsersPage() {
  const h = await headers();
  const cookie = h.get("cookie") ?? undefined;

  const me = await getMe(cookie).catch(() => ({ email: "", name: "", is_admin: false, permissions: [] as string[] }));
  const canManage = me.permissions?.includes("MANAGE_ROLES") ?? false;
  // Gate the page itself, not just the nav entry, so direct navigation by a
  // non-admin lands on a clean forbidden state rather than an empty table.
  const roster = canManage
    ? await listUsers(cookie).catch(() => ({ users: [], sources: [] }))
    : { users: [], sources: [] };

  return (
    <div>
      <div className="px-6 pt-4">
        <h2 className="text-[14px] font-semibold text-neutral-900">Users</h2>
        <p className="mt-0.5 max-w-[70ch] text-[12px] leading-relaxed text-neutral-500">
          everyone arti knows about. arti never creates accounts — it trusts the identity its
          login gives it — so this list is assembled from wherever an email was recorded: a
          sign-in, an artifact, an API key, a comment, a group, a role. add a service account
          here to record it before it has done any of those things.
        </p>
      </div>
      {canManage ? (
        <UsersManager initialUsers={roster.users} sources={roster.sources} />
      ) : (
        <div className="px-6 py-10 text-sm text-neutral-500">
          You need the MANAGE_ROLES permission to view users. Ask an admin for access.
        </div>
      )}
    </div>
  );
}
