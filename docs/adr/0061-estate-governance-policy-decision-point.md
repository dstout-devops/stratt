# ADR 0061 — Estate Governance: the policy decision point, the three authorships, and governance-as-data

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout); charter-guardian, dependency-scout, vocabulary-linter (reviewed 2026-07-18)
- **Charter sections:** §1.1, §1.4, §1.5, §1.6, §1.8, §2 (Gate · Contract · Finding · Evidence · Provenance · Principal · Assignment · Site · Baseline), §2.4, §2.5, §3, §4.3, §5, §8, and the permanent non-goals

## Context

Stratt has proven — end-to-end on real Crossplane (ADR-0058/0059) — the infrastructure control loop: declare → gated build → project back → drift Finding → gated convergence. The stated goal is to manage the **whole estate** (identities, applications, configuration, access, secrets, promotions) the same way. That goal is reachable **only if governance is not opinionated in the process** — different organisations impose different approval chains, policy checks, change windows, and separations of duty, and Stratt must host all of them without baking any one into the spine.

**Today there is exactly one checkpoint, and it is opinionated.** ADR-0011 shipped `Gate = {approvers.principals | approvers.teams, timeoutSeconds}` — a static human allow-list waited on by a Temporal signal (`types/workflow.go` `GateSpec`; `orchestrate/workflow.go` `runGateStep`; `POST /gates/{id}/decision`). ADR-0011 **explicitly deferred "policy Gates"** ("Deferred, not dropped: … policy Gates") and left a charter-guardian note that the inline principals/teams check "must fold into the single authorization model." This ADR is that deferred generalisation.

**The charter already names the missing machinery — it is scattered and mostly unbuilt:**
- **§2 Workflow** — "Gates (**human/policy** approval)": policy approval is in the frozen vocabulary; 0011 shipped only the human half.
- **§3 Central registry** — "**admission policies on manifests themselves — Kyverno-for-config**: 'no `exportable:true` cert Intents,' 'prod Assignments require a Gate,' 'team X may only target Views under org X.'" — a whole *declaration-time* policy surface.
- **§4.3 Safety machinery (mandatory)** — the **max-delta / blast-radius gate**: pause when a compiled target set changes more than a configured fraction between reconciles.
- **§5 Flow 4** — "**success-rate Gate between rings; maintenance-window guard from a calendar Facet**" — progressive-delivery and change-window gates.
- **§3 / §5** — trust-tier **delegation bound to the ownership registry** ("the platform team is a steward, not a chokepoint") + View-scoped execution authz — the separation-of-authorship model.

**Three authorship concerns collapse into one hand-authored Workflow YAML.** Whoever writes `estate/workflows/*.yaml` today fuses *what* (desired state), *how* (steps/order/plugins), and *who-may-under-what* (the gate). That does not scale across domains and it *is* the opinionation to remove.

**Scope.** Like ADR-0055 (Estate Composition), this ADR **fixes the governance model, the guardrails, and the sequenced roadmap**. It does **not** implement the control library, the admission surface, or any external-engine plugin — those are follow-up ADRs, each with its own Contract and charter pass. This decision was taken after four research sweeps (folded below): the internal gate/authz/CEL/Findings model; the policy-as-code engine landscape; the configurable-delivery / governance-injection prior art; and the change-governance control frameworks (ITIL 4, NIST 800-53 CM, ISO 27001, SOC 2, SLSA/in-toto).

## Decision

### 1. Generalise the Gate into a Policy Decision Point (PDP) with a four-way Decision

A **Policy Decision Point** evaluates a typed, engine-agnostic request and returns a four-way outcome — human approval becomes **one outcome**, not the primitive. The contract (a pinned, hash-verified Stratt schema, ADR-0015 pattern):

