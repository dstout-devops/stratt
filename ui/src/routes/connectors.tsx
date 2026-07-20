import { createFileRoute } from "@tanstack/react-router";
import { Placeholder } from "@/components/placeholder";

export const Route = createFileRoute("/connectors")({
  component: () => <Placeholder title="Connectors" slice="slice 3" />,
  staticData: { crumb: "Connectors" },
});
