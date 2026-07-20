# ADR 0062 — Policy Contract & PDP interface v1: the four-way Decision, the CEL evaluator, and the most-restrictive lattice

- **Status:** Accepted — architecture **partially superseded by [ADR-0072](0072-policy-decision-point-is-a-port.md)**
- **Date:** 2026-07-18
- **Deciders:** steward (dstout)
- **Charter sections:** §1.1, §1.4, §1.5, §1.8, §2 (Contract), §2.4, §3
- **Implements:** ADR-0061 §7.1 (the first sequenced follow-up)

> **Superseded note (ADR-0072):** the Contract, the four-way Decision, and the most-restrictive
> lattice stand. What ADR-0072 corrects: this ADR built the evaluator as a concrete, directly-called
> in-core engine with **no seam** — making policy a hardcoded dependency, un-swappable and
> un-bypassable, with governance domain logic in the content-blind spine. ADR-0072 puts the PDP behind
> the `policy.Decider` port (built-in CEL provider + external plugins + explicit bypass). Read this ADR
> for the Contract/outcome model; read ADR-0072 for the (corrected) placement.

## Context

ADR-0061 fixed the governance model: a Policy Decision Point returning a four-way `Decision` over a shared typed `ChangeContext`, with CEL + a closed Control library as the content-blind built-in and external engines as plugins. This ADR builds the **smallest buildable, verifiable slice** of that model: the **PDP evaluator core** and its **pinned Contract**, in isolation — *before* it is wired into the DAG (0061 §7.2), before the admission PEP (§7.4), and before target-anchored ControlSet Facets (§7.6). Scope is deliberately the pure decision engine: types + a CEL evaluator + the combining lattice + the Contract, unit-tested, with no API route, no DB table, no orchestration change.

## Decision

**1. Domain types (`types/policy.go`).** Go structs are an internal convenience; the pinned JSON Schema is the source of truth (§1.5). `Outcome` is a closed enum `allow | deny | require_approval | escalate`. `Decision{Outcome, Reasons[], Obligations[], Provenance}`; `Reason{Code, Message, ControlID}`; `Obligation{Type, Params}` over a **closed obligation enum** (0061 guardrail 1). `Control{ID, Type, When, Outcome, Obligations}` — `When` is a CEL predicate string. `ChangeContext{Actor, Committers, Targets[], BlastRadius, Environment, ChangeClass, RiskScore?, ScheduledAt, Labels}` with the sparse/optional risk coordinates from 0061 (M4).

**2. Pinned policy Contract (`contracts/policy/*.schema.json`).** `decision-request` and `decision` schemas, embedded + hash-verified via the existing loader (ADR-0015 pattern); `contracts/embed.go` gains a `policy/*.schema.json` glob and the pin-count test bumps. Engine plugins (0061 §7.5) normalise to these shapes; the built-in evaluator produces them directly.

**3. The evaluator (`core/internal/policy`).** `Evaluate(controls, ChangeContext) Decision`:
- **Every control is always evaluated** (0061 M3) — order is non-semantic. Each control's `When` CEL predicate is compiled (fail-closed at parse) and evaluated over the ChangeContext; a control whose predicate is true **fires** its declared `Outcome` + obligations.
- **The combining rule is the fixed, non-configurable most-restrictive-wins lattice** `deny > escalate > require_approval > allow` (0061 M3). No priority scalar, no configurable combinator. With no control firing, the outcome is `allow`.
- **All fired controls' reasons are collected** — not only the winning one (0061 S4) — so the record explains the full evaluation (§1.8).
- **Fail-safe / fail-closed:** a CEL evaluation error is a `deny` with a reason (never a silent pass); a missing sparse risk coordinate is treated as most-restrictive, never "no risk" (0061 M4).
- This slice evaluates the built-in tier only; `deny`-composition with OpenFGA (0061 M2) and the framework-compiled mandatory floors (0061 M1) are the DAG-wiring follow-up (§7.2), not this pure evaluator.

**4. The policy CEL env is a distinct, more strictly builtin-subsetted environment than the trigger env** (dependency-scout, 0061 §7.1): Control authors are less trusted than today's platform-only trigger authors. The evaluator reuses the cost-bounding + fail-closed discipline of `core/internal/rules` (static + runtime `CostLimit`, bool-typed output) but binds the `ChangeContext` variables and omits builtins the predicates don't need.

## Charter alignment

Upholds §1.1 (policy typed at the seam — a Contract, evaluated over the typed Envelope only, never opaque tool Payload), §1.4/§1.5 (CEL is already in core; the Contract is pinned hash-verified data, not language classes), §2.4 (the lattice is a fixed order-independent monotone — the additive-union analogue, not precedence), and §1.8 (all reasons recorded). No new Named Kind: `ChangeContext`/`DecisionRequest`/`Decision`/`Control` are Contract-payload type names, vocabulary-linter-cleared under 0061. No new dependency (cel-go is in core).

## Consequences

- **Positive:** the decision engine exists and is unit-tested in isolation, so §7.2 (DAG wiring) and §7.5 (engine plugins) build on a proven core; the four-way outcome + lattice + fail-safe are locked as code with tests, not just prose.
- **Negative / trade-offs:** not yet reachable at runtime — no Step consults it until §7.2; the built-in evaluator covers CEL predicates over the ChangeContext only (richer policy awaits an engine plugin).
- **Follow-ups:** ADR-0061 §7.2 (fold into the `RunDAG` step switch; OpenFGA deny-composition; framework-compiled mandatory floors), §7.3 (the typed Control library), §7.4 (admission PEP), §7.5 (OPA plugin), §7.6 (target-anchored ControlSet Facet). Pin-count test bumps with the two policy schemas.

## Alternatives considered

- **Wire the evaluator straight into the DAG in one slice** — rejected: couples the pure decision logic to orchestration + authz composition + the mandatory-floor compiler, making the first slice large and hard to test in isolation. The evaluator core is separable and TDD-friendly alone.
- **Reuse the trigger CEL env verbatim** — rejected: policy authors are less trusted (dependency-scout); the policy env is deliberately a separate, more-subsetted environment.
- **Skip the pinned Contract for v1, use Go types only** — rejected: the Contract is the hash-verified plugin/wire boundary (§1.5); building it now keeps the engine-plugin follow-up (§7.5) a drop-in.
