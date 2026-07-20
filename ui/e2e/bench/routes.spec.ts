import { test, expect } from "@playwright/test";

// L10 "perceived latency is a trust budget" (ADR-0003 / ADR-0090 §10). This is a BENCH, not a
// functional E2E: it asserts each descent-spine route reaches first contentful paint within budget,
// so a perceived-latency regression fails CI rather than flaking away. Run against a dev server with
// a seeded backend: `task ui:dev` (with strattd up) then `task -d ui bench`. Chromium, no retries.
//
// Budgets are generous first-paint ceilings for the app shell; the charter's hard gates (View query
// < 200 ms @ 50k Entities, pod-spawn p95 < 5 s) are asserted backend-side. As real datasets land,
// tighten these and add data-scale fixtures.

const ROUTES: { path: string; budgetMs: number }[] = [
  { path: "/runs", budgetMs: 1500 },
  { path: "/findings", budgetMs: 1500 },
  { path: "/graph", budgetMs: 1500 },
  { path: "/runs/approvals", budgetMs: 1500 },
];

for (const { path, budgetMs } of ROUTES) {
  test(`route ${path} paints within ${budgetMs}ms`, async ({ page }) => {
    const start = Date.now();
    await page.goto(path, { waitUntil: "domcontentloaded" });
    // The shell's nav is the first meaningful paint; wait for it.
    await page.getByRole("navigation").first().waitFor({ state: "visible" });
    const elapsed = Date.now() - start;
    expect(elapsed, `${path} first paint`).toBeLessThan(budgetMs);
  });
}
