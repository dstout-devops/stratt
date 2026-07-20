import { createFileRoute } from "@tanstack/react-router";
import { IntentsPage } from "@/screens/intents";

export const Route = createFileRoute("/intents/")({
  component: IntentsPage,
  staticData: { crumb: "Intents" },
});
