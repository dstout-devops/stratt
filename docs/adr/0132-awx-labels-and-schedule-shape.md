# ADR 0132 — Two "mechanical" projection gaps that were not: AWX labels, and what a schedule actually runs

- **Status:** **Accepted** (2026-07-26, steward) and **implemented** — labels + schedule shape projected,
  `ViewSelector.Relations` exposed through CaC, `task ci` green. Charter review by hand (this session's rules bar the
  subagent); §1.1/§1.2/§2.5/§9 answered inline. **No new dependency.**
- **Date:** 2026-07-26
- **Deciders:** steward
- **Charter sections:** §1.2 (projections), §2.5 (secrets are brokered, never projected), §9 (no ontology
  creep), §1.5 (pinned schemas)
- **Reconciles with:** ADR-0041 (per-key label ownership — the constraint that decides D1), ADR-0059
  decision 6 (`ViewSelector.Relations` — topology selection, the mechanism D1 uses), ADR-0084/0117 D5a
  (`mgmt.address` widened in place — the precedent for D3), ADR-0128 (D2's traversal-not-scan rule and D4's
  refusal of `extra_vars`), ADR-0131 (the poll-cost budget these must not blow)
- **Closes:** `AWX-006` and `AWX-013` in [docs/parity/awx-object-model.md](../parity/awx-object-model.md)

## Context

Both of these sat in the audit's Tier 3 as cheap mechanical copies. Neither is, and the traps are the
reason they are worth an ADR rather than a commit message.

**Labels look like labels.** AWX labels are the operator's own grouping vocabulary — `prod`, `critical`,
`team-web` — attached to job templates and workflows. The graph has labels. The obvious move is
`ansible.label.prod = "true"`.

**It cannot work, structurally.** A plugin's label keys are a **static allowlist on the grant**
(`Grant.LabelKeys`), registered per key at `Register` and enforced per key at the write path — because
ADR-0041 makes each key single-owner. A label key that is only known once you have read the Controller
cannot be in a grant declared at boot. The naive mapping is not merely inelegant; it is ungrantable.

**`extra_data` looks like a field.** The audit's complaint is real: two schedules of the same template with
different `extra_data` are indistinguishable in the graph today. But `extra_data` is a variable map that
can carry secret material, and ADR-0128 D4 **refused** `extra_vars` on the template for exactly that
reason. Projecting it here would walk around that decision through a side door.

## Decision

### D1 — An AWX label is an **Entity**, and membership is an edge

`ansible.label` (name + organization) as an eighth owned namespace, with
`ansible.template --has-label--> ansible.label` and the same from `ansible.workflow`.

This is ADR-0128 D2's rule applied a third time — a grouping question is a traversal — and it lands on a
selector that already exists: `ViewSelector.Relations` (ADR-0059 decision 6) selects entities **by
topology**, so "every production job template" is a View, not a scan:

```yaml
selector:
  kinds: [ansible.template]
  relations:
    - type: has-label
      targetKind: ansible.label
      targetLabels: { ansible.name: prod }
```

That is the payoff worth stating plainly: **an operator's AWX grouping vocabulary becomes Stratt Views**,
which is the migration story in miniature — the same grouping, expressed in the target system's own
primitive, before anything has been cut over.

**Found while shipping that View: `ViewSelector.Relations` was unreachable from Config-as-Code.**
`types.ViewSelector` has carried topology selection since ADR-0059, and the declaration decoder
(`declSelector`) exposed only `kinds`, `labels` and `facets` — so a declared View could never select by an
edge. The capability existed and no estate could use it. The decoder now decodes `relations` (a predicate
with no `type` is rejected, not silently ignored), which is a small fix to a gap that had nothing to do
with AWX and would have kept hiding until something needed it.

**Free of the poll-cost budget (ADR-0131).** Label _membership_ rides `summary_fields.labels` on objects
already being read, so the associations cost **zero** extra requests. Only the label objects themselves add
one collection read. This is deliberately a **collection** and not a detail-tier read: it is O(1) per poll,
not O(objects).

### D2 — `has-label` is emitted only for labels the Controller reported as objects

An edge whose target is not in the projected label set is dropped by the host (no vivify, ADR-0047 §1). No
special handling: same-source, so in practice it always resolves, and if AWX ever reports a label
association without the label, the edge going missing is the honest outcome rather than a vivified stub.

