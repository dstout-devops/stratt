# ADR 0063 — Policy Step & DAG dispatch v1: the PDP as a synchronous checkpoint

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout)
- **Charter sections:** §1.6, §1.8, §2 (Step, Gate), §2.4, §5
- **Implements:** ADR-0061 §7.2 (increment a) · builds on ADR-0062

## Context

ADR-0062 shipped the built-in PDP evaluator in isolation. This ADR makes it *reachable at runtime*: a Workflow Step can be a **policy checkpoint** that evaluates the PDP over a `ChangeContext` assembled from the run and gates the DAG on the outcome. It is the first increment of ADR-0061 §7.2 — deliberately the **automated `ALLOW`/`DENY`** path; wiring `REQUIRE_APPROVAL` to the human Gate flow (increment b), OpenFGA deny-composition at the launch seam (increment c), and the framework-compiled mandatory floors (increment d) are the following increments under 0061.

## Decision

**1. A new, mutually-exclusive Step shape `Policy *PolicySpec`** (`types/workflow.go`) alongside `Gate` / `Action` / actuation. `PolicySpec{Controls []types.Control}` carries inline controls for v1 (target-anchored `governance.controls` Facets are ADR-0061 §7.6). `ValidateWorkflow` (`desiredstate`) gains the policy arm in its exclusive-shape switch and **CEL-compiles every control's `When` at load — fail-closed at parse** (§1.8, mirroring `validatePlanPinning` and the trigger-rule compile), via `policy.ValidateControls`.

**2. `RunDAG` dispatches a policy Step synchronously.** The launch switch (`orchestrate/workflow.go`) gains `case step.Policy != nil: status = runPolicyStep(...)`. Unlike a Gate (which blocks on a human signal), a policy Step runs the **`EvaluatePolicy` activity** — non-deterministic CEL compile + `time.Now` must leave the workflow goroutine — which calls `policy.Evaluate` and returns the `Decision`. The `ChangeContext` is **assembled deterministically in the workflow goroutine** from `DAGInput` (actor = launching Principal; the `{{.event}}`/`{{.launch}}` inputs surface as `labels`/`environment`); richer enrichment (blast-radius from View membership, per-target criticality) is progressive and sparse/fail-safe (ADR-0061 M4).

**3. Outcome → step status (v1):** `allow` ⇒ step succeeds; `deny` ⇒ step fails. `require_approval` / `escalate` **fail closed** with a clear reason (`"approval required — interactive wiring lands in §7.2b"`) — safe and honest for this increment, never a silent pass. The mapping is a pure exported `PolicyStepStatus(outcome)` so it is unit-tested directly.

**4. Composition invariants preserved.** A policy Step can only **restrict** an already-authorised run — it never grants, so a policy `allow` cannot override the OpenFGA grant enforced at launch (§1.6 / ADR-0061 M2 holds by construction). The decision's reasons are logged by the activity so the Intent→Run→step descent explains the outcome (§1.8); durable recording as a Finding/Evidence is increment b/d.

## Charter alignment

Upholds §1.8 (fail-the-file at load; every runtime decision is logged with structured reasons), §2.4 (the evaluator's fixed lattice is unchanged — no precedence added), §1.6 (policy composes with, never replaces, the launch-time OpenFGA grant), and §5 (a policy Step is a Gate-family checkpoint; `deny`/`require_approval` block, they never auto-launch). No new Named Kind (`PolicySpec` is a Step field shape). No new dependency.

## Consequences

- **Positive:** governance is enforceable at runtime for the automated allow/deny case (e.g. "deny a prod change whose blast radius exceeds N"); the DAG walk, edge conditions, and terminal-status machinery are reused unchanged; the outcome mapping and control validation are unit-tested, and the dispatch is covered through the existing `dagTestEnv` harness.
- **Negative / trade-offs:** `require_approval`/`escalate` block rather than prompt until §7.2b; the v1 `ChangeContext` is sparse (actor + labels/environment), so blast-radius-driven controls await the enrichment increment; controls are inline on the Step until the target-anchored Facet (§7.6).
- **Follow-ups:** ADR-0061 §7.2b (REQUIRE_APPROVAL → open a human Gate carrying the approval obligation; fold the 0011 inline approver check into `authz.Authorizer`), §7.2c (launch-seam deny-composition), §7.2d (framework-compiled mandatory floors + durable Finding/Evidence recording), §7.6 (target-anchored `governance.controls` Facet + richer ChangeContext).

## Alternatives considered

- **Evaluate the policy inline in the workflow goroutine** — rejected: CEL compilation and `time.Now` are non-deterministic and would break Temporal replay determinism; policy evaluation belongs in an activity.
- **Map `require_approval` straight onto a Gate now** — deferred to §7.2b: it needs the approval obligation threaded into gate creation and the approver-check unification; folding it here would bloat the increment. Fail-closed is the safe interim.
- **Open the policy Step to arbitrary Payload (tool content)** — rejected (§1.1/ADR-0046): the PDP sees the typed Envelope/`ChangeContext` only.
