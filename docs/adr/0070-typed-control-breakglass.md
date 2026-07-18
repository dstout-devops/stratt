# ADR 0070 — Typed Control library: BreakGlass, emergency bypass with mandatory post-review

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout)
- **Charter sections:** §1.8, §2.4, §4.3, §5
- **Implements:** ADR-0061 §7.3 (increment 4) · builds on ADR-0069

## Context

The fourth §7.3 primitive: **BreakGlass** — the emergency-bypass valve. It reuses the two-pass *modifier* shape ADR-0069 (Waiver) established, and adds the two things a break-glass needs: **conditional activation** (a real emergency, not a flag) and a **mandatory post-review** (ADR-0061 guardrail 6: bypass is never silence). It is the last modifier primitive; only Quorum (a gate-layer change) remains after this.

## Decision

**1. `BreakGlassSpec` is a conditional bypass modifier.** A `Control` with a `BreakGlass` names the controls it `Bypasses` and a `PostReviewBy` authority. While **active** it suppresses those controls (recorded as `break-glass`, outcome not applied — reusing the Waiver two-pass suppression). Activation is a **declared emergency, never a bare flag**: `change_class == "emergency"` **AND** an `incident` **AND** a `reasonCode` present in the `ChangeContext` (the activator's justification, supplied at launch). No emergency declaration ⇒ inactive ⇒ the bypassed controls stand.

**2. A used break-glass ALWAYS leaves a mandatory post-review obligation** (guardrail 6). Whenever break-glass actually bypasses a fired control, the decision carries an `ObligationPostReview{by: PostReviewBy, incident}` and a `break-glass-used` reason. `PostReviewBy` is **required at load** — a break-glass with no retro-review authority does not compile. Bypass is recorded, obligated, and reviewable — never silent.

**3. The same structural limits as a waiver (ADR-0069/0066).** `Bypasses` must name non-self controls **in the same set**, so a break-glass can never reach a §4.3/§5 mandatory floor (they are not ControlSet controls). A fail-closed (broken) control is **not bypassable** — an emergency exempts a *decision*, not an *evaluation failure*. A control is a predicate, a waiver, or a break-glass — never more than one.

**4. `ChangeContext.ChangeClass` is enriched from a `changeClass` launch param** (with `incident`/`reasonCode` riding in labels) — the pragmatic runtime source, consistent with committers/environment.

## Charter alignment

Upholds §1.8 (an emergency bypass is fully recorded, obligated to review, and diagnosable — the antithesis of a silent override), §2.4 (bypass is explicit conditional data, not implicit precedence; the fixed lattice is unchanged — break-glass removes a control from the inputs, like a waiver), §4.3/§5 (a break-glass can never reach a mandatory floor — ADR-0066), and ADR-0061 guardrail 6 (mandatory post-review, enforced at load). No new Named Kind (`BreakGlassSpec` is a Control-payload shape); the closed obligation enum gains `post_review` under this ADR (guardrail 1: a new obligation type is its own ADR). No new dependency.

## Consequences

- **Positive:** the emergency path exists as accountable, conditionally-activated data — an incident-gated bypass that always produces a post-review obligation and elevated audit trail; it composes with everything (the winning outcome after a bypass is computed over the surviving controls); the two-pass modifier shape is confirmed to generalise (Waiver → BreakGlass).
- **Negative / trade-offs:** activation criteria are fixed in v1 (emergency + incident + reasonCode) rather than an author-supplied `activatable_when` expression; `auto_revoke_after` and an ECAB-style review SLA/lifecycle are deferred; the post-review is an *obligation on the record*, not yet an enforced workflow (that rides with the obligations-enforcement follow-up).
- **Follow-ups:** the last §7.3 primitive — **Quorum** (M-of-N at the gate layer); obligation *enforcement* (a `post_review` obligation opening a review task, a `notify` obligation firing a notification); author-configurable activation; blast-radius/risk ChangeContext enrichment (§7.6).

## Alternatives considered

- **A bare `enabled` flag for break-glass** — rejected: an always-on bypass is exactly the anti-pattern; activation must be a real, recorded emergency declaration (incident + reasonCode), so a bypass is never trivially available.
- **Optional post-review** — rejected (guardrail 6): bypass without mandatory review is silence; `PostReviewBy` is required at load and the obligation is unconditional when break-glass is used.
- **A separate BreakGlass evaluation engine** — rejected: it is a modifier like a waiver; reusing the two-pass suppression keeps one evaluation model and one `Evaluate(controls, cc)` signature.
