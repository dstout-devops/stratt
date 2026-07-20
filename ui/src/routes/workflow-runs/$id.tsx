import { createFileRoute } from "@tanstack/react-router";
import { Placeholder } from "@/components/placeholder";

export const Route = createFileRoute("/workflow-runs/$id")({
  component: () => <Placeholder title="WorkflowRun" slice="the next build step" />,
  staticData: { crumb: "WorkflowRun" },
});
