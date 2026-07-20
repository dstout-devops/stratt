import { createFileRoute } from "@tanstack/react-router";
import { RunsList } from "@/screens/runs";

export const Route = createFileRoute("/runs/")({
  component: RunsList,
  staticData: { crumb: "Runs" },
});
