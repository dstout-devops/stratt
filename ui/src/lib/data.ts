import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { keys, type FindingFilter } from "@/lib/keys";

// One queryOptions factory per resource. The SAME factory feeds useQuery, hover-prefetch, and route
// preload — so those paths share a cache key and never double-fetch (ADR-0090 §2). The queryFn body
// is the only place the transport appears; swapping OpenAPI for anything else touches only here.

export const runsQuery = (limit = 50) =>
  queryOptions({
    queryKey: keys.runs(limit),
    queryFn: async () => unwrap(await api.GET("/runs", { params: { query: { limit } } })),
    staleTime: 15_000,
  });

export const runQuery = (id: string) =>
  queryOptions({
    queryKey: keys.run(id),
    queryFn: async () => unwrap(await api.GET("/runs/{id}", { params: { path: { id } } })),
  });

export const workflowRunsQuery = (limit = 50) =>
  queryOptions({
    queryKey: keys.workflowRuns(limit),
    queryFn: async () => unwrap(await api.GET("/workflow-runs", { params: { query: { limit } } })),
    staleTime: 15_000,
  });

export const workflowRunQuery = (id: string) =>
  queryOptions({
    queryKey: keys.workflowRun(id),
    queryFn: async () => unwrap(await api.GET("/workflow-runs/{id}", { params: { path: { id } } })),
  });

export const findingsQuery = (f: FindingFilter = {}) =>
  queryOptions({
    queryKey: keys.findings(f),
    queryFn: async () => unwrap(await api.GET("/findings", { params: { query: f } })),
    staleTime: 15_000,
  });

export const findingQuery = (id: string) =>
  queryOptions({
    queryKey: keys.finding(id),
    queryFn: async () => unwrap(await api.GET("/findings/{id}", { params: { path: { id } } })),
  });

export const findingEvidenceQuery = (id: string) =>
  queryOptions({
    queryKey: keys.findingEvidence(id),
    queryFn: async () =>
      unwrap(await api.GET("/findings/{id}/evidence", { params: { path: { id } } })),
  });

export const baselinesQuery = () =>
  queryOptions({
    queryKey: keys.baselines(),
    queryFn: async () => unwrap(await api.GET("/baselines")),
  });

export const viewsQuery = () =>
  queryOptions({
    queryKey: keys.views(),
    queryFn: async () => unwrap(await api.GET("/views")),
  });

export const viewQuery = (name: string) =>
  queryOptions({
    queryKey: keys.view(name),
    queryFn: async () => unwrap(await api.GET("/views/{name}", { params: { path: { name } } })),
  });

export const viewEntitiesQuery = (name: string, limit = 500) =>
  queryOptions({
    queryKey: keys.viewEntities(name, limit),
    queryFn: async () =>
      unwrap(
        await api.GET("/views/{name}/entities", { params: { path: { name }, query: { limit } } }),
      ),
  });

export const entityQuery = (id: string) =>
  queryOptions({
    queryKey: keys.entity(id),
    queryFn: async () => unwrap(await api.GET("/entities/{id}", { params: { path: { id } } })),
  });

export const gatesQuery = (status: "pending" | "approved" | "denied" | "expired" = "pending") =>
  queryOptions({
    queryKey: keys.gates(status),
    queryFn: async () => unwrap(await api.GET("/gates", { params: { query: { status } } })),
    staleTime: 10_000,
  });

// Contracts are the schema-driven-rendering source (ADR-0003 L7/L8): every pinned Facet/Contract
// schema, indexed by name, feeds SchemaForm/schema-renderer. Pinned by hash → long stale time.
export const contractsQuery = () =>
  queryOptions({
    queryKey: keys.contracts(),
    queryFn: async () => unwrap(await api.GET("/contracts")),
    staleTime: 5 * 60_000,
  });

// Desired-state / intent layer (read; the Git desired state is authoritative, §1.2).
export const intentsQuery = () =>
  queryOptions({
    queryKey: keys.intents(),
    queryFn: async () => unwrap(await api.GET("/intents")),
  });

export const assignmentsQuery = () =>
  queryOptions({
    queryKey: keys.assignments(),
    queryFn: async () => unwrap(await api.GET("/assignments")),
  });

export const blueprintsQuery = () =>
  queryOptions({
    queryKey: keys.blueprints(),
    queryFn: async () => unwrap(await api.GET("/blueprints")),
  });

// Compile status carries the L3 membership deltas (which Entities join/leave each Assignment) —
// the "plan preview" surface, rendered with the plan/drift palette.
export const compileQuery = () =>
  queryOptions({
    queryKey: keys.compile(),
    queryFn: async () => unwrap(await api.GET("/compile")),
    staleTime: 15_000,
  });
