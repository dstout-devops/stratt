import { createFileRoute } from "@tanstack/react-router";
import { AdminPage } from "@/screens/admin";

export const Route = createFileRoute("/admin")({
  component: AdminPage,
  staticData: { crumb: "Admin" },
});
