import { createRootRoute } from "@tanstack/react-router";
import { AppShell } from "@/components/shell";

// The root route — the persistent AppShell (nav + DescentRail + ⌘K palette). The crumb type
// augmentation lives here so every route file can set staticData.crumb for the DescentRail.
declare module "@tanstack/react-router" {
  interface StaticDataRouteOption {
    crumb?: string;
  }
}

export const Route = createRootRoute({ component: AppShell });
