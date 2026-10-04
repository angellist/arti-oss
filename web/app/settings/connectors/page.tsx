import { headers } from "next/headers";
import { getMe, listMcpServers } from "@/lib/arti";
import ConnectorsManager from "@/components/ConnectorsManager";

export const dynamic = "force-dynamic";

export default async function SettingsConnectorsPage() {
  const h = await headers();
  const cookie = h.get("cookie") ?? undefined;

  const me = await getMe(cookie).catch(() => ({ email: "", name: "", is_admin: false, permissions: [] as string[] }));
  const canManage = me.permissions?.includes("MANAGE_CONNECTORS") ?? false;
  const servers = canManage ? await listMcpServers(cookie).catch(() => []) : [];

  return (
    <div>
      <div className="px-6 pt-4">
        <h2 className="text-[14px] font-semibold text-neutral-900">App Connectors</h2>
        <p className="mt-0.5 text-[12px] text-neutral-500">
          the MCP servers an APP may call through arti. a disabled connector answers every app as if it did not
          exist, and viewers keep their sign-ins, so enabling it again needs no new consent. deleting a connector
          also signs every viewer out of it. a tool allowlist narrows what apps may call; empty means every tool
          an app declares. registering the connector upstream stays in Runlayer.
        </p>
      </div>
      {canManage ? (
        <ConnectorsManager initialServers={servers} />
      ) : (
        <div className="px-6 py-10 text-sm text-neutral-500">
          You need the MANAGE_CONNECTORS permission to manage app connectors. Ask an admin for access.
        </div>
      )}
    </div>
  );
}
