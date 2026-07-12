# ADR 0023 — Intent/Assignment/Blueprint compiler

- **Status:** Accepted
- **Date:** 2026-07-12
- **Deciders:** Project steward (dstout)
- **Charter sections:** §2.4 (Intent layer, claim types, the anti-GPO axiom),
  §2.1 (Facet ownership registry), §4.1–4.3 (repo topology, worked example,
  safety machinery), §6 (power gradient), §8 (Phase 2)

## Context

The charter's centerpiece and largest Phase-2 line item: "Intent/Assignment/
Blueprint compiler with claim types, ownership registry, membership-delta
plan, max-delta gate." An **Intent** (declarative *what*) × an **Assignment**
(binds Intent → View, per env) × a versioned **Blueprint** (routes, per
capability-scoped Facets, to remediation + an observed Facet with a claim
type) compile into **Baselines + remediation Workflow refs** — and "drift
dashboards, Findings, Gates, and provenance fall out of the existing spine"
(§4.2).

## Decision

1. **Three CaC kinds** (`intents/`, `assignments/`, `blueprints/`), the
   established parse/validate/plan/apply/wire pattern. v1: `Intent/Application`
   only; `onRemove: retain` implemented (`revert`/`remove` parse-refused —
   schema-driven removal is Phase 3). Blueprints are versioned; `(name,
   version)` coexist so upgrades roll through rings. Assignments pin
   `blueprint@version`.
2. **Compile output = facet-observation Baselines, evaluated graph-side.**
   The compiled desired state is "these Entities should carry this Facet
   value" (§2.4 "expected Facet values"). A new Baseline mode
   `facet-observation` carries the compiled selector (View selector ∩ route
   match) + expectations + `CompiledFrom` origin. `RunBaselineCheck` branches
   on mode: the new `EvaluateFacetBaseline` activity resolves the selector,
   reads each Entity's Facets, marks any unmet expectation as drift, and
   feeds the **existing** ADR-0019 damping/Findings machinery — no execution
   pod. Findings carry no check-Run ref; the Temporal workflow history is the
   Evidence (v1; the object-locked Evidence store is Phase 3). Remediation
   stays a Workflow **ref** (§5 Flow 2: behind Gates).
3. **The compiler runs inside the desired-state reconcile cycle**, after the
   Intent-layer kinds are persisted, and **re-runs every pass** — membership
   drifts without Git changes (Syncer relabels), so compiled Baselines must
   re-derive continuously. Compiled Baselines are origin-stamped and
   compiler-owned: the hand-written `baselines/` kind excludes
   `CompiledFrom != nil` rows, and the compiler prunes only its own.
4. **Claim types — the anti-GPO axiom (§2.4), no implicit precedence.**
   Exclusive claims that collide over the same `(namespace, Entity)` across
   Assignments **fail the compile** (both named, both poisoned, no partial
   apply). Additive claims union (`contains` semantics). There is no
   priority, last-writer, or precedence field anywhere in the model.
5. **Ownership registry (§2.1).** A Blueprint claims write-ownership of a
   namespace **only for a route that manages it — i.e. declares a remediation
   Workflow** (guardian-refined). A pure-observation route reads a Facet
   (often Syncer-projected, like `os.kernel`) and never registers ownership,
   which removes any compiler-vs-Syncer registration-ordering hazard. Two
   distinct Blueprints managing one namespace conflict (`ErrOwnerConflict`);
   a namespace already Syncer/team-owned is read-observed without a claim.
   "One write owner per namespace" stays intact.
6. **Membership-delta plan + max-delta gate (§4.3).** A per-Assignment
   compiled-membership snapshot (`graph.assignment_membership`) is diffed
   each pass; joins/leaves surface in `GET /compile`. When the change exceeds
   the max-delta fraction (engine default 0.5, per-Assignment `maxDelta`
   override) the Assignment's recompile **pauses** — its prior compiled
   Baselines are kept, the pause is visible, and a deliberate `ackDelta` Git
   bump is the only unblock. `ackDelta` is an acknowledgement token, never a
   precedence mechanism.
7. **Orphan Findings (§2.4, §4.3).** A withdrawn Assignment (onRemove=retain)
   gets an orphan Finding (framework `orphan`) written *before* its compiled
   Baseline is pruned — abandoned state is never silent.
8. **Cross-reference validation at compile:** an Assignment's View must be
   cac-declared (§2.1 guardian constraint — desired state stays in Git), and
   its Intent, `Blueprint@version`, and each route's remediation Workflow
   must exist. Template substitution into observed values is **explicit field
   reference only** (`{{.spec.package}}`) — not an expression language
   (charter non-goal).
9. **API:** read-only `GET /intents`, `/assignments`, `/blueprints`, and
   `GET /compile` (the §4.3 membership-delta surface); compiled Baselines
   expose `mode` + `compiledFrom` for the §1.8 Finding → Baseline →
   Assignment/Blueprint descent.

## Consequences

- Verified live end-to-end: a `browser` Intent × two Blueprints (clean +
  drift) × Assignments on a 50-VM View compiled two facet-observation
  Baselines; the drift Blueprint opened 50 critical Findings, the clean one
  zero. Flipping the View to an empty set (50/50 change) **paused** both
  Assignments with prior Baselines kept; an `ackDelta` bump unblocked one and
  left the other paused. Two exclusive Assignments over the same Facet
  produced a **compile error naming both, with neither Baseline applied**.
  Withdrawing an Assignment wrote an **orphan Finding** and pruned its
  Baseline — and the desired-state prune-fraction guard refused a 3-of-4
  bulk delete until narrowed.
- Guardian-accepted notes: ownership is claimed only for managed
  (remediated) namespaces (above); and the max-delta gate does not cover an
  Assignment's **first** compile (0→N has no prior set to compare) — the
  desired-state prune-fraction guard and the plan's blast-radius member
  counts cover the initial rollout. Both are deliberate.
- vocabulary-linter fix: the remediation-Workflow ref field was
  `remediateWorkflow` on the Blueprint route and `remediationWorkflow` on
  the Baseline — one frozen concept, now uniformly `remediationWorkflow`.
- The Intent-kind GA (`Certificate`/`FileSet`/`Access`), the object-locked
  Evidence store, and executed remediation are Phase 3; v1 delivers the
  compiler machinery and the anti-GPO safety spine.
- `apps.installed` (the charter's literal observe example) needs an
  app-inventory Syncer (Phase 3); the e2e observes harness-populated
  `os.kernel`, which the compiler treats as a Syncer-owned read.
