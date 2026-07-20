import { defineConfig } from "@playwright/test";

// Bench, not functional E2E (gauntlet's posture): assert the ADR-0003 L10 / charter Phase-0
// latency budgets (View query < 200 ms; route FCP). Run against a dev server with a seeded
// backend. Chromium-only, no retries — a perf regression must fail, not flake-retry away.
export default defineConfig({
  testDir: "./e2e/bench",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  use: { baseURL: process.env.STRATT_UI_URL ?? "http://localhost:5173" },
});