```
DecisionRequest {
  principal:  { id, kind, roles[], attr{} }       # the acting Stratt Principal
  action:     string                               # "run.apply" | "assignment.admit" | "blueprint.promote" | …
  resource:   { kind, id, attr{} }                 # Entity/Facet ref — kind from the frozen vocabulary
  context:    ChangeContext                         # ambient typed facts (below)
  refs:       { intent?, run?, workflow?, target? } # §1.8 one-click-descent linkage
  pin:        { policy_digest, revision }           # §1.5 hash-verified
}
Decision {
  outcome:     ALLOW | DENY | REQUIRE_APPROVAL | ESCALATE   # superset of allow/deny
  reasons:     [ { code, message, policy_ref } ]           # structured, never opaque (§1.8)
  obligations: [ { type, params } ]                        # binding: require_approval{count,from}, ttl, record_evidence, notify
  provenance:  { engine, engine_version, policy_digest, revision, evaluated_at }
}
```

`REQUIRE_APPROVAL` **is** today's human Gate, generalised (its obligation carries the approver selector + quorum); `ESCALATE` routes to a higher authority/queue. The record is stamped with the deciding engine + pinned policy revision (§1.2/§1.5). This is the exact shape OPA returns natively and that Cerbos/Cedar adapters normalise into.

**`ChangeContext`** is the one shared, typed evaluation input — the unifier that keeps the spine content-blind (every control is a pure predicate over it):

```
ChangeContext {
  actor: Principal;  committers: []Principal
  targets: [ { entity_ref, kind, environment, criticality? } ]
  blast_radius: { entity_count, service_count, max_criticality? }   # feeds the §4.3 max-delta gate
  environment: enum(dev|stage|prod|…);  change_class: enum(standard|normal|emergency)
  risk_score?: number;  scheduled_at: timestamp;  labels: map<string,string>
}
```

`criticality`, `max_criticality`, and `risk_score` are **optional, sparse, computed/Contract-demanded coordinates — never required universal Entity attributes** (the band/beam discipline of ADR-0046, not a CSDM-style ontology, which §1.1 forbids). They are populated only where a Contract or a risk-scorer supplies them; **absent ⇒ fail-safe** — evaluated as most-critical / most-restrictive, never as "no risk." `entity_count`/`service_count`/`environment`/`change_class` are structural facts the spine already knows.

### 2. Two enforcement points (PEPs), one PDP

The charter names two policy surfaces; they share the one PDP and differ only in **where** they enforce:

| PEP | Where it runs | Charter anchor | Prior art |
|---|---|---|---|
| **Admission** | at the desired-state **write / compile** seam — a declaration or a compiled Blueprint is admitted only if policy passes; actor-independent | §3 "admission policies … Kyverno-for-config" | K8s ValidatingAdmissionPolicy (CEL, in-process, authored separately from workloads) |
| **Gate** | at **Run phase boundaries** — pre-dispatch / pre-apply / post-apply | §2 Gate, §4.3 max-delta, §5 Flow 1 "Gate on plan", Flow 4 window/success-rate | Spacelift plan/approval points; GitHub deployment protection rules; Argo sync waves/windows |

The existing human Gate, the **maintenance-window** guard, and the **success-rate/analysis** gate are *Gate-PEP instances* over the same PDP — not separate mechanisms.

**Mandatory floors are compiled in unconditionally — never ControlSet-authored.** The charter marks certain gates **mandatory** (§4.3 "all mandatory"): the **max-delta blast-radius gate** (§4.3), the **Flow-1 plan-gate** (§5), and the **orphan/drift Findings** (§2.4). These are emitted by the framework itself on every qualifying Run, are **not** expressible or omittable in a ControlSet, and a policy `ALLOW` cannot pass them. A ControlSet may only **add** restriction on top of the floors — never subtract one. The **sole** bypass is break-glass (guardrail 6) with its heightened audit + mandatory post-review. This is what keeps an automated `ALLOW` honest: the floor still fires (guardrail 3).

### 3. Tiered evaluators: CEL built-in, the Control library as data, external engines as plugins

