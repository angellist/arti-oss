import { formatWithOptions } from "node:util";

type Status = "info" | "warn" | "error";

// toLine serializes one console call to a single JSON line. Deployed pods
// need this because the Datadog agent ingests one log event per physical
// line: a Node stack trace printed by console.error (e.g. the AggregateError
// Next.js emits when a rewrite proxy can't reach arti-server) became ~18
// separate ERROR events, inflating error counts ~18x per incident.
// JSON.stringify escapes the newlines, so the whole trace travels as one
// event, and Datadog reads status/message from the JSON instead of guessing
// status from the output stream (which classified plain-text INFO lines on
// stderr as errors).
export function toLine(status: Status, args: unknown[]): string {
  const message = formatWithOptions(
    { depth: 6, breakLength: Number.POSITIVE_INFINITY },
    ...args,
  );
  return JSON.stringify({ status, message });
}

// patchConsole rewires the global console's log/info/warn/error to emit one
// JSON line per call. Called from instrumentation.ts on the nodejs runtime in
// production only — local dev keeps Node's readable multi-line output.
export function patchConsole() {
  const routes: Array<[("log" | "info" | "warn" | "error"), Status, NodeJS.WriteStream]> = [
    ["log", "info", process.stdout],
    ["info", "info", process.stdout],
    ["warn", "warn", process.stderr],
    ["error", "error", process.stderr],
  ];
  for (const [method, status, stream] of routes) {
    console[method] = (...args: unknown[]) => {
      stream.write(`${toLine(status, args)}\n`);
    };
  }
}
