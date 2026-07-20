# ADR 0064 — Policy REQUIRE_APPROVAL opens a human Gate; the approver check folds into one authz seam

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout)
- **Charter sections:** §1.6, §1.8, §2 (Gate), §5
- **Implements:** ADR-0061 §7.2 (increment b) · builds on ADR-0063

## Context

ADR-0063 wired the PDP into the DAG for the automated `allow`/`deny` case and *failed closed* on `require_approval`/`escalate`. This increment makes those two outcomes do what they mean: **open a human Gate**. It also discharges the note ADR-0011 left open — "the inline principals/teams approver check must fold into the single authorization model" (§1.6) — without forcing the declaration-scoped principals list into OpenFGA tuples.

## Decision

**1. `require_approval` / `escalate` open a Gate reusing the existing machinery.** `runGateStep`'s create→wait-on-signal→record logic is extracted into a shared `awaitGate(workflowRunID, stepName, approvers, timeoutSeconds, planDigest)`; `runGateStep` becomes a thin caller, and `runPolicyStep` calls the *same* `awaitGate` when the decision is `require_approval` or `escalate`. The Gate a policy Step opens is **indistinguishable to the approval API** (`POST /gates/{id}/decision`) from a declared Gate — same row, same signal, same audit — so the human path is one mechanism, not two.

**2. Approvers are derived from the decision's `require_approval` obligation** (the closed obligation from ADR-0062): its `params.teams` / `params.principals` become the `GateApprovers`, and an optional `params.timeoutSeconds` its timeout. **A `require_approval`/`escalate` outcome with no approver obligation fails closed** — an unsatisfiable approval is never a silent pass (§1.8). `escalate` shares the mechanism; its *higher-authority* semantics are encoded in the obligation's approver set (a richer escalation ladder is §7.3 Quorum/RiskRoute).

**3. The approver check folds into one authz seam.** The inline loop in `DecideGate` (a literal principals-list match plus a per-team `Authz.Check(member)`) becomes `authz.ApproverAuthorized(ctx, authorizer, principal, approvers)`. Both the human-Gate and policy-Gate decision paths answer "may this principal decide" through the *one* function (§1.6). The explicit principals list stays declaration-scoped data checked inside that helper — not a new OpenFGA object type (ADR-0011's deliberate deferral holds); the unification is the single seam, per 0061 M2.

## Charter alignment

Upholds §1.6 (one approver-authorization seam for every Gate, human- or policy-opened; ReBAC team membership still answered by `Authorizer.Check`), §1.8 (an unsatisfiable approval fails the step visibly; the policy-opened Gate is a first-class, descendible record), §2 (no new Named Kind — a policy-opened Gate *is* a Gate), and §5 (`require_approval`/`escalate` block on a human, never auto-launch). No new dependency.

## Consequences

- **Positive:** governance can now require human sign-off derived from policy (e.g. "prod change ⇒ require approval from `platform-admins`"), reusing the entire Gate/audit/descent stack; the approver check has one implementation; `escalate` is usable via its obligation.
- **Negative / trade-offs:** M-of-N quorum (`params.count > 1`) is *not* enforced yet — the Gate approves on the first authorized signal; true quorum is the §7.3 Quorum control. The escalation ladder is flat (one obligation-named approver set) until §7.3.
- **Follow-ups:** §7.2c (launch-seam deny-composition), §7.2d (framework-compiled mandatory floors + durable Finding/Evidence on every decision), §7.3 (Quorum/RiskRoute for M-of-N and escalation ladders).

## Alternatives considered

- **Keep failing closed on `require_approval`** — rejected: it strands the outcome the whole four-way model exists for; the human Gate already exists to reuse.
- **A separate policy-gate row/table** — rejected (§2): a policy-opened Gate is a Gate; a parallel type would fork the approval API and audit.
- **Push the principals list into OpenFGA tuples now** — deferred (ADR-0011): the list is declaration-scoped policy data; the seam unification (one `ApproverAuthorized`) achieves the §1.6 goal without minting an authz object type prematurely.