| Tier | Evaluator | Role | Dependency |
|---|---|---|---|
| 0 | **CEL** — reuse `core/internal/rules` (cel-go, hermetic, cost-bounded, fail-closed), widen its env from `event/emitter` to the full `ChangeContext` | inline boolean guard: `env=="prod"`, `blast_radius.entity_count > 20`, team/time predicates | **already in core** |
| 1 | **The Control library** — typed control primitives evaluated by the spine over the shared `ChangeContext` (§4 below) | the built-in governance vocabulary (Approval, SoD, TimeWindow, Waiver, Quorum, BreakGlass) — **data, not code** | none |
| 2 | **External PDP plugins** — behind the sovereign policy Contract | rich org policy estates | **plugin only, never core** |

**The built-in PDP is CEL + the Control library — not embedded OPA.** The policy-engine research recommended embedding OPA/Rego in-core as the default. We **deviate on charter grounds**: embedding OPA puts a second policy language and a heavy dependency inside the content-blind spine, contradicting ADR-0046 (every tool is a plugin), §1.4 (boring spine), and §1.5 (no external protocol load-bearing for the deterministic core). Stratt already owns a hermetic CEL evaluator; the four-way outcome is produced by the thin Control-library composition layer (ordered `{when: <CEL>, outcome, obligations}` rules) — typed-fields-plus-CEL, exactly ADR-0055 guardrail 2, **not a new language**.

A built-in evaluator is **required**, not merely convenient: the mandatory floors (§4.3/§5) must fire even when no external engine is installed — if they depended on an optional plugin, that plugin would become load-bearing for the deterministic core (§1.5). And the PDP evaluates the **typed governance Envelope / `ChangeContext` only — never the opaque Payload** (the HCL/playbook/Helm content stays opaque to the spine, ADR-0046). Policy is typed at the seam (§1.1), not interpreted from tool content.

**External engines are first-class plugins** over one Stratt-owned policy Contract (each adapter normalises to the `Decision` shape):

| Engine | License | Plugin role |
|---|---|---|
| **OPA / Rego** | Apache-2.0 (CNCF graduated) | flagship *recommended* rich PDP; decisions are arbitrary JSON → native four-way + obligations. **Optional-only, never core-bundled** — ~50 transitive deps (embedded KV, WASM runtime, OTel/OCI stack) must stay behind the plugin boundary; a CI go.mod-graph diff guards against its deps creeping into `core/` |
| **Cerbos** | Apache-2.0 (PDP) | Go-native gRPC PDP; YAML+CEL policies for non-programmer authors (never depend on the commercial Cerbos Hub) |
| **Cedar** | Apache-2.0 | formally-verified RBAC/ABAC for high-assurance gates — integrate over subprocess/gRPC against the **Rust reference**, *not* the partial-parity `cedar-go` (which lacks the validator/partial-eval that justify picking Cedar) |
| **Kyverno-JSON** | Apache-2.0 (CNCF graduated) | validate a compiled OpenTofu/Crossplane **plan** pre-apply (admission PEP) |
| ~~HashiCorp Sentinel~~ | **proprietary** | **excluded** — un-embeddable, incompatible with the Apache-2.0 posture |

**OpenFGA stays the single authoritative grant layer (§1.6) — the PDP never forks authz.** ReBAC answers *who relates to what* (View-scoped execution, team membership); the PDP answers *whether an action is permitted / needs approval / escalates given context*. They compose in a fixed order: **OpenFGA is evaluated first and is authoritative; the PDP can only add restriction on top** — a policy `ALLOW` can never override an authz `DENY` (deny-composition). The PDP calls `authz.Authorizer.Check` for relationship predicates and evaluates CEL/plugins for content/context predicates; the 0011 inline principals/teams check folds into `authz.Authorizer` (§7.2), discharging 0011's deferred note.

### 4. The three authorships, separated by target-anchored governance

The pattern is **GitHub/GitLab deployment-protection-rules generalised** — governance attaches to the *target*, not the process — fused with the **Crossplane/Kratix plugin-pipeline** process model. Three artifacts, three personas, one shared **target identity**:

