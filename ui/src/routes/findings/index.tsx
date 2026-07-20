import { createFileRoute } from "@tanstack/react-router";
import { FindingsList } from "@/screens/findings";

export const Route = createFileRoute("/findings/")({
  component: FindingsList,
  staticData: { crumb: "Findings" },
});
