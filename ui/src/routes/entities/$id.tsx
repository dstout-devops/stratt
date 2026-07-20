import { createFileRoute } from "@tanstack/react-router";
import { EntityDetail } from "@/screens/graph";

export const Route = createFileRoute("/entities/$id")({
  component: EntityDetail,
  staticData: { crumb: "Entity" },
});
