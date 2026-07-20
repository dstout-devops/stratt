import { createFileRoute } from "@tanstack/react-router";
import { WorkflowRunDetail } from "@/screens/workflow-runs";

export const Route = createFileRoute("/workflow-runs/$id")({
  component: WorkflowRunDetail,
  staticData: { crumb: "WorkflowRun" },
});
