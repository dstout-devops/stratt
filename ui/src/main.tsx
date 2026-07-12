import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  redirect,
  useNavigate,
} from "@tanstack/react-router";
import { useEffect, useState } from "react";
import "./app.css";
import { AppShell } from "./components/shell";
import { completeLogin } from "./auth/oidc";
import { ViewsList, ViewDetail, EntityDetail, ErrorLine } from "./routes/graph";
import { RunsList, RunDetail } from "./routes/runs";
import { WorkflowsList, WorkflowDetail, WorkflowRunDetail, GatesInbox } from "./routes/workflows";
import { TriggersList, TriggerDetail } from "./routes/triggers";
import { FindingsList, FindingDetail } from "./routes/findings";
import { BaselinesList, BaselineDetail } from "./routes/baselines";

const rootRoute = createRootRoute({
  component: () => (
    <AppShell>
      <Outlet />
    </AppShell>
  ),
});

// Every diagnostic state is URL-addressable (ADR-0003 L10): deep links
// round-trip through these routes.
const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/views" });
  },
});
const viewsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/views", component: ViewsList });
const viewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/views/$name",
  component: function ViewR() {
    const { name } = viewRoute.useParams();
    return <ViewDetail name={name} />;
  },
});
const entityRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/entities/$id",
  component: function EntityR() {
    const { id } = entityRoute.useParams();
    return <EntityDetail id={id} />;
  },
});
const runsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/runs", component: RunsList });
const runRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/runs/$id",
  component: function RunR() {
    const { id } = runRoute.useParams();
    return <RunDetail id={id} />;
  },
});
const workflowsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/workflows", component: WorkflowsList });
const workflowRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/workflows/$name",
  component: function WorkflowR() {
    const { name } = workflowRoute.useParams();
    return <WorkflowDetail name={name} />;
  },
});
const workflowRunRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/workflow-runs/$id",
  component: function WorkflowRunR() {
    const { id } = workflowRunRoute.useParams();
    return <WorkflowRunDetail id={id} />;
  },
});
const gatesRoute = createRoute({ getParentRoute: () => rootRoute, path: "/gates", component: GatesInbox });
const triggersRoute = createRoute({ getParentRoute: () => rootRoute, path: "/triggers", component: TriggersList });
const triggerRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/triggers/$name",
  component: function TriggerR() {
    const { name } = triggerRoute.useParams();
    return <TriggerDetail name={name} />;
  },
});

const findingsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/findings", component: FindingsList });
const findingRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/findings/$id",
  component: function FindingR() {
    const { id } = findingRoute.useParams();
    return <FindingDetail id={id} />;
  },
});
const baselinesRoute = createRoute({ getParentRoute: () => rootRoute, path: "/baselines", component: BaselinesList });
const baselineRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/baselines/$name",
  component: function BaselineR() {
    const { name } = baselineRoute.useParams();
    return <BaselineDetail name={name} />;
  },
});

// OIDC callback: exchange the code, restore the pre-login location.
const callbackRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/callback",
  component: function Callback() {
    const nav = useNavigate();
    const [err, setErr] = useState<unknown>(null);
    useEffect(() => {
      const code = new URLSearchParams(window.location.search).get("code");
      if (!code) {
        setErr(new Error("missing authorization code"));
        return;
      }
      completeLogin(code)
        .then((returnTo) => nav({ to: returnTo }))
        .catch(setErr);
    }, [nav]);
    return err ? <ErrorLine err={err} /> : <p style={{ color: "var(--text-muted)" }}>signing in…</p>;
  },
});

const router = createRouter({
  routeTree: rootRoute.addChildren([
    indexRoute,
    viewsRoute,
    viewRoute,
    entityRoute,
    runsRoute,
    runRoute,
    workflowsRoute,
    workflowRoute,
    workflowRunRoute,
    gatesRoute,
    triggersRoute,
    triggerRoute,
    findingsRoute,
    findingRoute,
    baselinesRoute,
    baselineRoute,
    callbackRoute,
  ]),
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