| Concern | Artifact | Persona | Prior art |
|---|---|---|---|
| **WHAT** | **Intent** — pure desired state, no steps/gates | resource owner (app/infra team) | Crossplane claim, Kratix resource request |
| **HOW** | **Blueprint → Workflow** of plugin-backed Steps (Ansible/OpenTofu/Helm/Crossplane/MCP over the port); a *compiled, reusable* artifact | platform | Crossplane composition functions, Kratix Promise pipelines |
| **WHO-MAY-UNDER-WHAT** | a **ControlSet attached to the TARGET** (Site / Assignment / Baseline — a stable identity), evaluated by the spine at Run phase boundaries | governance / security / change-management | GitHub/GitLab environment protection rules, Spacelift policy, K8s VAP |

**`ControlSet` is a typed Facet on the target, not a new Named Kind.** Per the vocabulary-linter, it is modelled as a governance Facet (e.g. `governance.controls`) on the existing frozen Kinds Site / Assignment / Baseline — a schema, hash-verified like any Facet — **never** a standalone entity type, DB table, or CLI noun (`stratt control …`). "ControlSet" is a pattern name for that Facet's shape, exactly as "Workflow" names a Temporal DAG rather than a distinct graph entity. Any move to make it a first-class entity would require the §2 freeze bar.

The process Workflow and the governance ControlSet-Facet are **independent artifacts that both reference the delivery target by stable identity, composed at Run-compile time — neither edits the other**. That indirection (the `environment:`-name generalised) is what makes Stratt "not opinionated in the process": the same reusable Workflow runs under any org's controls, and governance is authored without touching the process. dev→stage→prod promotion, SoD, freeze windows, and break-glass are all **control Facet configuration on the target** (one target per stage), never Workflow edits.

### 5. Every decision is a Finding + Evidence in Provenance — reuse the frozen Kinds

A governance decision is recorded with the existing Named Kinds — **no new attestation substrate, no new noun**. **Every** decision stamps **Provenance**, seals a supporting bundle into the object-locked **Evidence** store (ADR-0029), and appends to the one hash-chained audit stream (`AuditGateDecision` already exists, ADR-0034); the record is SLSA/in-toto-shaped (subject = the Run/target, predicate = the decision) and enumerates **all** contributing controls' reasons — not only the winning one — so one-click descent (§1.8) shows the full evaluation, not a truncation.

A **Finding is minted only on a compliance-relevant outcome** — `DENY`, a waiver-applied pass, a break-glass bypass, or an overridden control — since §2.4 defines a Finding as a drift/compliance *problem* record; a clean automated `ALLOW` records Provenance + Evidence but **no Finding**, so the drift dashboard is not polluted by routine permits. Governance Findings are `framework: governance/{control_type}`-tagged, and their **subject may be the target** (Site / Assignment / Baseline), not only an Entity — the §7.1 follow-up confirms the Finding schema admits a target subject before shipping. Together this satisfies NIST CM-3/AU, SOC 2 CC8.1, ISO A.8.15.

### 6. Guardrails (binding on every governance step)

