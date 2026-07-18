# ADR 0071 — Quorum (M-of-N): a gate threshold, not an evaluator Control

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout)
- **Charter sections:** §1.6, §1.8, §2 (Gate), §5
- **Implements:** ADR-0061 §7.3 (increment 5, final) · builds on ADR-0064/0011

## Context

The last §7.3 primitive: **Quorum** — require M-of-N distinct approvals before a Gate proceeds. The §7.3 cross-cut analysis flagged it as the outlier: it is **not an evaluator Control** (it neither fires an outcome nor suppresses one), so it does not live in `policy.Evaluate`. It is a property of the **Gate** — the approval mechanism — and so it lands at the orchestration layer, applying uniformly to a declared Gate Step and a policy-opened Gate (ADR-0064).

## Decision

**1. Quorum is a gate threshold, realised in one place: `awaitGate`.** A Gate proceeds only after `threshold` **distinct** authorized approvals (`threshold < 1 ⇒ 1`, the single-approval default — fully backward-compatible). Sources of the threshold: a declared Gate's `GateSpec.Threshold`, and a policy `require_approval` obligation's `count` param (ADR-0064's deferred `count`, now honoured). `awaitGate` accumulates the distinct approving principals in **replay-safe Temporal state**, so a re-approval by the same principal never double-counts. Any single **DENY short-circuits** to failure regardless of threshold; the timeout expires the Gate.

**2. No DB migration, no API change.** `DecideGate` already authorizes one approver (`authz.ApproverAuthorized`, ADR-0064) and signals the workflow; quorum just means it is called N times, each signal accumulating in the workflow. The gate row stays `pending` until the workflow finalises it, so every in-quorum `DecideGate` sees `pending` and is accepted; once the threshold is met and recorded, further calls 409 as already-decided. Distinctness is enforced workflow-side (the approving-principal set), so N approvals from one principal cannot satisfy a quorum of N.

**3. Quorum is deliberately NOT a typed Control.** It has no predicate and no suppression — modelling it as a Control would misfit the evaluator. It is the approval mechanism's cardinality, so it belongs on the Gate. This is the honest realisation of ADR-0061 §7.3's Quorum, at the layer it actually operates.

## Charter alignment

Upholds §1.6 (each of the N approvals is authorized through the one `ApproverAuthorized` seam; one audit path), §1.8 (the Gate's wait, its threshold, and its final decision are all on the §1.8 descent — the durable Temporal history records each accumulated approval), §2 (no new Named Kind — a quorum Gate is a Gate; `Threshold` is a field on the existing `GateSpec`), and §5 (a quorum Gate still blocks on humans, never auto-launches). No new dependency, no schema change.

## Consequences

- **Positive:** M-of-N sign-off is declarable on any Gate (`threshold: 2`) and on a policy require_approval (`count: 2`); it reuses the entire Gate/authz/audit/descent stack; distinctness and deny-short-circuit are enforced in replay-safe state; fully backward-compatible (existing single-approval Gates are `threshold: 1`). **This completes the §7.3 typed Control library.**
- **Negative / trade-offs:** v1 quorum is plain N-distinct-principals — the SoD-at-gate refinements (approver ≠ requester, approvers from ≥K distinct teams) are a follow-up; the gate row does not yet persist the running approval count, so the approval UI cannot show "2 of 3" without a later (migration-bearing) enhancement — the *mechanism* is complete, the *progress visibility* is deferred; per-approver SLA / delegation / escalation ladders (the research's richer Quorum) are future work.
- **Follow-ups:** SoD-at-gate distinctness (approver ≠ committer/requester, ≥K teams); persist running approval progress on the gate row for the UI; per-approver SLA + delegation; obligation enforcement (the `post_review`/`notify` obligations from ADR-0070/0062 opening real tasks); ChangeContext blast-radius/risk enrichment + target-anchored governance (§7.6).

## Alternatives considered

- **Model Quorum as a typed Control** (a `QuorumSpec` in the ControlSet) — rejected: it has no predicate and no outcome; it modifies how an *approval* is counted, which is a Gate property, not an evaluator input. Forcing it into `controlFires` would be a category error.
- **Track approval progress in the gate row (a migration)** — deferred: the mechanism needs only workflow-side state; persisting the running count is a UI-visibility enhancement, not required for correctness, and is left to a follow-up so this increment ships without a schema change.
- **Enforce distinctness in `DecideGate` (API-side)** — rejected: the workflow is the single writer of the accumulated set and is replay-safe; API-side tracking would duplicate state and risk divergence. Distinctness belongs where the count is decided.
