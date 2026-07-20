import { createFileRoute } from "@tanstack/react-router";
import { ApprovalsInbox } from "@/screens/approvals";

export const Route = createFileRoute("/runs/approvals")({
  component: ApprovalsInbox,
  staticData: { crumb: "Approvals" },
});
