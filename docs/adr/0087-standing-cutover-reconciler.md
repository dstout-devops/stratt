# ADR 0087 — Standing cutover: a desired-state⋈projection reconciler, not a projection-only Baseline

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** steward (dstout)
- **Reviews:** charter-guardian PASS-WITH-CHANGES — **Arch 1 (reconciler) chosen; Arch 2 (write `adopted`
  into the projection) REJECTED on §1.2**; own ADR required (this corrects ADR-0086 §4's mechanism). Five
  must-fixes folded (below). vocabulary-linter PASS — `adoptedFrom` (structured Workflow field, camelCase,
  mirrors `CompiledFrom`), `ansible-cutover` framework tag, internal `cutover` reconciler name all clean;
  a new "CutoverPolicy" Named Kind **FAIL** (vocabulary is frozen — reuse the Finding/Baseline surface).
- **Charter sections:** §1.2 (projections never a second truth — `adopted` is DERIVED at evaluation, never
  stored), §1.4 (boring spine — the tool-specifics ride the Connector manifest; the reconciler is tool-blind),
  §1.8 (never hide failure — flap-damped, two false-fire axes documented, the Finding descends to both ends),
  §2.4 (no precedence — a pure predicate), §7.6 (strangler-fig cutover)
- **Supersedes-in-part:** ADR-0086 §4 — its stated mechanism ("a facet-observation Baseline, ADR-0085
  machinery") is unbuildable (a projection-only Baseline cannot join a Git desired-state fact); replaced by
  the reconciler here. ADR-0086 §4's *intent* (make the cutover explicit, never silent) stands.
- **Builds on:** ADR-0086 (adopt; the adopt-time report guard is the one-shot twin of this standing check),
  ADR-0085 (the `ansible.*` projection + the `schedules` edge + `ansible.schedule.enabled` facet), the
  established finding-GC reconciler pattern (`WriteGovernanceFinding` + `ResolveCleared…`).

## Context

After `stratt adopt` materializes an observed AWX object into a Git-declared Named Kind, the SAME real
object is both still projected read-only (AWX executes it via an enabled schedule) AND now declared in Git
(Stratt executes it). Unmarked, it runs in **both** places. ADR-0086 §4 required a standing governance
signal and assumed a facet-observation Baseline — but "is template X adopted?" is a **Git desired-state
fact** (a Workflow with `adoptedFrom: X`), and the ADR-0085 Baseline engine reads the projection only. The
check is fundamentally a **join** between desired state and the projection; a projection-only Baseline
structurally cannot voice it.

Two architectures were weighed. **Arch 2 — copy the Git fact into the graph** (write an `adopted` marker
onto the projection entity, then a plain Baseline sees it) — was **rejected on §1.2**: it reopens ADR-0086
§2's settled "adopted is derived, never stored"; Run-provenance is the sanctioned second writer only for a
Run's actual effect on the external object, and adoption never touched AWX; and it fails the rebuild test
(AWX has no notion of "adopted", so on rebuild the marker vanishes — proving Git is the truth and the graph
copy is a drift-prone cache, textbook second truth). Its "follow edge, check target facet" predicate also
crosses ADR-0085's own "no new configuration language" non-goal line.

## Decision

**The standing cutover is a cadence reconciler that performs the join in code — reading the adopted set from
desired state and the live executors from the projection — and opens Findings. It writes nothing to the
projection graph; "adopted" is derived at evaluation time, never materialized.**

### 1. Structured `adoptedFrom` on the Named Kind (MF-5)
`adopted-from` is promoted from a file-header comment (ADR-0086) to a structured field on the desired-state
Workflow — `adoptedFrom: {kind, identity, source}` — mirroring `Baseline.CompiledFrom`. It lives on the
Named Kind's lineage in Git (and the stored Workflow spec), **never as a projection facet** (§1.1/§1.2). The
header comment stays too (human-readable descent).

### 2. The cutover descriptor rides the Connector manifest (MF-2, §1.4)
The tool-specifics — which projected kind, which inverse relation names the executors, which facet+path+value
means "still live" — are declared by the Connector in its manifest (`CutoverDescriptor`, proto field 12), so
they travel with the plugin binary and core never learns "ansible". For AWX: `{target_kind:
ansible.template, relation: schedules, liveness: ansible.schedule / enabled / "true"}`. **No hardcoding in
core; no new "cutover policy" CaC kind** (the vocabulary is frozen — if operators need to scope which adopted
objects are enforced, that is an ordinary View+Assignment concern). This is the ADR-0085 MF-1 principle: the
descriptor's owner is the Connector, not the check.

### 3. The reconciler (tool-blind)
`core/internal/cutover.Reconciler` (a sibling of the trigger/homegate reconcilers), on a cadence:
1. reads the descriptors (collected by strattd from each registered plugin's manifest at Register);
2. lists desired-state Workflows and keeps those with `adoptedFrom`;
3. for each, if `adoptedFrom.kind` matches a descriptor's `target_kind`, resolves the target entity and reads
   its **inverse** relations (`RelationSources(target, descriptor.relation)`) — the foreign executors;
4. for each executor, reads `descriptor.liveness_namespace` facet and compares the value at
   `liveness_path` to `liveness_value`; if it matches ("still live"), opens a Finding via
   `WriteGovernanceFinding(baseline, target, "warning", "ansible-cutover", detail)`;
5. a `ResolveCleared…` pass closes Findings whose condition no longer holds (executor disabled or the
   adoption reverted) — the finding-GC pattern.
The reconciler switches on NO tool name; every ansible-specific string comes from the descriptor.

### 4. Diagnosability (MF-4, §1.8)
The check reuses the standard Finding surface (no new severity/kind). Two false-fire axes are documented on
the Finding and in the package: the **projection/Syncer lag** axis (as ADR-0085 §4 — a not-yet-re-synced
schedule) and the **new Git desired-state read-lag** axis (a just-merged or just-removed `adoptedFrom`
Workflow can momentarily mis-fire the join). The Finding `detail` **descends to both ends** — the
`adoptedFrom` Workflow (the Git side) and the live foreign `ansible.schedule` (the projection side) — so an
operator sees exactly what to disable and why. Flap-damping applies (the Finding opens on a sustained, not a
transient, condition).

## Charter alignment
- **§1.2:** reads only (desired state + projection); Findings are governance output; the projection graph is
  never written; "adopted" is never materialized. Passes the rebuild test.
- **§1.4:** the reconciler is tool-blind; the ansible specifics are a Connector-declared descriptor.
- **§1.8:** explicit, damped, two-axis-documented, descends to both ends.
- **§2.4:** a pure predicate (adopted AND foreign-live) — no precedence, merge, or winner.
- **§9:** the liveness predicate is a single `{path, value}` equality — deliberately NOT a query grammar
  (the ADR-0085 DSL watchline; any regex/multi-condition growth returns to review).

## Consequences
- **Positive:** the strangler cutover is continuously enforced — an adopted object left running in AWX is a
  standing Finding, not a silent dual truth; the mechanism generalizes to any Connector that declares a
  descriptor (not just AWX); no projection write, no engine change, no new Named Kind.
- **Negative / trade-offs:** the join is O(adopted Workflows × their executors) per sweep (bounded, cadence-
  paced); the two read-lag axes can transiently mis-fire (damped); the descriptor is a new, if small,
  manifest surface plugins opt into.

## Alternatives considered
- **Arch 2 (write `adopted` into the projection)** — rejected (§1.2 second truth; fails rebuild; reopens
  ADR-0086 §2; its cross-edge target-facet predicate crosses the §9 no-language line).
- **A new "CutoverPolicy" CaC kind** — rejected (frozen vocabulary; the descriptor belongs on the Connector,
  scoping belongs to View+Assignment).
- **Fold into ADR-0086** — rejected: this corrects ADR-0086 §4's mechanism and adds a manifest surface + a
  reconciler — a decision of consequence, its own record (mirrors ADR-0085 MF-2's reasoning).
