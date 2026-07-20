# ADR 0069 — Typed Control library: Waiver, a time-boxed control exemption

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout)
- **Charter sections:** §1.8, §2.4, §4.3, §5
- **Implements:** ADR-0061 §7.3 (increment 3) · builds on ADR-0067/0068

## Context

The third §7.3 primitive, and the first that is **not a predicate**: a **Waiver** is a *modifier* that exempts another control from applying its outcome — the governance escape valve. It changes the evaluation loop (a two-pass model), so — as flagged in the §7.3 cross-cut analysis — it is its own increment, not a drop-in `controlFires` case. It carries ADR-0061 guardrail 4: **expiry is mandatory** (Kyverno's missing-expiry footgun).

## Decision

**1. `WaiverSpec` is a modifier control.** A `Control` with a `Waiver` has no `When`/`Outcome`; it names `ControlRef` (a peer control's ID), and carries a mandatory `ExpiresAt`, a `Justification`, and an `ApprovedBy`. While **active** (not expired at the decision time) it suppresses the referenced control: if that control fires, its outcome is **not applied**, and the exemption is **recorded** (`waived` reason — a waiver-applied pass is compliance-relevant, ADR-0061 S1). The evaluator becomes two-pass: (1) collect the set of control IDs exempted by active waivers; (2) evaluate predicate controls, skipping the outcome of any fired-but-waived control.

**2. Waivers can only exempt a peer ControlSet control — never a mandatory floor.** `ControlRef` must name a control **in the same set** (validated at load). The §4.3/§5 floors are framework mechanisms, not ControlSet controls (ADR-0066), so a waiver structurally cannot reference — and cannot suppress — them. A broken (fail-closed) control is likewise **not waivable**: a waiver exempts a *decision*, not an *evaluation failure*, so a compile/eval error still denies.

**3. Fail-safe expiry (ADR-0061 M4):** activity is judged against `ChangeContext.ScheduledAt` (the run's deterministic `workflow.Now`). An unjudgeable time (zero) leaves every waiver **inactive** — we cannot confirm it has not expired, so the underlying control stands.

**4. Load validation:** `ExpiresAt` (mandatory), `Justification`, and `ApprovedBy` are all required (an exemption must be time-boxed and accountable); `ControlRef` must be non-self and present in the set; a waiver is exclusive with the predicate kinds.

## Charter alignment

Upholds §2.4 (the exemption is explicit, approved, time-boxed data — not an implicit precedence/last-writer override; the fixed lattice is unchanged, a waiver simply removes a control from the inputs), §1.8 (every suppression is recorded; the decision explains what was waived, by whom, and why), and §4.3/§5 (a waiver can never reach a mandatory floor — ADR-0066). No new Named Kind (`WaiverSpec` is a Control-payload shape). No new dependency.

## Consequences

- **Positive:** the governance escape valve exists as accountable, time-boxed, self-expiring data — no unbounded exemptions (the Kyverno footgun avoided by construction); every waived pass is on the record (S1); the two-pass evaluator is established for future modifier primitives (BreakGlass will reuse the shape).
- **Negative / trade-offs:** v1 waivers are scoped only by `ControlRef` (whole-control), not by target/subject selector — per-target waiver scope is §7.6 (target-anchored governance); a `reviewCadence` field and revocation lifecycle are deferred.
- **Follow-ups:** the remaining §7.3 primitives — **Quorum** (M-of-N at the gate layer), **BreakGlass** (bypass modifier + mandatory post-review, which reuses this two-pass shape); per-target waiver scope + revocation (§7.6); committer/blast-radius enrichment (§7.6).

## Alternatives considered

- **No expiry / optional expiry** — rejected (guardrail 4): unbounded exemptions are the known Kyverno footgun; a Stratt waiver without `expiresAt` fails to compile.
- **Let a waiver suppress a fail-closed error or a mandatory floor** — rejected: a waiver exempts a *policy decision*, not an evaluation failure or a §4.3/§5 safety floor; `ControlRef`-in-set + the not-waivable fail-closed path enforce this structurally.
- **Waivers as a separate `PolicySpec.Waivers` list** — rejected for v1: keeping waivers *in* the ControlSet (as a Control kind) keeps `Evaluate(controls, cc)` stable and models a waiver as what it is — part of the governance set — at the cost of a two-pass loop, which is warranted and self-contained.
