// Next.js instrumentation hook — runs once per server process at startup.
// In deployed pods, rewire console output to one JSON line per call so the
// Datadog agent ingests each log call as a single, parseable event (see
// lib/structured-console.ts for the why). Guarded to the nodejs runtime
// (the edge/middleware runtime has no node:util) and to production builds
// (local dev keeps readable multi-line output).
export async function register() {
  if (process.env.NEXT_RUNTIME === "nodejs" && process.env.NODE_ENV === "production") {
    const { patchConsole } = await import("./lib/structured-console");
    patchConsole();
  }
}