### D3 — A schedule says **when**, in which timezone, and **what shape** of override — never the values

`ansible.schedule` widens with `timezone`, `nextRunAt`, `dtStart`, `until`, the per-schedule launch
overrides (`limit`, `jobTags`, `skipTags`, `jobType`, `verbosity`, `diffMode`, `forks`, `timeout`,
`scmBranch`), and — the interesting one — **`extraDataKeys`**: the KEY NAMES of `extra_data`, never the
values.

That resolves the §2.5 tension rather than dodging it. Two schedules of one template are distinguishable
(different keys, or the same keys is itself informative), the operator can see _what a schedule
parameterises_, and no value ever reaches the graph. It also surfaces something worth seeing: a schedule
with a key like `db_password` is injecting secret-shaped material as a launch variable, which should be a
CredentialRef — the projection makes that visible without carrying the secret.

`rrule` without `timezone` was under-determined, which is the plainest of the gaps: the mirror recorded a
recurrence rule and not the clock it runs on.

### D4 — The schema widens **in place**; it is not a v2

`ansible.schedule` is a **Facet on a rebuildable projection**, not a wire Contract with external authors.
Adding optional properties to a closed schema is backward-compatible widening: previously-valid documents
stay valid, and the graph is a read-model that rebuilds from the Source anyway (§1.2). The precedent is
`mgmt.address`, widened in place with `port` by ADR-0117 D5a against ADR-0084's closed schema.

This is **not** the rule for an Actuator input Contract: `ansible.input` versions as sibling files
(v1…v6) because a pinned input Contract is a wire promise to Step authors, and editing it in place would
change what an existing declaration means. The distinction is who reads the schema — a projection's reader
is the platform; an input Contract's reader is an operator's committed YAML.

## Charter alignment

- **§1.2.** Read-only; AAP remains the SoR. `extraDataKeys` is a projection of shape, not of content.
- **§2.5.** No credential material and now, explicitly, no launch-variable **values** either — the
  ADR-0128 D4 line held rather than being routed around.
- **§9.** One new namespace with a named consumer (the View pattern in D1), and a widened closed schema.
- **§1.5.** The pin changes with the file, which is what pinning is for; D4 states when in-place widening
  is legitimate so the next person does not have to guess.

## Consequences

- **Positive — and wider than AWX.** Exposing `relations` through the CaC decoder unblocks topology
  selection for **every** declared View, not just this one: "the hosts in the DMZ" (ADR-0059's own
  motivating example) was expressible in the type and not in the estate. An AWX estate's grouping
  vocabulary becomes queryable — and reusable as Views — without a
  scan. A schedule stops being an under-determined `rrule`. Both at a cost of one extra collection read per
  poll, inside the ADR-0131 budget.
- **The label namespace is operator-populated and unbounded in cardinality.** A Controller with thousands
  of labels projects thousands of entities. That is honest (they exist) and it is the first `ansible.*`
  namespace whose size is driven by operator behaviour rather than automation count — worth knowing before
  someone points it at a Controller with a label per deploy.
- **`extraDataKeys` is a partial answer by construction.** Two schedules with identical keys and different
  values still look identical. That is the §2.5 price and it is the right one; an operator who needs the
  values reads them at AWX, where they are governed by AWX's own access control.
- **`ansible.schedule`'s pin hash changes**, so a deployment mid-upgrade sees the boot-time contract check
  compare a new hash — the intended behaviour of a pinned-schema system, and harmless because the facet is
  a rebuildable projection.

## Alternatives considered

- **AWX label → graph label key** (`ansible.label.prod = "true"`). Rejected in D1: label keys are a static
  grant allowlist enforced per key (ADR-0041), so a key discovered at read time is ungrantable. Even with
  dynamic grants it would mint an unbounded key namespace, each needing single-owner registration.
- **AWX labels as an array field on the template facet.** Answers by scan, carries no organization, and
  gives a label no identity — the same three objections ADR-0128 D2 raised against an array of credential
  names, for the same reasons.
- **Project `extra_data` values.** Rejected in D3 on §2.5, and it would have quietly reversed ADR-0128 D4
  for the same class of data one object over.
- **Version `ansible.schedule` as a sibling v2.** Rejected in D4: it is a projection facet, not a wire
  contract, and `mgmt.address` already set the in-place precedent. Versioning it would strand v1 readers
  that do not exist.
