import { createFileRoute } from "@tanstack/react-router";
import { Placeholder } from "@/components/placeholder";

export const Route = createFileRoute("/fleet")({
  component: () => <Placeholder title="Fleet" slice="slice 3" />,
  staticData: { crumb: "Fleet" },
});
