# ADR 0055 — Estate Composition: what it means to "define the estate"

- **Status:** Proposed
- **Date:** 2026-07-17
- **Deciders:** steward (dstout), charter-guardian
- **Charter sections:** §1.1, §1.2, §1.4, §2, §5, §8, and the permanent non-goals (§1.1/§7.5)

## Context

A recurring, expanding request: let an operator **define the estate from the simplest possible form** — "I
need N Linux servers on network X with template Z; certificates and applications handled" — with sane defaults
and optional overrides, and have Stratt fan that out across provision → network → DNS → cert → configure → app,
eventually consuming vendor Helm charts and driving Crossplane. This ADR does **not** build that. It fixes
**what "defining the estate" means** and the **guardrails** every future step must obey, so that the platform's
own §1 disciplines and permanent non-goals are never traded away for surface convenience. It is written now
because the vision brushes several non-goals (a new configuration language, a universal ontology, a writable
CMDB, silent auto-provisioning), and getting the model wrong once compounds forever.

**Battle-test of the current model** (grounded in the code, not aspiration): roughly 80% of the target scenario
already composes from existing typed primitives —
- provisioning is expressible on the **Workflow/Actuator plane** (`opentofu` Actuator with HCL `module`/`vars`;
  the `awsec2:create-vm` Action with typed `{instanceId, privateIp}` outputs), with **real cross-Step binding**
  `{{.steps.<name>.outputs.<field>}}` (ADR-0031 proved a live `provision→configure` chain end-to-end);
- desired-state convergence of a group is exactly the **Intent → Assignment → Blueprint → View compiler**
  (`core/internal/compiler/compiler.go`): a Blueprint is a template, a View is a group, an Assignment binds
  them with `{{.spec.x}}` parameterization, and the compiler fans out per-member facet-observation Baselines +
  gated remediation-Workflow refs;
- the entire templating surface is `core/internal/template/template.go` — bounded `{{.ns.path}}` dotted-path
  substitution (namespaces `spec`/`event`/`steps`/`param`), whose header already cites §1 "no new configuration
  languages"; there is no expression language, and tool content (ansible/HCL) is opaque to the core.

Six gaps separate that from the vision — **none requires a new configuration language**; all are closable with
typed fields + the existing substitution surface:

| # | Gap | Charter status |
|---|---|---|
| G6 | No defaults/override engine ("simplest form + sane defaults + optional overrides") | **charter-sensitive** — "override beats default" is implicit precedence (§2.4/§4.1) unless it is explicit overlay + claim-type merge |
| G5 | No unified onboarding template (template Z + View + cert + app as one defaulted/overridable unit) | charter-safe |
| G3 | No group-of-groups (Views are flat AND-conjunctions; no composition/nesting) | charter-safe (guard OR-creep) |
| G4 | Intent layer produces **checks**, not **builds** (compiler emits Baselines + Workflow *refs*, never launches) | charter-sensitive |
| G1 | No provisioning Intent — "declare N servers" has no declarative home | charter-sensitive |
| G2 | No cardinality/fan-out (the compiler fans out over *existing* members, never creates them) | charter-sensitive |

## Decision

**1. "The estate" is the typed graph; "defining the estate" is declaring typed primitives that compile and
orchestrate through plugins — composition, never a schema-of-everything.**
- The **estate** = the typed graph: projected Entities + Facets + Relations (what *exists*).
- The **desired estate** = a set of typed CaC declarations in Git — Sources, Views, Intents, Assignments,
  Blueprints, Baselines, Triggers, Workflows, CredentialRefs, plus authz — reconciled by the desired-state
  loop and the Intent compiler, executed through Connectors/Actuators over the sovereign port.
- Defining the estate is therefore **declaring those primitives and letting them fan out through plugins**. It
  is not authoring one universal model of the world.

**2. The five guardrails (binding on every future estate-composition step).**
1. **No universal ontology (§1.1).** Type the seams — plugin Contracts and named Facets — never whole Entities,
   never a global schema of "server/network/app". Core never models a domain generically.
2. **No new configuration language (permanent non-goal).** Templating stays Blueprints + `{{.ns.path}}`
   substitution + JSON-Schema-typed specs. Cardinality, defaults, and overrides are **typed fields + compiler
   fan-out**, never loops / conditionals / expression evaluation in config values.
3. **Boring spine, pluggable everything (§1.4).** Crossplane, Helm, DNS, and network provisioners are
   **plugins** (Actuators/Connectors behind the port + a typed Contract). Core never learns them.
4. **No writable CMDB / no OS imaging (permanent non-goals).** Devices are projected; "servers" arrive from a
   provisioning plugin and **project back** — never a device table the API writes to.
5. **No silent auto-launch (§5 Flow 1 "Gate on plan", §2.3 Gate, §4.3 max-delta).** Builds and remediation stay
   policy/human-gated. "Declare and it builds" MUST resolve to a **gated** generated Workflow — a saved-plan
   Gate + the max-delta blast-radius gate — never an automatic apply.
