import { headers } from "next/headers";
import { getMe, listRoles, listPermissions } from "@/lib/arti";
import RolesManager from "@/components/RolesManager";

export const dynamic = "force-dynamic";

export default async function SettingsRolesPage() {
  const h = await headers();
  const cookie = h.get("cookie") ?? undefined;

  const me = await getMe(cookie).catch(() => ({ email: "", name: "", is_admin: false, permissions: [] as string[] }));
  const canManage = me.permissions?.includes("MANAGE_ROLES") ?? false;
  // Gate the page itself (not just the nav) so direct navigation by a
  // non-admin lands on a clean forbidden state.
  const [roles, permissions] = canManage
    ? await Promise.all([listRoles(cookie).catch(() => []), listPermissions(cookie).catch(() => [])])
    : [[], []];

  return (
    <div>
      <div className="px-6 pt-4">
        <h2 className="text-[14px] font-semibold text-neutral-900">Roles and Permissions</h2>
        <p className="mt-0.5 text-[12px] text-neutral-500">
          roles are named sets of permissions. assign a role to a person or a user group; a
          permission is a capability key checked in code.
        </p>
      </div>
      {canManage ? (
        <RolesManager initialRoles={roles} permissions={permissions} />
      ) : (
        <div className="px-6 py-10 text-sm text-neutral-500">
          You need the MANAGE_ROLES permission to manage roles. Ask an admin for access.
        </div>
      )}
    </div>
  );
}
