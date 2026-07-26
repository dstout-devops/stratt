# ADR 0129 — A mirrored workflow says what it invokes; it does not re-model AWX's node graph

- **Status:** **Accepted** (2026-07-26, steward) and **implemented** — schema pinned, edges projected,
  Baseline shipped, `task ci` green. Charter review by hand (this session's rules bar the
  subagent); §1.1/§1.2/§1.8/§9 answered inline. **No new dependency.** No new core-model identifier — one
  new relation type (`invokes`) and facet fields inside an existing tool-namespaced Facet.
- **Date:** 2026-07-26
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams), §1.2 (projections, never a second truth), §1.8 (never hide
  diagnosis), §9 (no ontology creep)
- **Reconciles with:** ADR-0025 (the AWX projection), ADR-0047 §1 (relations target by identity; no
  vivify), ADR-0085 (relation-presence Baselines), ADR-0089 (the transform is plugin breadth — it, not the
  mirror, owns cutover fidelity), ADR-0127 (one plugin, two Sources), ADR-0128 (the sibling decision for
  `ansible.template`; this follows its D2 rule and its D4 line)
- **Closes:** `AWX-002` in [docs/parity/awx-object-model.md](../parity/awx-object-model.md)

## Context

`ansible.workflow` is a name, a description, and an `owned-by` edge. The audit's phrasing: the graph knows
a workflow **exists** and knows nothing about what it **does**, so the Entity cannot be reasoned about,
related to the templates it invokes, or governed by a Baseline that means anything.

The data is not missing — it is **already fetched and parsed, twice over, by code in the same module**.
The adopt deep-read pulls `/workflow_job_templates/{id}/workflow_nodes/` and
[materialize/workflows.go](../../plugins/ansible-automation/controller/materialize/workflows.go) turns the
node graph into a real Stratt Workflow DAG, gates and all. The projection simply never emitted any of it.
That is the read-path asymmetry the audit named, in its purest form: the transform sees the topology, the
mirror does not, and nothing recorded that as a decision.

## Decision

### D1 — Project what a workflow **invokes**, as edges

`ansible.workflow --invokes--> ansible.template` (and `--> ansible.workflow` for a nested workflow), one
edge per **distinct** target, derived from the node graph. This follows ADR-0128 D2's rule without
restating the argument: a question about what runs what is a **traversal**, so "which workflows invoke this
template?" — blast radius, the reverse direction — falls out for free.

Node type decides the target scheme, on an **explicit** switch:

| `summary_fields.unified_job_template.unified_job_type`                               | Projected as         |
| ------------------------------------------------------------------------------------ | -------------------- |
| `job`                                                                                | → `ansible.template` |
| `workflow_job`                                                                       | → `ansible.workflow` |
| `workflow_approval`                                                                  | no edge — see D2     |
| anything else (`project_update`, `inventory_update`, `system_job`, or a future type) | **skipped**          |

Skipping the default is deliberate and is the §1.8 call in this ADR. A workflow node can genuinely target
a project sync or an inventory update — objects we do not project — so those have no edge to draw. The
alternative, treating any node with a non-zero `unified_job_template` as a template, would draw a
**confidently wrong** edge onto an identity that may exist and mean something else. A missing edge is
visible (the relation-presence Baseline below reads exactly that); a wrong edge is not.

### D2 — Approval gates are a **fact on the workflow**, not an entity

An approval node has no target — it is a pause. So it is projected as `hasApprovalGate` on the facet,
beside `nodeCount`. `ansible.workflow` gains a pinned, closed schema alongside them.

**On the §1.1 demander, stated precisely rather than overclaimed.** The demander is the **projection
Contract itself** — the manifest's `ContractDecl` for this namespace — exactly as
[`ansible.playbook`](../../contracts/facets/ansible.playbook.schema.json) is pinned today, whose own
description says it: _"a relation-presence check reads no content; a writer demands the schema."_ The
Baseline in D4 is a relation-presence check and therefore demands **nothing**; saying otherwise would
repeat the mistake ADR-0128 diagnosed, one level up. The schema is here to harden the write seam, and that
is a sufficient reason on its own.

### D3 — The node graph is NOT projected as entities, and cutover fidelity is why it does not need to be

