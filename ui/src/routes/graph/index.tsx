import { createFileRoute } from "@tanstack/react-router";
import { ViewsList } from "@/screens/graph";

export const Route = createFileRoute("/graph/")({
  component: ViewsList,
  staticData: { crumb: "Graph" },
});
