import { createFileRoute } from "@tanstack/react-router";
import { WorkflowDetail } from "@/screens/workflows";

export const Route = createFileRoute("/workflows/$name")({
  component: WorkflowDetail,
  staticData: { crumb: "Workflow" },
});
