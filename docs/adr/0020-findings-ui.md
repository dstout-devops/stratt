# ADR 0020 — Findings UI: the estate drift screen

- **Status:** Accepted
- **Date:** 2026-07-12
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.8, §3.1 (Findings = center-of-gravity screen);
  ADR-0003 Law L5; ADR-0019 (the API beneath it)

## Context

ADR-0019 shipped Baselines + Findings with a read-only API and named this
screen the next slice. ADR-0003 L5 is the spec: "Drift is a Finding: estate
roll-up → per-Entity diff → proposed fix. A clean state is visible without a
manual query; a drifted state descends to the observed-vs-expected diff and
the remediation Workflow."

## Decision

1. **`/findings` leads with the roll-up, not the list.** The top card shows
   every declared Baseline with a computed posture chip — **clean** (no live
   Findings), **damping** (pending only — drift observed, not yet fired,
   §4.3 made visible), **drifted** (open count + worst severity). Clean rows
   render exactly like drifted ones; the empty estate is a statement, not an
   absence (L5 vs Spacelift's invisible clean state). Posture is derived
   client-side from the two list endpoints — no new API surface.
2. **Findings table** below it, status-filterable (default **open**), reused
   verbatim inside Baseline Detail with the baseline filter pinned.
3. **Finding Detail = Evidence + descent:** DescentRail Baseline → Finding →
   Evidence Run (the full task-event stream); the redacted structure-only
   diff rendered as-is (the ADR-0019 truncation marker shows itself); the
   Baseline's damping threshold contextualizes the counter; **remediation is
   a link** to the Workflow screen — no launch affordance on a Finding
   (Flow 2: remediation behind Gates, deliberately).
4. **`/baselines`** list + detail clone the Triggers pattern (both CaC-only,
   read-only); Run screens gain the Baseline origin/descent rung
   (`run.baseline`, the triggered_by pattern).
5. **Chips:** Finding lifecycle and severity join the one shared state
   palette (`--state-*` tokens; dot + icon + label, color never alone). No
   new dependencies; hand-rendered columns today — `SchemaTable`-driven
   rendering from the Finding Contract remains the recorded upgrade
   (ADR-0003 L7/L8).

## Consequences

- Flow 2's dashboard is real: both check-mode and tofu-plan Baselines on one
  screen, verified live (drifted 50/warning + clean side by side; resolved
  audit rows reachable via the filter; every rung click-through).
- Findings list caps at the API's 500 limit without pagination — acceptable
  at v1 scale; virtualized/paginated Findings (L10) recorded for the
  estate-scale pass alongside SchemaTable.
- Baseline check params render verbatim in the detail — they are the CaC
  declaration (already public in Git; validated to carry no secret
  material), so this is the §1.8 posture, not a leak.