No `ansible.workflownode` namespace. Nodes are internal structure — ordering, success/failure/always
branching, per-node timeouts — and projecting them would spend a namespace, an identity scheme and a
tombstone scheme on plumbing nothing currently queries (§9: a namespace needs a demander, not an
intuition).

The reason that is safe rather than lossy is the same line ADR-0128 D4 drew for `ask_*_on_launch`:
**fidelity belongs to the adopt path, not the mirror.** `stratt adopt` reads the node graph from AWX
directly, at adopt time, and builds the DAG with its edges and gates — it has never consulted the
projection and does not need to. The mirror exists to answer governance questions about an estate that is
still running on AWX. Those questions are "what does this invoke", "what invokes this", and "is there an
approval in here" — all three answered above.

**If a consumer appears that genuinely needs the DAG** — the obvious one is rendering a mirrored workflow
in the UI beside a Stratt Workflow — that is when the namespace earns its place, and it is booked as
**AWX-016** rather than pre-built.

### D4 — Ship the governance surface: `awx-workflow-covered`

A relation-presence Baseline (ADR-0085) over `awx-workflows` requiring an `invokes` edge. A mirrored
workflow that invokes nothing is either dead automation or a workflow whose targets AWX has since deleted —
and the second case matters, because an edge onto a deleted template **drops** rather than vivifies
(ADR-0047 §1), so the absence is the signal. The exact shape of `awx-template-covered`, one level up.

Its false-positive mode is the same and is documented on the declaration: a workflow whose targets have
not yet been enumerated this cycle looks uncovered, so it is damped.

## Charter alignment

- **§1.1.** The schema hardens the write seam and its demander is named honestly (the projection Contract),
  not attributed to a Baseline that reads no content.
- **§1.2.** Read-only; AAP stays authoritative. The edges are derived from what AWX reports, per sync.
- **§1.8.** An unknown node type draws **no** edge rather than a plausible wrong one, and the missing edge
  is exactly what the Baseline surfaces.
- **§9.** No new namespace: one relation type and two facet fields on an existing one.

## Consequences

- **Positive.** `ansible.workflow` becomes a governable Entity. Blast radius ("what invokes this template?")
  is a reverse traversal, matching the credential question ADR-0128 D2 opened. The read-path asymmetry the
  audit found is closed for its last Tier-1 case.
- **Negative — an N+1 read, and this is the real cost.** AWX has no bulk endpoint for workflow nodes, so a
  full sync now issues **one request per workflow** on top of the six collection reads. The adopt path
  already does this, so the shape is not new, but the projection runs it **every poll interval** rather
  than once per adopt. Against a Controller we do not own (PLG-1's external-first rule) that is more
  surface to be slow, rate-limited, or RBAC-refused — and because `Enumerate` fails the whole Observe on
  any one error by design (§1.8: an empty projection must never be presented as a successful full sync), a
  single workflow's node fetch failing now costs the **entire** cycle, not just that workflow's edges.
  Accepted as-is rather than special-cased: partial-success semantics for a full sync is a spine decision
  that would have to hold for every Syncer, and inventing it inside one plugin is precisely the kind of
  local exception §1.4 exists to prevent. It is called out here so the next person meets it as a documented
  trade rather than a mystery.
- **Follow-up booked:** **AWX-016** — the full node graph as entities, if and when a consumer (a UI DAG for
  mirrored workflows) demands it.

## Alternatives considered

- **Project the node graph as `ansible.workflownode` entities.** The faithful option, and rejected in D3:
  no consumer demands it, adopt already owns fidelity, and it spends a namespace plus a tombstone scheme on
  internal plumbing. Booked as AWX-016 so the decision is revisitable rather than closed.
- **Store the DAG as a JSON blob on the workflow facet.** Cheapest, and rejected for the reason ADR-0128 D2
  rejected an array of credential names: it makes a graph question a parse. A blob in a Facet is also
  exactly the shape §9 warns about — an ontology smuggled in as one field.
- **Emit an edge for every node target regardless of type.** Rejected in D1: it draws confidently wrong
  edges for project/inventory-update nodes. A missing edge is diagnosable; a wrong one is worse than
  nothing.
- **Do nothing; adopt already reads the topology.** True, and the reason this was never noticed. Rejected
  because it confuses two jobs: adopt answers "help me migrate this workflow", the mirror answers "what is
  running in my estate right now" — and until now the second had no answer at all.
