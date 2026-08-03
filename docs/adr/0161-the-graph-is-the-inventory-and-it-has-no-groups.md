# ADR 0161 — The graph is the inventory, and it renders no groups

- **Status:** **Proposed** (2026-08-03, steward). Charter review by hand — this session's rules bar
  the subagent; §1.1/§1.2/§1.4/§2.1/§9 answered inline. **No new runtime dependency.**
- **Date:** 2026-08-03
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams, not the world), §1.2 (projections, never a second
  truth), §1.4 (boring spine — core authors no tool vocabulary), §2.1 (Views), §9 (no ontology creep)
- **Extends ADR-0084/0156** (the core sends typed coordinates, the shim renders ansible keys) with
  the same split applied to GROUP MEMBERSHIP. Builds on **ADR-0059** (relation-aware View
  selection), **ADR-0041/0042** (per-key Facet ownership), **ADR-0024** (parametrized Views),
  **ADR-0134** (an Actuator's content root is one project, `group_vars/` included).

## Context

Dynamic and composed inventories are the best thing about Ansible's operational model, and the
audit that produced ADR-0160 kept running into them. This ADR is the inventory half.

### Where Stratt is already ahead, and it is most of it

An AWX **inventory** is a container that several **sources** sync into on a schedule, merging by
`overwrite`/`overwrite_vars` flags — last sync wins, per inventory, with the same host merged
separately in every inventory that lists it.

Stratt has no inventory container. **Sources project into ONE graph**, continuously, and a conflict
between two writers is resolved STRUCTURALLY by per-key Facet ownership (ADR-0041/0042) rather than
by whoever synced last. A host observed by vCenter and by NetBox is one Entity with per-key
provenance, not two rows in two inventories. That is a straight improvement and it needs no work.

A **smart inventory** (a saved host filter) and a **constructed inventory**'s selection half are a
Stratt **View**: saved, versioned, CaC-declared, and matching on `Kinds`, `Labels`, **`Facets`**
(namespace + JSON path) and **`Relations`** (an outgoing typed edge to a target — "the hosts in the
DMZ", ADR-0059). The Relations clause has no AWX equivalent at all: AWX cannot select by topology
because it has no topology.

So: dynamic inventory ≈ Syncers, smart/constructed selection ≈ Views, static inventory ≈ the
`declared` plugin. **The composition story is already better.** What is missing is downstream of all
of it.

### The finding: the rendered inventory has exactly one group

`buildInventory` writes `[all]`, one line per host, and `[all:vars]`. **That is the whole file.**
Stratt has never rendered an ansible group.

The consequences are not subtle:

- A play that says `hosts: webservers` matches **nothing**. Every migrated playbook targeting a group
  must be rewritten to `hosts: all`, with the selection moved into the View or `params.limit`.
- `group_vars/webservers.yml` in an Actuator's content root is **never loaded**, because ansible
  resolves group_vars by group name against the inventory's groups, and there are none. ADR-0134 D2
  explicitly declares `group_vars/` part of a project, and nothing can use it.
- `keyed_groups` — one group per distinct value of a host attribute, the single most useful thing in
  a dynamic inventory — has no expression at all. The nearest thing is declaring one View per value,
  which requires knowing the values in advance and is exactly what keyed_groups exists to avoid.

Measured against ADR-0160's standard — *can the operator do the task, with the same permissions and
ownership* — this fails. "Run the fleet's web tier through the play my org already has" is a task an
AAP operator does daily and a Stratt operator cannot do without rewriting the play.

## Decision

### D1 — Group membership is TYPED DATA the core resolves; the INI is the shim's to render

The same split ADR-0084 drew for the address and ADR-0156 drew for the transport, applied here: the
core sends each Target the **groups it belongs to** as a list of names; the shim renders `[name]`
sections. The core authors no INI, and the shim learns nothing about where a group came from.

This is why it is not a new inventory concept: a group name is a string the core already has, and
`ApplyTarget` gaining `groups` is the same shape as it gaining `transport`.

### D2 — Groups are DERIVED from declared keys, never separately declared

A Step (or its Actuator) declares how to group, and the grouping is data, not code:

```yaml
groupBy:
  - label: role # every distinct value of the `role` label → one group
  - facet: { namespace: cloud.placement, path: region }
    prefix: region # → region_eu-west-1, region_us-east-1
```

**This is `keyed_groups`, and it arrives without a new configuration language** (§1 non-goal): a
label key or a Facet namespace+path is the same structured predicate data a View selector already
uses (ADR-0024's rule — explicit field lookup, no operators, no evaluation). One group per distinct
value falls out of the key, so values nobody enumerated still produce groups.

**Derived, never a second declaration.** A hand-written group membership list would be a second home
for a fact the labels and Facets already carry, and the two would drift (§2.4). Membership is a
function of the graph, recomputed per Run, and a Run records the groups it rendered (§1.8: the
inventory is already emitted as a `kind=inventory` event, so this is visible in descent for free).

### D3 — The View remains the blast radius and the authorization unit; groups are WITHIN it

Grouping never widens a Run. The View selects the target set and is the §2.5/ADR-0028 authorization
unit; `groupBy` only partitions what is already selected, and a play naming a group reaches a subset,
never a superset.

This is the load-bearing safety property, and it is why groups are a RENDERING concern rather than a
selection one: if a group could pull in a host the View did not select, the authz unit would be the
play's `hosts:` line, which is content — and content deciding blast radius is precisely what
ADR-0028 refuses.

### D4 — `group_vars/` then works with no new mechanism, and that is the tell

Once `[webservers]` is rendered, ansible resolves `group_vars/webservers.yml` from the project root
by its own rules. ADR-0134 D2 already declares `group_vars/` part of an Actuator's content root; it
has simply been unreachable. Nothing new is built here — a promise already made starts being kept.

### D5 — `compose` is REFUSED, and the refusal is the §1.2 line

Ansible's `compose:` derives new host variables from Jinja over existing ones. Stratt does not, and
this is not a deferral:

A derived variable is a **computed fact about a host**, and §1.2 admits exactly two writers of host
attributes — Normalizers and Run provenance. A third path that invents attributes at inventory-render
time would be a second truth with no provenance, unrebuildable from Sources, and invisible to drift.

The task remains doable, in two places that already exist and both carry provenance: a **Normalizer**
computes it into a Facet (owned, stamped, visible in the graph), or the **play** computes it in
`set_fact` where it is ansible's own business. What is refused is the middle: a value that looks like
an observation, is not one, and nothing owns.

### D6 — `groups:` (conditional group creation) is deferred, not refused

Ansible's `groups: {name: <jinja condition>}` creates a group when an expression holds. Every version
of it needs an expression evaluator, which is the §1 non-goal. A Facet predicate covers the common
case declaratively (`groupBy` on a key whose values already encode the condition), and the residue
is booked rather than approximated. If it returns, it returns as data.

## Consequences

- **Migrated playbooks run unmodified**, which is the adoption argument this whole family exists for.
  A play targeting `hosts: webservers` works when the Step declares `groupBy: [{label: role}]`.
- **`ApplyTarget` gains a `groups` field** — a port change, additive, and the same shape as
  `transport`. Plugins that ignore it are unaffected.
- **The rendered inventory stops being byte-trivial**, and its byte-stability tests must cover
  deterministic group ordering: two Runs over one target set must produce identical bytes, or the
  §1.8 comparability property the inventory event exists for is lost.
- **`keyed_groups`' generative behaviour arrives without an expression language**, which is the part
  worth checking hardest in review.
- **Nothing about selection changes.** Views, authz and blast radius are untouched; a Run reaches
  exactly the hosts it reached before, arranged differently.

## Verification

- Unit: a target set with two distinct `role` values renders two groups plus `[all]`; a Facet-keyed
  group renders one section per distinct value; group ordering is deterministic across two renders;
  a target with no value for the key lands in `[all]` only and in no invented group.
- Unit: `groupBy` cannot widen a Run — a group name that matches nothing in the View produces an
  empty section, never a lookup back into the graph.
- **Live**: a demo whose play targets a GROUP (`hosts: <group>`) and converges only that subset,
  asserted from the Run's per-target results — plus a `group_vars/<group>.yml` whose value reaches
  the play, because "group_vars now work" is the claim most likely to be true in principle and false
  in the pod.
