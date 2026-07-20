import { createFileRoute } from "@tanstack/react-router";
import { IntentDetail } from "@/screens/intents";

export const Route = createFileRoute("/intents/$name")({
  component: IntentDetail,
  staticData: { crumb: "Intent" },
});
