// Centralized, hierarchical query keys — the single source of truth queries, prefetch, optimistic
// updates, and (future) SSE invalidation all reference. This one decision makes the rest tractable
// (gauntlet's linchpin). Stable array shapes; objects last so partial-prefix invalidation works.

export type FindingStatus = "pending" | "open" | "resolved";
export type FindingFilter = { baseline?: string; status?: FindingStatus; limit?: number };

export const keys = {
  runs: (limit?: number) => ["runs", { limit }] as const,
  run: (id: string) => ["run", id] as const,
  runEvents: (id: string) => ["run", id, "events"] as const,
  workflowRuns: (limit?: number) => ["workflow-runs", { limit }] as const,
  workflowRun: (id: string) => ["workflow-run", id] as const,
  findings: (f: FindingFilter) => ["findings", f] as const,
  finding: (id: string) => ["finding", id] as const,
  findingEvidence: (id: string) => ["finding", id, "evidence"] as const,
  baselines: () => ["baselines"] as const,
  views: () => ["views"] as const,
  view: (name: string) => ["view", name] as const,
  viewEntities: (name: string, limit?: number) => ["view", name, "entities", { limit }] as const,
  entity: (id: string) => ["entity", id] as const,
  gates: (status?: string) => ["gates", { status }] as const,
  contracts: () => ["contracts"] as const,
  intents: () => ["intents"] as const,
  assignments: () => ["assignments"] as const,
  blueprints: () => ["blueprints"] as const,
  compile: () => ["compile"] as const,
  sources: () => ["sources"] as const,
  sites: () => ["sites"] as const,
  emitters: () => ["emitters"] as const,
  audit: (limit?: number) => ["audit", { limit }] as const,
  usage: () => ["usage"] as const,
  workflows: () => ["workflows"] as const,
  workflow: (name: string) => ["workflow", name] as const,
};
