# ADR 0011 — Workflows + Gates v1: Step DAGs with human approval

- **Status:** Accepted
- **Date:** 2026-07-11
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.2, §1.6, §1.8, §2 (Workflow, Step, Gate), §2.5, §3, §8 (Phase 1)

## Context

Charter §2 defines the shapes: **Step** — one contracted invocation; **Workflow** —
"Temporal-backed DAG of Steps with success/failure/always edges, Gates (human/policy
approval), convergence, nesting"; §3 gives approvals to Temporal. The prerequisites
landed in slices 5–6: real Principals (Gates need an approver identity) and the
EnsureRun pattern (any Temporal execution can mint its own Run row).

## Decision

**v1 ships the DAG subset that the current spine supports honestly:** `needs:` edges
with `when: success | failure | always`, human Gate Steps, parallel execution of
independent branches, one Run per actuation Step. **Deferred, not dropped:** cross-Step
output binding and input Contracts (the tofu provision→configure seam — needs Phase-2
Contract machinery), Workflow nesting, convergence, policy Gates, Trigger→Workflow
starts, API-declared Workflows.

1. **CaC-only declarations, read-only declaration API.** `workflows/*.yaml` in the
   desired-state repo, projected to `graph.workflow` (plan/apply/prune, per-kind
   max-prune guard). Unlike Triggers there is no impersonation concern — the *launching*
   Principal rides every Step's credential `use` check — so CaC-only here is scope
   symmetry, not an authz requirement. The wire `DesiredState` carries `workflows`
   (slice-6 lesson: the CLI sends the checkout verbatim).
2. **WorkflowRun** is the execution record (composed from two Named Kinds, as
   Workflow → Run composition): `graph.workflow_run` + per-Step linkage columns
   `graph.run.workflow_run_id/step_name` — the §1.8 Workflow → Run rung is a queryable
   column, and each Step's child Temporal workflow id is deterministic
   (`wfrun-<id>-<step>`).
3. **RunDAG execution model.** The spec is pinned into workflow state at start (a Git
   edit mid-flight affects future executions only). Steps launch as soon as their needs
   are terminal and the when-condition holds — completion-driven scheduling, no round
   barriers, so a pending Gate on one branch never stalls an independent branch.
   Actuation Steps run as **child `RunAgainstView` workflows** (EnsureRun creates the
   Run row): slicing, credential projection, per-target results, and event streams all
   reuse slices 3–6 unchanged. Edge semantics: success = all needs succeeded; failure =
   ≥1 need failed; always = any terminal outcome; a skipped need satisfies neither
   success nor failure (skips cascade down success chains).
4. **Gates.** A Gate Step opens a `graph.gate` row (approver policy pinned at creation —
   the audit shows what authorized the decision) and waits on a per-Step Temporal
   signal, racing the declared `timeoutSeconds` (expired = denied; 0 waits forever).
   The workflow is the row's single writer after creation, so every transition is in
   its §1.8 history. **Approver authorization** is declaration-scoped: the deciding
   Principal must be listed in `approvers.principals` or be a `member` of one of
   `approvers.teams`, checked through the existing `authz.Authorizer` seam — identical
   semantics on the tuple evaluator and OpenFGA, **no authorization-model change**.
   The API (`POST /gates/{id}/decision`) enforces this, requires an authenticated
   Principal, 409s an already-decided Gate, and delivers the decision as a signal
   (202 — the workflow records it). Two near-simultaneous authorized decisions race
   benignly: the first signal wins; the recorded decision names its Principal.
5. **Terminal status is raw:** a WorkflowRun fails if any Step failed, was denied, or
   expired — even when a `when: failure` cleanup branch ran green. A handled failure is
   still a failure on the record (§1.8).
6. **No new dependencies.** Temporal child workflows, signals, and selectors; testify
   (already in the module graph transitively) is promoted to a direct **test** dependency
   by the Temporal test-environment tests.

## Consequences

- The approval inbox is `GET /gates?status=pending`; the descent view is
  `GET /workflow-runs/{id}` (per-Step Run/Gate ids; Runs tail via the existing
  `/runs/{id}/events` SSE).
- A strattd restart is survivable end-to-end: the DAG, its pending Gates, and their
  timers live in Temporal history, not process memory.
- Gate approver teams are resolved at decision time — a team-membership revoke in Git
  takes effect on the next decision attempt within a reconcile interval.
- The explicit `approvers.principals` match is an inline check in the decision handler
  (declaration-scoped policy data), deliberately not a distinct OpenFGA object type yet;
  when View-scoped execution lands (Phase 2/3), both the principals path and the team
  path must fold into the single authorization model (charter-guardian note).
- `WorkflowRun` is a composed term (Workflow × Run), not itself a frozen §2 Named Kind —
  to be blessed or renamed deliberately at the vocabulary freeze, not by drift
  (charter-guardian note).
- Follow-ups: Trigger kind for Workflow starts; SSE/notification for pending Gates
  (Views UI slice); OpenFGA-native Gate relations when View-scoped execution lands
  (Phase 2/3); Workflow-level cancel API; output binding via Contracts (Phase 2).
