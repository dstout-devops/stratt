# ADR 0073 — The admission PEP: policy at the compile seam, over the PDP port

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout)
- **Charter sections:** §1.1, §1.4, §1.5, §1.8, §3, §5
- **Implements:** ADR-0061 §7.4 · builds on ADR-0072 (the PDP port)

## Context

ADR-0061 named **two enforcement points, one PDP** (decision 2): the **gate PEP** at Run boundaries (built, §7.2) and the **admission PEP** at the desired-state write/compile seam — the charter's "**Kyverno-for-config**" (§3: "no `exportable:true` cert Intents", "prod Assignments require a Gate", "team X may only target Views under org X"). Admission validates a **declaration** as it is admitted, actor-independent (the K8s ValidatingAdmissionPolicy model).

The critical constraint, learned the hard way in ADR-0072: admission must go **through the PDP port**, not become a second concrete in-core evaluator. `policy.Admit` as directly-called in-core logic would re-commit the exact drift ADR-0072 corrected — governance domain logic in the content-blind spine, un-swappable, un-bypassable.

## Decision

**1. The admission PEP is a second operation on the same `policy.Decider` port** — one PDP, two enforcement operations:

```go
type Decider interface {
	Decide(ctx, Request) types.Decision           // the gate PEP (run boundary)
	Admit(ctx, AdmissionRequest) types.Decision    // the admission PEP (compile seam)
	Validate(controls) error
}
type AdmissionRequest struct { Object map[string]any; Controls []types.Control }
```

The core sends the **declaration object** + the admission controls to the port and acts on the returned `Decision`; it does not interpret admission meaning. The built-in `CEL` provider evaluates; an external engine (OPA/Kyverno-JSON — the natural admission engine) plugs in behind the same port; `Bypass` admits everything, recorded.

**2. Admission controls are CEL predicates over the declaration `object`, allow/deny only.** The CEL env binds `object` (the manifest), mirroring K8s VAP (`object.spec.environment == 'prod' && !hasGate(object)` → deny). `require_approval`/`escalate` do not apply at the compile seam — a declaration is admitted or rejected at load, not queued for approval — so admission validation rejects any non-allow/deny outcome and any run-time typed primitive (TimeWindow/SoD/Waiver/BreakGlass are run-time governance, not declaration checks). The most-restrictive lattice and fail-closed discipline are shared with the run evaluator: an uncompilable or unevaluable admission control **denies** (§1.8, never a silent admit).

**3. A DENY rejects the declaration at load — fail-closed.** When the loader wires admission (the follow-up slice), an admission `deny` fails the file exactly like a structural validation error, with the reasons on the record (§1.8). This slice ships the port operation + the built-in provider + validation, unit-tested in isolation — the correct-over-the-port foundation, before the estate admission-policy surface and the loader wiring (as §7.1 landed the run evaluator before the DAG dispatch).

## Charter alignment

Upholds §1.1 (admission policy typed at the seam — the declaration `object`, never a global schema), §1.4/§1.5 (admission runs through the port; the engine is swappable + bypassable; nothing governance-domain is added to the core call path — the ADR-0072 discipline), §3 (realises the charter's "admission policies on manifests — Kyverno-for-config"), §1.8 (a rejected declaration fails visibly with reasons; fail-closed on unevaluable controls), and §5 (admission gates declarations, it never launches anything). No new Named Kind; `AdmissionRequest` is a port payload.

## Consequences

- **Positive:** the second enforcement point exists behind the *same* port — one PDP, two PEPs, both content-blind and swappable/bypassable; Kyverno-JSON/OPA become the natural admission engine over the port; the built-in CEL provider gives a zero-dependency default; the evaluation reuses the run evaluator's lattice + fail-closed discipline.
- **Negative / trade-offs:** not yet reachable at load — the estate **admission-policy declaration surface** (where admission controls are declared — §3's central registry) and the **loader wiring** (admit each declaration through the port, reject on deny) are the following slices; this slice is the port operation + provider, unit-tested.
- **Follow-ups:** §7.4b — the estate admission-policy surface (a central admission policy declared in Git); §7.4c — wire the desired-state loader to admit declarations through the port (deny ⇒ reject at load); the Kyverno-JSON/OPA admission provider over the sovereign gRPC port; a port `ValidateAdmission` for engine-selected admission-dialect validation.

## Alternatives considered

- **A concrete in-core `policy.Admit`, directly called** — rejected: it is precisely the drift ADR-0072 corrected; admission belongs behind the port like the run decision.
- **A separate admission port distinct from `Decider`** — rejected: one PDP with two enforcement operations is truer to ADR-0061's "two PEPs, one PDP"; a separate port would fork the engine configuration and the audit path.
- **Allow `require_approval` at admission** — rejected: a declaration cannot wait for a human at compile time; admission is admit-or-reject. Approval-gated *changes* are the gate PEP's job at run time.
