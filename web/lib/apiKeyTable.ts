import type { ApiKey } from "./types";

export type KeyStatus = "Active" | "Expired" | "Revoked";
export type StatusFilter = "all" | "active" | "expired" | "revoked";
export type SortKey =
  | "name"
  | "owner"
  | "key"
  | "scope"
  | "created"
  | "expires"
  | "last_used"
  | "sources"
  | "docs"
  | "status";
export type SortDir = "asc" | "desc";

export function keyStatus(k: ApiKey, now: Date = new Date()): KeyStatus {
  if (k.revoked_at) return "Revoked";
  if (new Date(k.expires_at) < now) return "Expired";
  return "Active";
}

// The "upload" scope's allowlist also admits every artifact GET, so the
// label has to say read as well or people assume the key is write-only.
const scopeLabels: Record<string, string> = { upload: "Read + Upload" };

export function scopeLabel(scope: string): string {
  return Object.hasOwn(scopeLabels, scope) ? scopeLabels[scope] : scope;
}

export function ownersOf(keys: ApiKey[]): string[] {
  return [...new Set(keys.map((k) => k.owner_email.toLowerCase()))].sort();
}

export function filterKeys(
  keys: ApiKey[],
  f: { owner: string; status: StatusFilter; query?: string },
  now: Date = new Date(),
): ApiKey[] {
  const q = (f.query ?? "").trim().toLowerCase();
  return keys.filter(
    (k) =>
      (f.owner === "" || k.owner_email.toLowerCase() === f.owner.toLowerCase()) &&
      (f.status === "all" || keyStatus(k, now).toLowerCase() === f.status) &&
      (q === "" || k.name.toLowerCase().includes(q) || k.owner_email.toLowerCase().includes(q)),
  );
}

export interface KeyMetrics {
  sources: number;
  docs: number;
}

type SortValue = string | number | null;

function sortValue(k: ApiKey, key: SortKey, m: KeyMetrics, now: Date): SortValue {
  const time = (s: string | null) => (s ? new Date(s).getTime() : null);
  switch (key) {
    case "name":
      return k.name.toLowerCase();
    case "owner":
      return k.owner_email.toLowerCase();
    case "key":
      return k.key_prefix;
    case "scope":
      return k.scopes.map(scopeLabel).join(", ");
    case "created":
      return time(k.created_at);
    case "expires":
      return time(k.expires_at);
    case "last_used":
      return time(k.last_used_at);
    case "sources":
      return m.sources;
    case "docs":
      return m.docs;
    case "status":
      return keyStatus(k, now);
  }
}

// sortKeys orders keys by one column. Empty values (a key never used) sort
// last in both directions; ties keep the input order.
export function sortKeys(
  keys: ApiKey[],
  key: SortKey,
  dir: SortDir,
  metrics: (k: ApiKey) => KeyMetrics,
  now: Date = new Date(),
): ApiKey[] {
  const sign = dir === "asc" ? 1 : -1;
  return keys
    .map((k, i) => ({ k, i, v: sortValue(k, key, metrics(k), now) }))
    .sort((a, b) => {
      if (a.v === null || b.v === null) {
        if (a.v === b.v) return a.i - b.i;
        return a.v === null ? 1 : -1;
      }
      const c = a.v < b.v ? -1 : a.v > b.v ? 1 : 0;
      return c !== 0 ? c * sign : a.i - b.i;
    })
    .map((x) => x.k);
}