6. **No implicit precedence in composition (§2.4 anti-GPO, §4.1 "no inheritance, no last-writer-wins, ever").**
   Defaults, overrides, and grouping never introduce a priority/precedence/last-writer field. "Sane defaults +
   optional overrides" resolves via **explicit overlay** (Kustomize-style overlay directories) + **claim-type
   merge** (exclusive-double-claim fails the compile, additive unions) — the same discipline the Intent
   compiler already enforces. This is the one place the anti-GPO axiom is most tempting to trade for convenience;
   it is not tradeable.

**3. The G1–G6 taxonomy is the sequenced, ADR-gated roadmap.** Charter-safe gaps (G5, G3) may proceed as
ordinary typed extensions with a short ADR each. The **charter-sensitive** cluster (G6, G4, G1, G2) gets
dedicated ADRs before any code:
- **G6 (defaults/override):** resolved by guardrail 6 — explicit Kustomize-style overlay directories + §2.4
  claim-type merge, never a precedence field. A defaults/override *merge engine* is the anti-GPO axiom's
  softest target and does **not** proceed as a "charter-safe typed extension."
- **G1+G4 (provision-from-intent):** a provisioning Intent declares *desired existence*; the reconcile
  **generates a Gated provisioning Workflow** (the existing `provision→configure` seam, a plan-Gate per §5
  Flow 1), never a silent apply — reconciling "declare and build" with the no-auto-launch axiom.
- **G2 (cardinality):** a typed `count`/selector field fans out to N **gated** Steps whose built infrastructure
  **projects back** as Entities — the graph never holds desired-but-nonexistent rows (§1.2 preserved).

**4. Each new capability plugin (Crossplane, Helm, DNS, network) is its own ADR + Contract**, sandbox-tiered per
§7.3, and slots in behind the port with no core change.

## Charter alignment

This is a **§1/§2-touching decision at the highest review bar** — it defines the model the whole platform
composes within. It upholds: §1.1 (types the seams, forbids a universal ontology), §1.2 (desired state in Git,
graph stays a rebuildable projection), §1.4 (spine thin, breadth in plugins), §1.6 (the estate is authored once
and consumed identically by UI/CLI/CI/agents), §5 (the port is the abstraction; no tool is load-bearing in the
core), §8 (Phase-4 consolidation, paced). It explicitly **defends** four permanent non-goals against the
vision's pull (no new configuration language, no writable CMDB, no OS imaging, and — via guardrail 5 — the
no-silent-auto-launch safety posture). The one standing tension it names rather than hides: "declare N servers
and they build" is only admissible as a **gated** generated Workflow; an automatic build-from-declaration would
violate §5 and is out of scope until its own ADR reconciles it.

## Consequences

- **Positive:** a single, charter-grounded frame every future step (and every new plugin) slots into; the
  vision becomes reachable **incrementally** without a rewrite or a DSL; the non-goals are protected by an
  explicit, cited contract rather than case-by-case judgement.
- **Negative / trade-offs:** the "simplest form" experience arrives in typed increments (G6→G5→…), not in one
  leap; some scenarios that a DSL would express in one line require a typed primitive + a plugin instead — this
  is the deliberate cost of "type the seams, not the world."
- **Follow-ups:** ADR-0056 (Estate-as-Code — the Git-declarable foundation, incl. `sources/`); a dedicated ADR
  for G6 (defaults/override as §2.4/§4.1-safe explicit overlay + claim-type merge, NOT a precedence field);
  dedicated ADRs for the G1/G4 provisioning-Intent and G2 cardinality clusters;
  per-plugin ADRs + Contracts for Crossplane/Helm/DNS/network. The `vocabulary-linter` gains estate-composition
  checks (no `provider`/`inventory`/`resource`/`binding` synonyms for the Named Kinds; no config-language
  keywords). No CI evergreen change.

## Alternatives considered

- **A new estate DSL / IaC language** (the naïve reading of "heavy reliance on templating") — **rejected**: a
  permanent non-goal. Loops/conditionals/count-expressions belong in typed fields + compiler fan-out, not a
  language; tool-specific content (HCL, Jinja) stays opaque plugin content.
- **A universal "estate" schema modeling servers/networks/apps** — **rejected** under §1.1: that is the
  universal ontology the charter forbids. Capabilities are typed at plugin seams, not globally.
- **Auto-provisioning: declare desired existence → apply immediately** — **rejected** under §5 (no silent
  auto-launch); admissible only as a gated generated Workflow (G1/G4 ADR).
- **Leave "defining the estate" undefined and build features ad hoc** — **rejected**: without this frame each
  convenience request re-litigates the non-goals and eventually erodes one.

## Reviews

- **charter-guardian (2026-07-17): SOUND-WITH-CHANGES.** The framing actively defends the non-goals rather than
  eroding them; the G1/G4/G2 tension is named, not hidden. **Must-fix (folded):** G6 (defaults/override) was
  mis-classified "charter-safe" — a defaults+override merge is precisely where implicit precedence re-enters
  ("override beats default" = last-writer-wins), so it is reclassified **charter-sensitive** and bound by a new
  **guardrail 6** to §4.1 explicit-overlay + §2.4 claim-type merge (never a precedence field). **Flag (folded):**
  the no-auto-launch citation was tightened from "§5 Flow 2" to §5 Flow 1 (Gate-on-plan) + §2.3 Gate + §4.3
  max-delta — the machinery the gating actually leans on. No permanent non-goal is breached as written.
