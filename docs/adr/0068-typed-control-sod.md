# ADR 0068 — Typed Control library: Separation of Duties, and committer enrichment

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout)
- **Charter sections:** §1.1, §1.6, §2.4
- **Implements:** ADR-0061 §7.3 (increment 2) · builds on ADR-0067

## Context

The second typed Control primitive: **Separation of Duties (SoD)** — a built-in must-have. It is the first primitive that needs **ChangeContext enrichment** (the actor is already known; the *committers* are not), so this increment also wires the shared committer source. It drops into the ADR-0067 typed-control pattern (a nested spec pointer + a `controlFires` dispatch case + load validation) — no evaluation-model change, unlike Waiver/Quorum/BreakGlass.

## Decision

**1. `SoDSpec` v1 checks distinctness from `committers`.** `Control.SoD.DistinctFrom` names the role sets the actor must NOT belong to; the control **fires its Outcome** (an SoD violation) when the actor does. v1 supports `committers` — four-eyes at authoring: the requester may not also be a change author. Semantics are plain set membership: `actor ∈ committers` ⇒ violated; **no committers recorded ⇒ no dual-role conflict ⇒ not fired** (there is nothing to violate, so — unlike a TimeWindow with no time — SoD can judge and simply passes; not a fail-closed case). Approver-distinctness (requester ≠ approver) is a gate-decision-time concern, a follow-up.

**2. Committers are surfaced into `ChangeContext.Committers` from a `committers` launch param** (a list of Principal IDs, tolerating `[]string`/`[]any`). A CI or operator launching the change supplies the authors — the same pragmatic runtime source as TimeWindow's `workflow.Now`. Richer committer provenance (from the desired-state Git commit, or a trigger event) is a follow-up; SoD is correct given whatever committers are supplied.

**3. Load validation:** `validateSoD` requires a non-empty `distinctFrom` of recognised role sets, and the one-kind-per-control rule (ADR-0067) rejects a control that is both a CEL `When` and a typed SoD.

## Charter alignment

Upholds §1.1 (a typed primitive at the seam, not an ontology), §1.6 (SoD composes the one Principal model — actor and committers are Principals), and §2.4 (the fixed lattice is unchanged; SoD is another control feeding it). No new Named Kind (`SoDSpec` is a Control-payload shape). No new dependency. The closed-vocabulary discipline holds — `distinctFrom` accepts only recognised role sets, extended by ADR, never inline.

## Consequences

- **Positive:** four-eyes-at-authoring is declarable as typed data (`{sod: {distinctFrom: [committers]}, outcome: deny}`); the committer-enrichment seam is established (the shared prerequisite several future primitives/controls need); the typed-control pattern is confirmed to generalise cleanly (TimeWindow → SoD with no evaluation-model change).
- **Negative / trade-offs:** committers must be supplied by the launcher for SoD to detect a violation — an unsupplied committer list means "no conflict detected" (honest, but a stricter org may add a "require committers present" control); approver-distinctness and `min_distinct_teams` are not built yet.
- **Follow-ups:** the remaining §7.3 primitives that are *not* drop-in — **Waiver** (suppresses other controls + mandatory expiry), **Quorum** (M-of-N at the gate layer), **BreakGlass** (bypass modifier + post-review); committer provenance from Git/trigger events; approver-distinctness SoD at gate-decision time; blast-radius/risk ChangeContext enrichment (§7.6).

## Alternatives considered

- **Fail closed when committers are absent** — rejected: an empty committer set is a legitimate "no authors recorded", and `actor ∈ ∅` is honestly false; failing closed on every no-committer change would block legitimate API-launched changes. Data completeness (are committers supplied?) is a separate, explicit control if an org wants it.
- **Derive committers from desired-state Git provenance now** — deferred: it reaches into the compile/provenance layer; the launch-param source is the pragmatic v1, mirroring TimeWindow's `workflow.Now`.