1. **Governance is data over a closed vocabulary, never code.** Controls are typed fields + CEL predicates, pinned and hash-verified (§1.5 schema-drift-is-blocking). CEL is admissible as a **predicate/guard** language only (as already chartered for Triggers), **never a desired-state value language** (ADR-0055 guardrail 2 holds). The Control library is a **closed, typed set** of primitives (Approval, SoD, TimeWindow, Waiver, Quorum, BreakGlass) with a **closed obligation enum**; org authoring is *parameterisation*, not language or obligation extension — a new primitive or obligation type is a typed Facet + its own ADR + a linter pass, never an inline escape hatch. Governance-as-code would breach the spine's content-blindness (§1.4/ADR-0046).
2. **No precedence field (§2.4).** **All** controls in a set are **always evaluated** (order is display-only and non-semantic — never a short-circuit that changes the recorded reason set). Outcomes combine by a single, fixed, **non-configurable most-restrictive-wins lattice — `DENY > ESCALATE > REQUIRE_APPROVAL > ALLOW`** — which is order-independent and fail-closed. There is **no** configurable / allow-overrides / first-applicable combinator and **no** priority scalar. "Any DENY wins" is therefore a fixed order-independent monotone (the direct analogue of §2.4 additive-union), not implicit precedence.
3. **ALLOW is not silent auto-launch, and never crosses a mandatory floor (§5).** An automated `ALLOW` may let convergence proceed, but the mandatory floors (§4.3 max-delta, §5 Flow-1 plan-gate, §2.4 orphan Findings) are framework-compiled and fire regardless — a policy `ALLOW` cannot pass them (decision 2). The permit records Provenance + Evidence, is fully audited and reversible, and remains diagnosable — it removes a *click*, never the *diagnosis* (§1.8). Auto-remediation without a human is admissible only where a policy explicitly authorises it *and* the floors still hold; this is the one place that most tempts a §5 breach and is bound by this guardrail.
4. **Waivers are time-boxed (mandatory `expires_at`).** Kyverno's missing expiry is a known footgun; a Stratt waiver without an expiry fails to compile.
5. **A decision timeout never defaults to approve.** On approver inaction: `escalate | reject | route-manual` — never auto-allow.
6. **Break-glass still emits full evidence.** Emergency bypass is a first-class, heightened-audit path with a **mandatory post-review** obligation — bypass ≠ silence.
7. **Do not mint new Named Kinds without the linter (§2).** "Gate" is frozen; "Control", "Waiver", "ControlSet", "decision point" are **not**. Express governance through **Contract → Finding → Evidence + typed Facets on the target**; run `vocabulary-linter` before any becomes a core-model identifier.

### 7. Sequenced follow-up ADRs (nothing below is built here)

1. **Policy Contract + PDP interface v1** — the `DecisionRequest`/`Decision` schema + the CEL built-in evaluator (widen `rules` env; the outcome-composition layer). The policy CEL env must be **more strictly builtin-subsetted than the trigger env** (Control authors are less-trusted than today's platform-authored triggers). The minimum that makes a policy check a Step/PEP.
2. **The Gate-PEP generalisation** — fold today's human Gate + §4.3 max-delta into the PDP dispatch (`RunDAG` step switch); the human path becomes the `REQUIRE_APPROVAL` obligation; unify the inline principals/teams check into `authz.Authorizer` (0011's deferred note).
3. **The Control library** — Approval/SoD/TimeWindow/Waiver/Quorum/BreakGlass as typed data; risk-scorers, metric providers, and delegation graphs as the pluggable tier.
4. **The admission PEP** — "Kyverno-for-config" at the desired-state compile seam (§3), with Kyverno-JSON as the first plugin.
5. **External-engine plugin v1** — OPA behind the policy Contract, **optional-only, never core-bundled** (dependency-scout confirmed: ~50 transitive deps); Cedar over subprocess/gRPC against the Rust reference (not `cedar-go`); a CI go.mod-graph diff keeps any engine's deps out of `core/`. Also plan the routine `google/cel-go`→`cel-expr/cel-go` import-path bump.
6. **Target-anchored governance binding** — the `governance.controls` Facet attached to Site/Assignment/Baseline, evaluated at Run boundaries; the three-authorship separation made real.

## Charter alignment

