import { createFileRoute } from "@tanstack/react-router";
import { ConnectorsPage } from "@/screens/connectors";

export const Route = createFileRoute("/connectors")({
  component: ConnectorsPage,
  staticData: { crumb: "Connectors" },
});
