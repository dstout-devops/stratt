# ADR 0072 — The Policy Decision Point is a PORT, not a core dependency (corrects ADR-0062)

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout)
- **Charter sections:** §1.1, §1.4, §1.5, §3, §5, ADR-0046
- **Supersedes:** the in-core-direct-call architecture of ADR-0062 (the Contract and outcome model of 0062 stand)

## Context

ADR-0061 (the governance model) got a charter-guardian pass; **ADR-0062–0071 (the implementation) did not.** Reviewing against the steward's stated tenet — *"everything is pluggable and configurable; we must be able to swap out OR bypass every external tool; policy and governance are external to Stratt just like infrastructure, behind ports and plugins, never core dependencies"* — the implementation had drifted:

- `core/internal/policy` was a **concrete decision engine, called directly** by `runPolicyStep` (no seam) — the CEL evaluator, the four-way lattice, and the whole governance **domain vocabulary** (SoD, TimeWindow, Waiver, BreakGlass). Governance domain semantics were baked into the content-blind spine (violating §1.1 "type the seams, not the world" and ADR-0046 "every tool is a plugin"), and — with no interface — an external engine could **not** replace it and there was **no way to bypass** it. Policy was a hardcoded dependency, not a port.

This is the same category error as putting Crossplane's or Ansible's logic in core. It contradicts §1.4 (breadth lives behind plugin surfaces) and §1.5 (no external protocol/engine load-bearing for the deterministic core).

## Decision

**1. The PDP is a PORT — the `policy.Decider` seam** — mirroring the two existing patterns for the same problem: `authz.Authorizer` (interface + in-tree tuple evaluator + OpenFGA) and the Actuator/Action registry (in-tree providers + external plugin-port providers). The core PEP obtains a `Decision` through the port and **never calls a concrete engine**:

```go
type Decider interface { Decide(ctx, Request) types.Decision }   // Request{Controls, ChangeContext}
```

`Activities.Decider` holds the configured provider; `EvaluatePolicy` calls `a.Decider.Decide(...)`. The core is content-blind at the call site — it sends controls + context and acts on the returned Decision; it does not know what SoD or a break-glass *means*.

**2. Providers are swappable and bypassable.**
- **`CEL`** — the built-in, zero-dependency **in-tree provider** (the CEL engine + the typed Control library). One provider among many, the *default*, never load-bearing.
- **External engine (OPA/Cerbos/…)** — a plugin over the sovereign gRPC port (a follow-up provider, exactly as external plugin Actuators sit beside in-tree ones).
- **`Bypass`** — disables governance **explicitly and visibly**: it allows every change but stamps a `policy-bypassed` reason and a `bypass` engine, so the audit stream shows governance was turned off (§1.8 — never a silent skip). Wired by `STRATT_POLICY_BYPASS`. This is the honest realisation of "we must be able to bypass every external tool."

**3. The line between spine and provider.**

| Piece | Placement |
|---|---|
| DecisionRequest/Decision **Contract** (contracts/policy) | **core** — the port contract (§1.5) |
| The **PEP** — policy Step, gate, quorum, audit recording (ADR-0065), the mandatory floors (ADR-0066) | **core** — enforcement mechanism, spine (like an actuation Step that calls a plugin) |
| The four-way outcome + the **`Decider` seam** | **core** — the port |
| The **CEL evaluation**, the **typed Control library** (SoD/TimeWindow/Waiver/BreakGlass), the most-restrictive **lattice** | **behind the port** — a provider (in-tree `CEL` today; extractable to a plugin). Governance **domain logic**, not spine. |
| `types/policy.go` | **core** — shared wire/domain types (the Contract's Go shapes), engine-agnostic |

**4. Admission (the deferred §7.4) goes through the port too** — it must NOT become a second in-core evaluator. Admission is "core sends an admission-shaped DecisionRequest to the PDP over the same seam"; the engine decides. Building `policy.Admit` as concrete in-core logic would have re-committed the exact error this ADR corrects.

## Charter alignment

Restores §1.1 (governance domain semantics live at a seam, not in the spine), §1.4 (the decision engine is breadth behind a port, like every other tool; the spine owns the PEP/Contract/floors), §1.5 (no engine is load-bearing — the core runs via Bypass or a swapped provider), §5 (the PEP still gates, unchanged), and ADR-0046 (content-blind core, every tool a plugin). It aligns policy with the two precedents the charter already blesses (authz seam, actuator registry). No permanent non-goal touched.

## Consequences

- **Positive:** policy is now genuinely pluggable, swappable, and bypassable — the stated tenet holds structurally; the core call site is content-blind; the built-in CEL engine keeps the zero-dependency default (like the in-process tuple evaluator); the whole §7.3 control library is preserved verbatim — it simply now lives *behind* the port as the `CEL` provider's implementation, not in the call path.
- **Negative / trade-offs:** the `CEL` provider is still an **in-tree** package (`core/internal/policy`), like the in-tree Actuators and the tuple evaluator — swappable/bypassable through the seam, but not yet a separate process. Full extraction to an external gRPC plugin is the follow-up (it makes governance "external" in deployment, not just in architecture). Load-time `ValidateControls` is still a package function (the CEL provider's static validation), not yet routed through a port `Validate` method.
- **Follow-ups:** extract the `CEL` provider behind the sovereign gRPC port as a first-class policy plugin (matching external plugin Actuators); add a port `Validate` so declaration-time validation is engine-selected too; ship the OPA plugin over the port (ADR-0061 §7.5); the admission PEP over the port (§7.4).

## Alternatives considered

- **Leave the engine directly-called in core (ADR-0062 as built)** — rejected: it makes policy a hardcoded dependency, un-swappable and un-bypassable, with governance domain logic in the content-blind spine. Contradicts the stated tenet and §1.1/§1.4/§1.5/ADR-0046.
- **Move the CEL engine straight to an external gRPC plugin now** — deferred, not rejected: the seam is the essential fix and delivers swap/bypass immediately; the in-tree provider mirrors how Stratt already ships in-tree Actuators beside external plugin ones. Full extraction follows once the port has proven out with a second (OPA) provider.
- **A pure allow-all default when unconfigured** — rejected: an unconfigured PDP defaulting to the built-in CEL provider is the safe, explicit default; bypass must be a deliberate, recorded configuration (`STRATT_POLICY_BYPASS`), never the accidental resting state.