This is a **§1/§2-touching decision at the highest review bar** — it defines the governance model the whole platform composes within, and it **realises charter-named-but-unbuilt concepts** rather than inventing: §2 "Gates (human/policy approval)", §3 admission/Kyverno-for-config, §4.3 max-delta, §5 Flow-1/Flow-4 gates, §3/§5 trust-tier delegation. It upholds §1.1 (types the seam — a policy Contract, never a global policy ontology), §1.4 (CEL + Control library in the boring spine; every engine a plugin), §1.5 (one sovereign policy Contract; OPA/Cerbos/Cedar are transports beneath it, none load-bearing), §1.6 (one Principal/authz/audit/cost model — the PDP composes with OpenFGA, never replaces it; identical for UI/CLI/CI/agents), and §1.8 (every decision is a queryable Finding with structured reasons — one-click descent preserved). It defends the non-goals: no new configuration language (typed fields + CEL-as-predicate over a closed primitive/obligation set, not a DSL — guardrail 1), no writable CMDB, no paid tier (Apache-2.0 engines only; Sentinel excluded). **Named tensions, resolved by the folded charter-guardian must-fixes (see Reviews):** (a) §2.4 — controls are always fully evaluated and combine by a fixed order-independent most-restrictive-wins lattice, not precedence (guardrail 2 / M3); (b) §5/§4.3 — mandatory floors are framework-compiled and un-omittable, and an automated `ALLOW` cannot cross them, so it removes a click not the diagnosis (decision 2 + guardrail 3 / M1); (c) §1.6 — OpenFGA stays the single authoritative grant layer, the PDP adds restriction only (decision 3 / M2); (d) §1.1 — `criticality`/`risk_score` are optional sparse computed coordinates, fail-safe when absent, never a universal ontology (decision 1 / M4); (e) §2 vocabulary — `ControlSet` is a typed Facet not a Named Kind, and new nouns are gated on `vocabulary-linter` (guardrail 7 / F1). Each follow-up ADR (§7) carries its own charter pass.

## Consequences

- **Positive:** one governance model that spans infra, identity, apps, config, access, and promotions without special-casing any; the human Gate is preserved as the `REQUIRE_APPROVAL` degenerate case, so nothing regresses; governance and process are authored by different personas and evolve independently (the "not opinionated in the process" goal, structurally enforced); external policy investments (Rego/Cerbos/Cedar) plug in without a core change; every decision is attestable on the existing Finding/Evidence/audit substrate.
- **Negative / trade-offs:** the governance experience arrives in typed increments (the §7 sequence), not in one release; CEL + the Control library deliberately cover the common case, so the richest policies require an engine plugin (the cost of keeping the spine content-blind); target-anchored governance adds an indirection (process and controls reference a shared target identity) that authors must learn — the same indirection GitHub environments require.
- **Follow-ups:** the six sequenced ADRs (§7); `vocabulary-linter` re-run on each concrete schema before it becomes a shipping identifier; the `docs/oss-connector-tool-landscape.md` candidate list gains a **policy-engine** section (OPA/Cerbos/Cedar/Kyverno-JSON — it currently has none; added by this ADR). Dependency-scout has ruled OPA **optional-only**; no CI evergreen change until an engine plugin lands, at which point wire its N-1 pin + a `go.mod`-graph diff keeping engine deps out of `core/`.

## Alternatives considered

