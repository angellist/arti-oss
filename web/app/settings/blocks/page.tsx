import { headers } from "next/headers";
import { getMe, listBlocks } from "@/lib/arti";
import BlocksManager from "@/components/BlocksManager";

export const dynamic = "force-dynamic";

export default async function SettingsBlocksPage() {
  const h = await headers();
  const cookie = h.get("cookie") ?? undefined;

  const me = await getMe(cookie).catch(() => ({ email: "", name: "", is_admin: false, permissions: [] as string[] }));
  const canManage = me.permissions?.includes("MANAGE_ARTIFACTS") ?? false;
  // Gate the page itself, not just the nav entry, so direct navigation by a
  // non-admin lands on a clean forbidden state rather than an empty table.
  const blocks = canManage ? await listBlocks(cookie).catch(() => []) : [];

  return (
    <div>
      <div className="px-6 pt-4">
        <h2 className="text-[14px] font-semibold text-neutral-900">Blocked Documents</h2>
        <p className="mt-0.5 text-[12px] text-neutral-500">
          a block takes a document away from everyone, admins included, and refuses writes to its
          slug. nothing about the document changes — its versions, owner and access list are
          untouched — so lifting the block restores it exactly. patterns match a slug, or an
          artifact id for a document that has no slug, with <code>*</code> for any run of
          characters and <code>?</code> for one.
        </p>
      </div>
      {canManage ? (
        <BlocksManager initialBlocks={blocks} />
      ) : (
        <div className="px-6 py-10 text-sm text-neutral-500">
          You need the MANAGE_ARTIFACTS permission to block documents. Ask an admin for access.
        </div>
      )}
    </div>
  );
}
