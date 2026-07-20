# ADR 0085 — Relation-presence Baseline: desired state over graph topology, not just node facets

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** steward (dstout)
- **Reviews:** charter-guardian PASS-WITH-CHANGES — `RequiredRelations` approved as a tool-blind
  governance primitive (the topology sibling of `FacetExpectation`); two must-fixes folded: **MF-1**
  the §1.1 schema demander is the **ansible-project Connector's facet write path**, NOT the orphan
  Baseline (a presence check demands no content schema — a *writer* does); **MF-2** this warrants its
  own ADR (a spine desired-state extension), not a fold into ADR-0033. Flag folded: the orphan Baseline
  sets `dampingObservations` and documents the Syncer-lag false-positive mode (§4.3/§1.8). vocabulary-linter
  PASS on the `ansible.*` projection kinds (prior slice).
- **Charter sections:** §1.4 (boring spine — a tool-blind predicate, no relation semantics in core),
  §1.1 (type the seams — the schema is demanded by a shipping Connector Contract), §1.2 (projections,
  never a second truth — the Baseline reads, opens Findings, writes nothing), §2.4 (no implicit
  precedence — a pure existence predicate, no merge/winner), §1.8 (never hide failure — the false-orphan
  mode is documented + damped)
- **Mirrors:** ADR-0033 (hand-written facet-observation Baselines — the reused Finding/damping/evidence
  machinery), ADR-0023 (compiler-emitted facet-observation Baselines — same evaluator), ADR-0042/0082
  (per-source liveness — why a dropped cross-source edge is the honest orphan signal)
- **Unblocks:** the AWX-estate "orphan template" audit (`awx-template-covered`), and any governance that
  asserts an Entity *must be connected* (a device must be placed in a subnet, a service must depend on
  something) rather than merely carry a facet value.

## Context

A facet-observation Baseline (ADR-0033/0023) evaluates desired state **per node**: for each Entity in the
Baseline's View, read its Facets and check `Expected []FacetExpectation` (operators `equals`/`contains`/
`notBefore`). The evaluator (`orchestrate/baseline.go` `EvaluateFacetBaseline`) has **no relation
awareness** — it treats "a missing Facet is drift" but cannot express "a missing **edge** is drift."

The `ansible` domain now spans two read-only Syncers: the AWX Connector (orchestration — `ansible.template`
etc.) and the ansible-project Connector (primitive content — `ansible.playbook` etc.). AWX projects a
cross-source `ansible.template --runs--> ansible.playbook` edge; per the host's no-vivify rule
(`pluginhost/host.go`, ADR-0042/0082) an edge whose target is not projected by any known project is
**dropped, never vivified**. So a job template whose playbook lives in no managed content root has **no
`runs` edge** — an *orphan*, running content Stratt cannot see. Detecting that is a **topology** check
("this template must have an outgoing `runs` edge"), which the per-node facet engine cannot voice.

## Decision

**Add `RequiredRelations []string` to `types.Baseline` — a tool-blind presence predicate evaluated
graph-side, the topology sibling of `Expected []FacetExpectation`.** For a facet-observation Baseline, each
targeted Entity must carry **≥1 outgoing relation of each named type**; a missing one is an unmet
expectation = drift, feeding the identical §4.3 flap-damping / Finding / Evidence / Notice machinery.

### 1. The predicate
`EvaluateFacetBaseline`, per targeted Entity, after the facet expectations, calls the existing
`store.RelationTargets(ctx, entityID, relType)` (`graph/reader.go`) for each `RequiredRelations` type; an
empty result is unmet (`reason: "relation absent"`). A Baseline may set `expected`, `requiredRelations`, or
both; `ValidateBaseline` is relaxed to accept `requiredRelations`-only (each entry a non-empty string).

### 2. Tool-blind by construction (§1.4)
The spine gains exactly one general predicate — *"an Entity should have an outgoing edge of type T"*. The
relation-type strings (`runs`) live entirely in the CaC Baseline doc and the plugin; core never switches on
relation semantics, never learns "ansible" or "runs". This is the structural analog of the facet engine,
which already treats presence-of-facet as checkable — this adds presence-of-edge.

