# ADR 0028 — View-scoped execution authz (full OpenFGA)

- **Status:** Accepted
- **Date:** 2026-07-13
- **Deciders:** Project steward (dstout)
- **Charter sections:** §8 (Phase-3 "full OpenFGA (View-scoped execution)"),
  §2.5 (Authorization — ReBAC, View-scoped execution, deny-by-default), §1.6
  (one authz model / audit), §1.2 (projections); ADR-0009 (authz model),
  ADR-0026 (cancel posture), ADR-0018 (Triggers), ADR-0019 (Baselines)

## Context

Opens Phase 3 by closing the platform's most-deferred gap: **execution was
authenticated but not authorized.** Every launch path — `StartRun`,
`startWorkflowRun`, the AWX façade, Trigger fires, Baseline checks — ran any
Principal against any View. The charter's authorization model (§2.5) is
**View-scoped execution**: "may run this Workflow, but only against Entities in
this View." The OpenFGA model (ADR-0009) had only `principal`/`org`/`team`/
`credential_ref` — no `view` type, no execute relation — which is precisely why
ADR-0009 left `StartRun` ungated and ADR-0026 had to make cancel
authenticated-only (gating on the unmodeled `run` type would have failed closed
for everyone). This slice adds the missing axis and enforces it, making
execution **deny-by-default** — the foundation for the "security review" promote
gate.

**Binding decision (steward):** strictly View-scoped. `runner` is granted
per-View (directly to a Principal or to a team's members); a View's owner-team
admin implies `runner` on that View; **org/team admin does NOT blanket-grant**
runner on all Views. No bypass — honoring "only against Entities in this View."

## Decision

1. **Model — a `view` type + `runner` relation** (two artifacts kept in lockstep
   by `TestOpenFGAAgreement`): `authzmodel.json` gains `view` (owner_team; `admin
   = direct ∪ owner_team.admin`; `reader = direct ∪ admin`; `runner = direct ∪
   admin`, both assignable from `[principal, team#member]`), and the in-process
   `TupleAuthorizer` gains the matching implied cases. `RelationRunner = "runner"`.
   The **only** path to `runner` is a direct grant, a `team#member` userset, or
   `admin` (direct-view-admin or owner-team-admin) — there is **no `org#admin →
   runner on all views`** rule. An org admin reaches a View only through the
   legitimate ownership chain (its org → the owning team → the View), never by
   blanket bypass.
2. **Enforcement — one chokepoint covers all six launch paths (§1.6).** Every
   launch funnels through the `RunAgainstView` Temporal workflow (API/façade via
   `LaunchRun`; API `startWorkflowRun` + Trigger-workflows via `RunDAG`, where each
   actuation Step becomes its own `RunAgainstView` child with its own `ViewName`;
   Trigger-Runs and Baseline checks via direct `ExecuteWorkflow`). A new
   `CheckExecutionGrant` activity — modeled exactly on the credential `use` check
   in `ResolveCredentials` — runs **first** in `RunAgainstView`:
   `Check(in.Principal, runner, "view:"+in.ViewName)`; an empty or ungranted
   Principal is a **non-retryable `ExecutionDenied`** that fails the Run before any
   target is resolved or pod spawned. Because DAG Steps are per-View children,
   per-Step Views are checked automatically.
3. **Handler fast-403 (UX + closes the ADR-0009/0026 flags).** The existing
   `Server.requireGrant` gates `StartRun` (target View), `StartWorkflowRun` (each
   actuation Step's View), and — **re-introducing the object-gating ADR-0026
   removed** — `CancelRun` (the Run's View: you may cancel Runs against Views you
   may launch against). The AWX façade mirrors these via a symmetric
   `requireRunner`, so the compat surface is never a weaker authz path (§1.6). The
   handler check is fail-fast UX; the `RunAgainstView` check is the universal
   enforcement that also covers the no-handler paths (Trigger, Baseline).
4. **Tuples are CaC (§1.2).** `runner` grants live in the declarations repo's
   `authz/tuples.yaml` — the same manifest that grants credential `use` — synced to
   OpenFGA by the existing `SyncTuples` as a rebuildable projection.

## Consequences

- **Live-verified (dev harness, real OpenFGA + kind + EE):**
  - A Principal **granted** `runner` on `dev-vms` → `POST /runs` `201` → the Run
    runs to `succeeded` (the chokepoint allows).
  - An **ungranted** Principal → `403` `"principal nobody lacks runner on
    view:dev-vms"` at the door.
  - **No-handler path:** firing the `quarantine-host` Trigger (which launches
    directly via `ExecuteWorkflow` under Principal `admin`) → the Run **failed** at
    the chokepoint: `"principal admin lacks runner on view:alerted-host
    (ExecutionDenied)"` — proving all paths are gated under one model.
  - `CancelRun`: ungranted → `403`, granted → `202` → `canceled` (closes ADR-0026).
  - The **OpenFGA server agrees with the in-process evaluator across 152 checks**
    (`TestOpenFGAAgreement`), including all `view` runner/reader/admin cases; the
    no-org-bypass property is unit-proven (an org admin has no `runner` on a View
    owned by a team in a different org).
- **Deny-by-default posture change:** existing Triggers/Baselines/direct launches
  now require explicit `runner` grants or they fail `ExecutionDenied`. This is the
  intended §2.5 posture ("Deny is the default"), not a regression.
- **Charter posture:** View-scoped execution with no bypass (§2.5); one
  `authz.Authorizer.Check` seam at one chokepoint, façade symmetric (§1.6); CaC
  tuples projected to OpenFGA (§1.2).

## Deferred / fast-follow (documented)
- A `run` object type for finer-grained cancel/read grants (v1 gates cancel on the
  Run's View, which is sufficient and closes ADR-0026).
- `reader`-gated View reads: the model now carries a `view` `reader` relation, but
  the View GET endpoints stay open in v1; wiring read-authz on them is a follow-up.
- SCIM / IdP group sync feeding `team` membership (a separate Phase-3 item).

## Runway after
Phase-3 board continues: Evidence store (object-lock) + CIS pack; Sites (NATS
leaf) + pull agent/Bundles; Intent/Certificate GA (the promote flagship);
audit→Splunk; HA/DR; SCIM.
