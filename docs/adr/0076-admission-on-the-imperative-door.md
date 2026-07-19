# ADR 0076 — Admission on the imperative door: the API is not a bypass around the compile-seam PEP

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** steward (dstout)
- **Charter sections:** §1.2 (projections/declaration surface), §1.8 (no hidden failure), §2.4 (no implicit precedence), §6 (one authorization model across UI/CLI/CI/agent)
- **Implements:** the enterprise-readiness crack fix **GOV-2** for ADR-0073 (the admission PEP)
- **Extends:** ADR-0073 (admission PEP over the port), ADR-0072 (the PDP is a port)

## Context

ADR-0073 placed an **admission PEP** at the estate compile seam: at `ParseDir`, every declaration is admitted through the PDP port and a deny rejects the load. An enterprise-readiness audit found the PEP was wired at **only one of the doors into the graph** — the Git reconcile. The **imperative API doors** — `POST /desired-state/plan`, `POST /desired-state/apply`, and `PUT /views/{name}` — decoded a declaration straight into the graph with **no admission call at all** (`ComputePlan`/`Apply` took no Decider). A caller who could reach the API (an operator, a CI job, an AI agent over the same surface — §6) bypassed the entire admission policy by not going through Git.

An admission policy that only one of several equivalent doors honours is not a policy — it is a suggestion. This directly contradicts §6 (one authorization model, identically enforced regardless of surface) and §1.8 (a control that silently does not run is worse than no control).

## Decision

**The admission PEP is enforced at every door into the graph, over the same PDP port, against the same estate policy.**

**1. A door-agnostic admission entrypoint.** `desiredstate.AdmitDeclarations(ctx, decls, controls, decider)` admits already-parsed typed declarations — the form the imperative doors hold — mirroring the raw-manifest `admitEstate` the Git door uses. Each declaration is encoded to the generic `{kind, ...}` admission object (`declarationObject`), preserving the declaration's own `kind` when it has one (an `Intent/Certificate` is admitted as `Certificate`, not the fallback) exactly as `manifestObject` does, so **a control keyed on `object.kind` sees the same shape at both doors.** The object is the declaration's typed JSON — the same shape the graph will hold.

**2. The API server admits before the graph write.** `Server.desiredStateBody` (shared by `/plan` and `/apply`) and `DeclareView` call `AdmitDeclarations` and return **403** on deny. It routes through the `policy.Decider` port (ADR-0072) — CEL by default, or an external OPA/Kyverno engine, or an explicit recorded Bypass — never a second in-core evaluator. A deny, or any evaluator error, **fails closed** (the port's contract).

**3. Boot-snapshot policy on the imperative door.** The server admits against the estate's admission controls **snapshotted at boot** from the same estate the reconcile controller admits against. This is a deliberate asymmetry: the Git reconcile door always admits against the **live** estate (it re-parses every cycle); the imperative door is the backstop and admits against the boot snapshot. A later change to the admission policy is picked up by the reconcile door immediately and by the imperative door on restart. Recorded here so it is a documented property, not a latent surprise.

## Charter alignment

Upholds **§6** (the admission policy is now identical across the Git, API, CLI, and agent surfaces — a caller cannot pick a weaker door), **§1.8** (no door reaches the graph with admission silently skipped; a deny is a loud 403 with the control's reasons), **§2.4** (the same fixed most-restrictive lattice as the Git door — no new precedence), and **§1.2** (the imperative door is still a declaration surface; admission gates it before it becomes graph state). No new Named Kind; no new dependency; the enforcement is over the existing port.

## Consequences

- **Positive:** the admission bypass is closed at all three imperative endpoints; one policy, one lattice, one port, every door; the object shape is consistent between doors (kind-preserving), so authors write a control once.
- **Negative / trade-offs:** boot-snapshot semantics on the imperative door (documented above) — a live-refresh (the server subscribing to the controller's last-parsed admission set) is a clean follow-up but adds coupling; deferred until an operator actually needs mid-run admission-policy changes to hit the imperative door without a restart.
- **Known refinement:** the Git door admits the **raw manifest** shape while the imperative door admits the **typed** shape; they agree on `kind` and top-level fields but a control reaching into a manifest-only field would see it at one door and not the other. Unifying `admitEstate` to admit the post-parse typed object (one shape everywhere) is the tracked hardening.

## Alternatives considered

- **Thread a Decider through `ComputePlan`/`Apply`** — rejected as the primary seam: those are pure graph-diff functions; admission is a request-boundary concern (like authz), so it belongs at the handler, before the diff, where the 403 is natural. `AdmitDeclarations` keeps the diff functions content-blind.
- **Trust the caller's `AdmissionControls`** (admit the incoming declarations against the policy in the same request body) — rejected: a caller would simply send an empty policy to self-exempt. The authoritative policy must be the **server's** estate snapshot, never the request's.
- **Admit only on `/apply`, not `/plan`** — rejected: `/plan` is a read-through preview of what `/apply` would do; showing a plan for a declaration that admission would reject is misleading (§1.8). Both admit.