### 3. The §1.1 schema demander is the Connector, not the Baseline (MF-1)
This slice ships `contracts/facets/ansible.playbook.schema.json` (closed: `name`/`path`/`plays`/`hosts`).
**The Contract that demands it is the ansible-project Connector's playbook-facet projection** — its manifest
`ContractDecl{SchemaId: "ansible.playbook"}`, whose write path (`projector.go` `upsertFacetTx` →
`contract.ValidateFacet`) flips the namespace `covered=false → covered=true`, validating every projected
playbook facet (§1.1 progressive hardening). **The orphan Baseline is the motivating governance consumer,
but it is NOT the §1.1 demander:** a relation-*presence* check reads zero playbook facet content and would
evaluate identically with no schema at all. The rule this sets, to keep the gate honest: **a presence check
never demands a content schema; a writer does.** A future Facet schema whose only "consumer" is a
relation-presence Baseline would be a real §1.1 violation.

### 4. The orphan Finding's soundness depends on Syncer completeness (§4.3/§1.8)
A playbook not yet enumerated by the ansible-project Syncer → the `runs` edge drops → the template looks
orphaned = a **false positive driven by projection lag**, not real drift. This is mitigated, never hidden:
the reused §4.3 flap-damping absorbs transient lag (`dampingObservations` is set on the consumer), and the
failure mode is documented on the Baseline so a false orphan is diagnosable (§1.8), not mysterious. A
persistently-behind Syncer still false-positives — that is an operability property of the mirror, surfaced
rather than papered over.

## Charter alignment
- **§1.4:** one tool-blind predicate; no relation/tool semantics in the spine; the type strings are CaC.
- **§1.1:** the schema is demanded by the Connector's shipping Contract (the write seam), not the check.
- **§1.2/§2.4:** the Baseline is pure-read — opens Findings, writes no attribute, makes no claim, selects no
  winner. The cross-source edge has a single writer (AWX provenance); its target has a single facet owner
  (ansible-project). AWX holds `ansible.playbook` as a *pointable* IdentityScheme but not a FacetNamespace —
  the §2.1 two-writers-to-one-namespace trap, avoided.
- **§1.8:** the projection-lag false-orphan mode is damped + documented, not silent.

## Consequences
- **Positive:** desired state can now assert graph **topology** (an Entity must be connected), reusing all
  Finding/damping/evidence plumbing; the AWX orphan-template audit ships; the primitive generalizes (a
  device must be placed in a subnet; a service must depend on something).
- **Negative / trade-offs:** the orphan signal inherits the ansible-project Syncer's completeness (the §4.3
  lag mode above); `RequiredRelations` is presence-only by design.

## Scope boundary (the non-goal line — watchlist)
`RequiredRelations` is a **flat, typed presence list** — never a query grammar. Any future growth toward
cardinality ranges (`exactly N`), target-facet predicates (`runs a playbook WHERE plays>0`), or edge
expressions is the **"no new configuration language"** permanent non-goal line, and must return through
charter-guardian review before it becomes a DSL embedded in `Baseline`. Presence-of-edge is the primitive;
a topology *query language* is out of scope, permanently, absent its own ADR.

## Alternatives considered
- **A graph-query-Actuator check Step** (a Baseline whose check Step queries the graph for
  templates-without-`runs`) — rejected: it needs a new query Actuator + Contract, far more surface than one
  typed presence field, and it puts topology logic in a tool rather than the tool-blind spine.
- **Store dangling edges + check for a dangling `runs`** — rejected: it fights the §1.2 no-vivify rule
  (`host.go`), forcing a placeholder target Entity to exist. Working *with* the no-vivify drop (orphan =
  absence of the resolved edge) is the charter-honest read.
- **A facet on the template computed cross-source ("playbookResolves: bool")** — rejected: no single Source
  can compute it (module isolation), and it would be a second truth (§1.2). Topology is read at evaluation,
  never materialized as a node attribute.
