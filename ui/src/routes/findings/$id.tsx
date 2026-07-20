import { createFileRoute } from "@tanstack/react-router";
import { FindingDetail } from "@/screens/findings";

export const Route = createFileRoute("/findings/$id")({
  component: FindingDetail,
  staticData: { crumb: "Finding" },
});
