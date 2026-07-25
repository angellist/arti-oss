import { defineConfig } from "vitest/config";
import { resolve } from "node:path";

export default defineConfig({
  // middleware.ts must live at the project root for Next to pick it up, so
  // its test does too.
  test: {
    environment: "node",
    include: ["lib/**/*.test.{ts,tsx}", "components/**/*.test.{ts,tsx}", "*.test.{ts,tsx}"],
  },
  resolve: { alias: { "@": resolve(__dirname, ".") } },
});
