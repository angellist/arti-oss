import type { UserAccess } from "@/lib/types";

// Human labels for the opaque permission keys. Unknown keys fall back to the
// raw string so a newly-added permission still renders.
const PERM_LABEL: Record<string, string> = {
  MANAGE_ROLES: "Manage roles",
  MANAGE_USER_GROUPS: "Manage user groups",
  MANAGE_ARTIFACTS: "Manage all artifacts",
  MANAGE_SKILLS: "Manage skills",
  MANAGE_API_KEYS: "Manage all API keys",
  MANAGE_CONNECTORS: "Manage app connectors",
  ASSERT_ACTOR: "Write on a person's behalf (service principals)",
  READ_ORG_SHARED: "Read org-wide shared artifacts (service principals only)",
  USE_ARTIFACTS: "Use artifacts",
};
const permLabel = (k: string) => PERM_LABEL[k] ?? k;

export function PermName({ perm, muted = false }: { perm: string; muted?: boolean }) {
  return (
    <span className="flex flex-col">
      <span className={"text-[13px] " + (muted ? "text-neutral-400" : "text-neutral-800")}>{permLabel(perm)}</span>
      <span className="font-mono text-[10px] text-neutral-400">{perm}</span>
    </span>
  );
}

// AccessMatrix renders one column per role the person holds and one row per
// permission, a dot where the role grants it — so the union across columns is
// the person's effective access.
export function AccessMatrix({ access, permissions }: { access: UserAccess; permissions: string[] }) {
  if (access.roles.length === 0) {
    return <p className="mt-3 text-[12px] text-neutral-500">{access.email} holds no roles.</p>;
  }
  const effective = new Set(access.effective_permissions);
  return (
    <div className="mt-3 overflow-x-auto">
      <table className="border-collapse text-[12px]">
        <thead>
          <tr>
            <th className="border-b border-neutral-200 px-2 py-1 text-left font-medium text-neutral-500">Permission</th>
            {access.roles.map((role, i) => (
              <th key={role.name + ":" + role.source + ":" + i} className="border-b border-neutral-200 px-3 py-1 text-center">
                <div className="font-medium text-neutral-800">{role.name}</div>
                <div className="text-[10px] font-normal text-neutral-400">
                  {role.source.startsWith("group:") ? "from " + role.source : role.source}
                </div>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {permissions.map((perm) => (
            <tr key={perm} className={effective.has(perm) ? "" : "opacity-60"}>
              <td className="border-b border-neutral-100 px-2 py-1">
                <PermName perm={perm} />
              </td>
              {access.roles.map((role, i) => (
                <td key={role.name + ":" + role.source + ":" + i} className="border-b border-neutral-100 px-3 py-1 text-center">
                  {role.permissions.includes(perm) ? (
                    <span className="inline-block h-2 w-2 rounded-full bg-emerald-500" aria-label="granted" />
                  ) : (
                    <span className="text-neutral-300">·</span>
                  )}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
