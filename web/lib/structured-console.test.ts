import { describe, expect, it } from "vitest";
import { toLine } from "./structured-console";

// The point of structured console output: a multi-line Node error dump must
// travel as ONE physical line (one Datadog event), carrying its status in
// the JSON instead of the output stream.
describe("toLine", () => {
  it("keeps a multi-line stack trace on a single physical line", () => {
    const err = new Error("connect ECONNREFUSED ::1:8095");
    const line = toLine("error", ["proxy failed", err]);
    expect(line).not.toContain("\n");
    const parsed = JSON.parse(line);
    expect(parsed.status).toBe("error");
    expect(parsed.message).toContain("proxy failed");
    expect(parsed.message).toContain("ECONNREFUSED");
    // The stack survives inside the message (escaped, not dropped).
    expect(parsed.message).toContain("at ");
  });

  it("formats printf-style args like console does", () => {
    const parsed = JSON.parse(toLine("info", ["ready on port %d", 3030]));
    expect(parsed.status).toBe("info");
    expect(parsed.message).toBe("ready on port 3030");
  });

  it("flattens an AggregateError with nested causes to one line", () => {
    const agg = new AggregateError(
      [new Error("connect ECONNREFUSED ::1:8095"), new Error("connect ECONNREFUSED 127.0.0.1:8095")],
      "all attempts failed",
    );
    const line = toLine("error", [agg]);
    expect(line).not.toContain("\n");
    expect(JSON.parse(line).message).toContain("ECONNREFUSED");
  });
});
