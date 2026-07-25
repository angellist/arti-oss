import { defineConfig } from "vitest/config";
import { resolve } from "node:path";

export default defineConfig({
  test: { environment: "node", include: ["lib/**/*.test.{ts,tsx}", "components/**/*.test.{ts,tsx}"] },
  resolve: { alias: { "@": resolve(__dirname, ".") } },
});
