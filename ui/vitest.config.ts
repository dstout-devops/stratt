import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { fileURLToPath, URL } from "node:url";

// Separate from vite.config.ts: tests need neither the Tailwind plugin nor the dev proxy, and
// jsdom + React 19 is happier on the threads pool with a generous teardown (gauntlet's lesson).
export default defineConfig({
  plugins: [react()],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test-setup.ts"],
    css: false,
    pool: "threads",
    teardownTimeout: 30_000,
    exclude: ["e2e/**", "node_modules/**", "dist/**"],
  },
});
