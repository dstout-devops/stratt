# ADR 0075 — Obligation enforcement: a binding rider is enforced, not recorded-and-dropped

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout)
- **Charter sections:** §1.8, §2 (Finding, Evidence), §4.3
- **Implements:** the enterprise-readiness crack fix for ADR-0070 guardrail 6 (break-glass "mandatory post-review")

## Context

An enterprise-readiness audit found the sharpest governance crack: the four-way `Decision` **emits** binding `Obligations` (`require_approval`, `notify`, `record_evidence`, `ttl`, `post_review`) but only `require_approval` was ever **consumed** (wired to a Gate). The rest were collected and dropped. Worst: break-glass's `post_review` obligation (ADR-0070 guardrail 6 — "bypass is never silence — mandatory post-review") was not even in the audit detail — the sole trace was a `break-glass-used` reason string. "Mandatory review" tracked nothing. A governance layer that emits obligations it does not enforce is theater, and an enterprise reviewer rejects on it.

## Decision

**Obligations are enforced by the PEP after the decision, using the machinery already shipped.**

**1. Every obligation is on the tamper-evident audit chain.** `policyAuditEvent` now marshals `dec.Obligations` into the `policy.decision` audit detail (ADR-0065). No binding rider is invisible on the §1.8 descent.

**2. `post_review` becomes a TRACKED, closeable Finding** (`graph.WriteGovernanceFinding`, framework `governance/post-review`, keyed by `(run/step)` so it is idempotent per bypass and resolvable). A break-glass emergency now leaves an open item someone must review and close — not a discarded struct. This reuses the existing Findings surface (list, resolve, the compliance rollup), so the review appears on the drift/findings dashboard like any other open compliance item. A failed write is logged, never silent (§1.8).

**3. The remaining obligations follow the same pattern, in sequence:**
- `record_evidence` → seal a WORM Evidence bundle for the decision (`evidencestore.Seal`), nil-guarded like the drift path — the compliance artifact for a privileged/approved change.
- `notify` → publish through the notification dispatcher (ADR-0027).
- `ttl` → an expiry the enforcement point honours on the resulting grant/waiver.

These are the following slices; this ADR ships the audit-chain inclusion and `post_review` — the break-glass guardrail-6 fix — and fixes the model (obligations are enforced, never dropped).

## Charter alignment

Upholds §1.8 (every binding rider is durably recorded on the tamper-evident chain and, where it obliges an action, produces a tracked item — nothing swallowed), §2 (reuses the frozen `Finding` Kind for the review item — no new noun; framework-tagged `governance/post-review`), and ADR-0070 guardrail 6 (the mandatory post-review is now a real, closeable obligation, not a string). No new dependency; no new Named Kind.

## Consequences

- **Positive:** break-glass "mandatory post-review" is enforceable — a reviewer can see the open review item and its resolution; every decision's obligations are attestable on the audit chain; the fix reuses the shipped Findings machinery (write/list/resolve), so it lands small.
- **Negative / trade-offs:** `record_evidence`/`notify`/`ttl` enforcement are the following slices (this closes the break-glass blocker and the audit-chain gap first); the post-review Finding's resolution is manual (a reviewer resolves it) — an SLA/escalation on an unresolved post-review is a later hardening.
- **Follow-ups:** `record_evidence` → `evidencestore.Seal` per decision; `notify` → the dispatcher; `ttl` → grant/waiver expiry; an SLA on unresolved `governance/post-review` Findings.

## Alternatives considered

- **A new review/obligation table + subsystem** — rejected: the Findings store already models "a tracked, severity-tagged, closeable compliance item with a lifecycle." A post-review is exactly that; minting a parallel store would fork the compliance surface.
- **Leave obligations advisory (status quo)** — rejected: it makes the governance layer's central promise (a decision's riders are *binding*) false; it is the crack this ADR exists to close.
