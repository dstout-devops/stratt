# ADR 0066 — The mandatory safety floors pre-exist and are non-substitutable (ADR-0061 M1, closed)

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout)
- **Charter sections:** §2.4, §4.3, §5
- **Implements:** ADR-0061 §7.2 (increment d, part 2) — closes must-fix **M1**

## Context

ADR-0061 M1 (charter-guardian must-fix) requires that the charter-mandatory safety floors — the **§4.3 max-delta blast-radius gate**, the **§5 Flow-1 plan-gate**, and the **§2.4 orphan/drift Findings** — be compiled in unconditionally by the framework, be un-omittable in a ControlSet, and be un-crossable by a policy `ALLOW`. This increment verifies M1. The finding: **the floors already exist as framework mechanisms** and the policy layer (ADR-0063/0064) is a *separate, additive* checkpoint that cannot express, omit, or substitute for them. No new floor machinery is built; M1 holds by construction, and this ADR records + tests that.

## Decision

**The mandatory floors are pre-existing framework mechanisms, outside the ControlSet vocabulary:**

| Floor | Where it lives | Why a ControlSet/policy `ALLOW` cannot bypass it |
|---|---|---|
| **§4.3 max-delta blast-radius gate** | the **compiler** (`core/internal/compiler`): `DefaultMaxDelta`, `Assignment.MaxDelta`/`AckDelta`, the membership-delta pause surfaced on `/compile` (§1.8) | engine-level; it pauses a *compile* whose target set changes more than the fraction. It is not a Step, not in the `Control`/`PolicySpec` vocabulary — a ControlSet has no field that could express or disable it. |
| **§5 Flow-1 plan-gate** | `validatePlanPinning` / `guardedByGateForPlan` (`desiredstate`), fail-closed **at load** (ADR-0047 §8) | a plan-pinned Apply must be guarded by a real **Gate** binding the plan digest (write-once, approve-what-you-see). `guardedByGateForPlan` accepts only `Gate != nil && PlanFrom == plan`; a policy Step binds **no** digest (`runPolicyStep` calls `awaitGate` with `planDigest=""`) and so can never satisfy it — the pin stays enforced. |
| **§2.4 orphan/drift Findings** | the reconcile / Baseline machinery (ADR-0019/0059) | emitted by the framework on observation; not a Step, not in the ControlSet vocabulary. |

**The policy layer is additive-only.** A `PolicySpec` Step is one of four mutually-exclusive shapes (ADR-0063); it carries only `Controls` (CEL predicates + a four-way outcome). It cannot be a plan-pinned Apply (it has no `PlanFrom`/actuation), cannot be a plan-gate guard (it is not a `Gate`), and cannot influence the compiler's max-delta pause. A policy `ALLOW` merely succeeds a checkpoint Step *inside* a run whose floors were already applied at compile/load. Therefore M1 is satisfied structurally — the floors compose *under* the policy layer, never *through* it.

**Verified by test:** a plan-pinned Apply "guarded" only by a policy Step (no Gate) is **rejected at load** by `validatePlanPinning`, exactly as one guarded by nothing — proving the policy layer cannot substitute for the plan-gate floor.

## Charter alignment

Upholds §4.3 (the max-delta gate remains the mandatory, engine-level blast-radius insurance, untouched by the policy layer), §5 (the plan-gate stays fail-closed and Gate-only; a policy checkpoint never degrades approve-what-you-see), and §2.4 (orphan Findings remain framework-emitted). Closes ADR-0061 M1 by verification rather than new mechanism — the honest outcome.

## Consequences

- **Positive:** M1 is closed and *tested*, not merely asserted; the composition (policy adds restriction on top of un-bypassable floors) is documented for future readers; no new floor code to maintain.
- **Negative / trade-offs:** none — this is a verification/closure increment.
- **Follow-ups:** §7.2d part-2's remaining item — per-decision object-locked **Evidence** sealing — rides with §7.4 (admission PEP) where standing violations mint Findings+Evidence; §7.3 (the typed Control library: SoD/TimeWindow/Waiver/Quorum/BreakGlass) is the next substantive governance surface.

## Alternatives considered

- **Build a new "mandatory-floor compiler" in the policy layer** — rejected: it would duplicate the compiler's max-delta gate and `validatePlanPinning`, and risk a *second*, weaker enforcement path. The floors already exist at the right layer; the policy layer must compose under them, not re-implement them.
- **Let a policy `require_approval` substitute for the plan-gate** — rejected (§5): a policy-opened Gate binds no plan digest, so it cannot provide approve-what-you-see; `validatePlanPinning` correctly demands a real plan-binding Gate.
