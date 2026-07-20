import { createFileRoute } from "@tanstack/react-router";
import { RunDetail } from "@/screens/runs";

export const Route = createFileRoute("/runs/$id")({
  component: RunDetail,
  staticData: { crumb: "Run" },
});
