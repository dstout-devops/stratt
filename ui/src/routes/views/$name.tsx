import { createFileRoute } from "@tanstack/react-router";
import { ViewDetail } from "@/screens/graph";

export const Route = createFileRoute("/views/$name")({
  component: ViewDetail,
  staticData: { crumb: "View" },
});
