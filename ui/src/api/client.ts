// Thin typed client over /api/v1 — the same API the CLI, CI, and agents use
// (§1.6). Types are generated from core/api/openapi.yaml (task generate).
import type { components } from "./schema";
import { currentTokens, devPrincipal } from "../auth/oidc";

export type View = components["schemas"]["View"];
export type Entity = components["schemas"]["Entity"];
export type ViewResolution = components["schemas"]["ViewResolution"];
export type Run = components["schemas"]["Run"];
export type Workflow = components["schemas"]["Workflow"];
export type Step = components["schemas"]["Step"];
export type WorkflowRun = components["schemas"]["WorkflowRun"];
export type WorkflowRunDetail = components["schemas"]["WorkflowRunDetail"];
export type Gate = components["schemas"]["Gate"];
export type EntityDocument = components["schemas"]["EntityDocument"];
export type Trigger = components["schemas"]["Trigger"];
export type TriggerDetail = components["schemas"]["TriggerDetail"];

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

function headers(): HeadersInit {
  const h: Record<string, string> = { "Content-Type": "application/json" };
  const t = currentTokens();
  if (t) {
    h["Authorization"] = `Bearer ${t.accessToken}`;
  } else if (devPrincipal()) {
    h["X-Stratt-Principal"] = devPrincipal();
  }
  return h;
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers: headers(),
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    // Surface the API's own message verbatim — an authz denial names the
    // principal and grant; hiding that would hide diagnosis (§1.8).
    let msg = `${res.status}`;
    try {
      const e = (await res.json()) as { message?: string };
      if (e.message) msg = e.message;
    } catch {
      /* non-JSON error body */
    }
    throw new ApiError(res.status, msg);
  }
  if (res.status === 202 || res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const api = {
  listViews: () => call<View[]>("GET", "/views"),
  getView: (name: string) => call<View>("GET", `/views/${encodeURIComponent(name)}`),
  resolveView: (name: string) =>
    call<ViewResolution>("GET", `/views/${encodeURIComponent(name)}/entities`),
  getEntity: (id: string) => call<EntityDocument>("GET", `/entities/${encodeURIComponent(id)}`),
  listRuns: (limit = 100) => call<Run[]>("GET", `/runs?limit=${limit}`),
  getRun: (id: string) => call<Run>("GET", `/runs/${encodeURIComponent(id)}`),
  listWorkflows: () => call<Workflow[]>("GET", "/workflows"),
  getWorkflow: (name: string) => call<Workflow>("GET", `/workflows/${encodeURIComponent(name)}`),
  startWorkflowRun: (name: string) =>
    call<WorkflowRun>("POST", `/workflows/${encodeURIComponent(name)}/runs`),
  listWorkflowRuns: (limit = 100) => call<WorkflowRun[]>("GET", `/workflow-runs?limit=${limit}`),
  getWorkflowRun: (id: string) =>
    call<WorkflowRunDetail>("GET", `/workflow-runs/${encodeURIComponent(id)}`),
  listGates: (status?: string) =>
    call<Gate[]>("GET", `/gates${status ? `?status=${status}` : ""}`),
  decideGate: (id: string, approve: boolean, note: string) =>
    call<void>("POST", `/gates/${encodeURIComponent(id)}/decision`, { approve, note }),
  listTriggers: () => call<Trigger[]>("GET", "/triggers"),
  getTrigger: (name: string) => call<TriggerDetail>("GET", `/triggers/${encodeURIComponent(name)}`),
};