- **Embed OPA/Rego as the in-core default PDP** (the policy-engine research's headline) — **rejected** for core: a second policy language + heavy dependency inside the content-blind spine breaches §1.4/§1.5/ADR-0046. OPA is adopted instead as the recommended *plugin*; CEL + the Control library are the built-in.
- **Extend the human Gate with a few more approver fields** (bigger allow-lists, M-of-N) — **rejected**: it entrenches "governance = human approval" and never reaches admission policy, blast-radius, windows, waivers, or external engines. The Gate must become one *outcome* of a decision, not the decision.
- **Overload OpenFGA to express all policy** (contextual tuples/conditions) — **rejected**: ReBAC answers relationships, not arbitrary context/content predicates or obligations; forcing risk/blast-radius/window logic into a relationship graph is the wrong tool. Keep ReBAC and the PDP as complementary layers.
- **Mint new `Gate`/`Control`/`Policy`/`Waiver` Named Kinds** — **deferred to `vocabulary-linter`**: the frozen vocabulary already has Gate, Contract, Finding, Evidence, Provenance; express governance through those + typed Facets rather than expanding §2 by drift.
- **Author governance inside each Workflow** (status quo, or the Kratix "controls live in the platform-owned pipeline" model) — **rejected**: it re-fuses the process and governance authorships and makes the process opinionated. Governance attaches to the target, composed by identity (the GitHub/GitLab model).

## Reviews

- **charter-guardian (2026-07-18): SOUND-WITH-CHANGES.** The direction defends the disciplines rather than eroding them and realises charter-named machinery rather than inventing. **Must-fixes (all folded):**
  - **M1 (§4.3/§5):** mandatory floors (max-delta, Flow-1 plan-gate, orphan Findings) were being downgraded to omittable ControlSet instances. Folded into decision 2 + guardrail 3 — floors are framework-compiled, un-omittable, and un-passable by a policy `ALLOW`; the sole bypass is break-glass with heightened audit.
  - **M2 (§1.6):** the PDP could have forked authz. Folded into decision 3 — OpenFGA is authoritative and evaluated first; the PDP composes as added restriction only (deny-composition); a policy `ALLOW` never overrides an authz `DENY`.
  - **M3 (§2.4):** "short-circuit" contradicted "record all" and left the combinator unspecified. Folded into guardrail 2 — all controls always evaluated (order display-only), a fixed non-configurable most-restrictive-wins lattice (`DENY > ESCALATE > REQUIRE_APPROVAL > ALLOW`), no priority scalar.
  - **M4 (§1.1):** `criticality`/`risk_score` risked a universal ontology. Folded into the `ChangeContext` note — optional, sparse, computed/Contract-demanded, fail-safe when absent.
  - **Should-fixes (all folded):** S1 — a clean `ALLOW` records Provenance+Evidence, a Finding is minted only on a compliance-relevant outcome (decision 5); S2 — the built-in evaluator is *required* by §1.5 and evaluates the typed Envelope only, never the opaque Payload (decision 3); S3 — CEL is predicate-not-value and the Control primitive/obligation set is closed (guardrail 1); S4 — the decision record enumerates all contributing reasons (decision 5).
- **dependency-scout (2026-07-18): RECOMMEND.** `cel-go` (v0.29.2, in core) is a boring evergreen dependency; leaning on it for policy is additive — with the caveat (folded, §7.1) that the policy CEL env be more strictly builtin-subsetted than the trigger env, and a routine `google/cel-go`→`cel-expr/cel-go` import-path bump (§7.5). **OPA verdict: purely optional, never core-bundled** — ~50 transitive deps (embedded KV, WASM runtime, OTel/OCI stack) would double the core graph; ship it as the flagship *recommended* plugin behind the sovereign boundary, with a CI go.mod-graph diff guarding `core/`. Cerbos and Kyverno-JSON safe as plugins (avoid Cerbos Hub lock-in). **Cedar: integrate over subprocess/gRPC against the Rust reference, not the partial-parity `cedar-go`** (which lacks the validator/partial-eval that justify choosing Cedar). Sentinel exclusion confirmed. All folded into decision 3's engine table + §7.5.
- **vocabulary-linter (2026-07-18): clean — no banned terms, no blocking issues.** `PDP`/`PEP`/"decision point" OK as architecture roles; `ChangeContext`/`DecisionRequest`/`Decision` OK as Contract-payload type names; `Waiver` OK as a Facet/obligation record (not a Kind). **Clarity fix (folded):** `ControlSet` is pinned as a typed Facet (`governance.controls`) on Site/Assignment/Baseline, never a standalone Named Kind/table/CLI noun (decision 4). Reusing `Finding` (framework-tagged, with a target subject) and `Gate` (as the `REQUIRE_APPROVAL` outcome) is legitimate completion of frozen Kinds, not overloading — with the §7.1 requirement that the Finding schema admit a target subject before shipping.
- **Accepted (steward, 2026-07-18)** with all four must-fixes and four should-fixes folded into the body; the ADR is now immutable — supersede, don't rewrite. Follow-up §7.1 (Policy Contract + PDP interface v1) begins under this frame.
