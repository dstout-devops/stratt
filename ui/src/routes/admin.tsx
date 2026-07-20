import { createFileRoute } from "@tanstack/react-router";
import { Placeholder } from "@/components/placeholder";

export const Route = createFileRoute("/admin")({
  component: () => <Placeholder title="Admin" slice="slice 3" />,
  staticData: { crumb: "Admin" },
});
