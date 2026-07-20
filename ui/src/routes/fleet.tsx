import { createFileRoute } from "@tanstack/react-router";
import { FleetPage } from "@/screens/fleet";

export const Route = createFileRoute("/fleet")({
  component: FleetPage,
  staticData: { crumb: "Fleet" },
});
