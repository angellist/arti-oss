// Returns a short human label like "5s ago" / "12m ago" / "3h ago" for
// timestamps within the last 24h. Older timestamps fall back to a
// YYYY-MM-DD HH:MM string. The caller is expected to attach the full
// ISO timestamp to a tooltip for accuracy.
export function relativeTime(iso: string): string {
  const t = Date.parse(iso);
  if (isNaN(t)) return iso;
  const diff = Math.max(0, Date.now() - t);
  const s = Math.floor(diff / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  // Older: short calendar date in the user's locale, no seconds.
  return iso.replace("T", " ").slice(0, 16);
}
