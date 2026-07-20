import { createFileRoute } from "@tanstack/react-router";
import { WorkflowsList } from "@/screens/workflows";

export const Route = createFileRoute("/workflows/")({
  component: WorkflowsList,
  staticData: { crumb: "Workflows" },
});
