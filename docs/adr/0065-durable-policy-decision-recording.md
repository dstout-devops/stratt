# ADR 0065 — Durable policy-decision recording: the audit stream, not a Finding

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout)
- **Charter sections:** §1.6, §1.8, §2 (Finding, Evidence), §2.4, §3, §4.3
- **Implements:** ADR-0061 §7.2 (increment d, part 1) · builds on ADR-0063/0064

## Context

ADR-0063/0064 wired the PDP into the DAG but recorded a decision only by logging. ADR-0061 decision 5 said every decision stamps Provenance + Evidence and a *compliance-relevant* one mints a Finding (S1). Making that durable raises a modelling choice: a per-run policy decision is a **point-in-time action** (this run was allowed / denied / sent to approval), not a **standing drift state**. A `Finding` (§2.4) is a drift/compliance record with a `pending → open → resolved` lifecycle — the wrong shape for a point-in-time verdict, which would open Findings that never resolve.

## Decision

**1. Every policy decision is recorded on the one hash-chained audit stream** (ADR-0034), not as a Finding. A new action constant `AuditPolicyDecision = "policy.decision"` joins the stable vocabulary next to `gate.decision`. The `EvaluatePolicy` activity records an `AuditEvent{PrincipalID: actor, Action: policy.decision, Object: workflowRunID, Outcome, Detail}` after evaluating — point-in-time, Principal-stamped, tamper-evident (§1.6/§1.8). This is the right home for "a decision happened": the audit stream already records every action including allows (like the HTTP middleware), so a clean `allow` is recorded without polluting a drift dashboard (ADR-0061 S1, satisfied via the audit stream rather than a Finding).

**2. Outcome mapping:** `allow → ok`, `deny → denied`, `require_approval`/`escalate` recorded verbatim; the reasons + step ride in `Detail` (structured, non-secret, §2.5). The construction is a pure `policyAuditEvent(arg, decision)` so the mapping is unit-tested; the `Store.RecordAudit` write is non-fatal-on-error (logged, never swallowed — §1.8) and nil-guarded (like the Evidence store).

**3. Findings and object-locked Evidence bundles are deferred to where they fit.** A *standing* governance violation (a declaration that keeps failing admission) is the admission-PEP's concern (§7.4) and is the right place to mint a Finding; sealing an object-locked Evidence bundle per decision is §7.2d-part-2 alongside the mandatory-floor compiler. This ADR delivers the tamper-evident audit record — the attestable spine — and names the rest.

## Charter alignment

Upholds §1.6/§1.8 (one audit stream; every decision is a durable, descendible, tamper-evident record with structured reasons), §2 (reuses `AuditEvent` — no new Named Kind; deliberately does NOT overload `Finding` for a point-in-time verdict), §2.5 (Detail carries reasons, never secret material). It refines ADR-0061 decision 5's "Finding + Evidence" to "audit now; Finding for standing violations at the admission PEP" — the honest fit, not a weakening (the hash-chained stream is the attestation substrate).

## Consequences

- **Positive:** governance decisions are durably attestable immediately, reusing the audit chain + SIEM forwarder (ADR-0034) with zero new substrate; allow/deny/approval all recorded uniformly; the mapping is unit-tested.
- **Negative / trade-offs:** no object-locked Evidence bundle per decision yet (the audit chain is tamper-evident but not WORM-sealed) — that lands with §7.2d-2; no Finding for standing violations until the admission PEP (§7.4).
- **Follow-ups:** §7.2d-2 (framework-compiled mandatory floors §4.3/§5 + per-decision Evidence sealing), §7.4 (admission PEP mints Findings for standing declaration violations).

## Alternatives considered

- **Mint a Finding per decision** — rejected: a Finding is a drift record with a resolve lifecycle; a point-in-time verdict would open Findings that never resolve and pollute the drift surface (ADR-0061 S1's exact concern).
- **Log only (status quo)** — rejected: a log line is not tamper-evident, not on the audit chain, and not forwarded to the SIEM — governance decisions must be attestable (§1.8).
