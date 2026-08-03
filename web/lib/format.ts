// Human-readable byte size. Returns the original count for anything
// under 1 KB, otherwise switches to KB / MB with up to one decimal.
//
//   923           → "923 bytes"
//   17_155        → "16.8 KB"
//   1_500_000     → "1.4 MB"
//   null          → ""
export function formatBytes(n: number | null | undefined): string {
  if (n == null) return "";
  if (n < 1024) return `${n} bytes`;
  if (n < 1024 * 1024) return `${oneDecimal(n / 1024)} KB`;
  return `${oneDecimal(n / (1024 * 1024))} MB`;
}

function oneDecimal(x: number): string {
  // Avoid trailing ".0" — render integers as integers, otherwise
  // exactly one digit of fractional precision.
  if (Number.isInteger(x)) return x.toString();
  return x.toFixed(1);
}
