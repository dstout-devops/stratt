import { createRouter } from "@tanstack/react-router";
import { routeTree } from "@/routeTree.gen";
import { Skeleton } from "@/components/ui/skeleton";

// The router is created from the PLUGIN-GENERATED route tree (src/routeTree.gen.ts, built from
// src/routes/** by @tanstack/router-plugin). defaultPreload: "intent" prefetches a route's data AND
// its code chunk on hover — the split is invisible in perceived latency (ADR-0090 §2).
export const router = createRouter({
  routeTree,
  defaultPreload: "intent",
  scrollRestoration: true,
  defaultPendingComponent: () => (
    <div className="p-6">
      <Skeleton className="h-40 w-full" />
    </div>
  ),
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
