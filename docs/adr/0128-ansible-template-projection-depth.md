# ADR 0128 — The `ansible.template` mirror answers governance questions, or it is decoration

- **Status:** **Accepted** (2026-07-26, steward) and **implemented** — schemas pinned, projection
  deepened, estate consumer shipped, `task ci` green. Charter review by hand (this session's rules bar the
  subagent); §1.1/§1.2/§2/§2.5/§3/§9 answered inline. **No new dependency.** Vocabulary reviewed by hand
  against §2's banned-identifier list — see D2, which is the close call.
- **Date:** 2026-07-26
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams — a Facet schema is demanded by a shipping Contract), §1.2
  (projections, never a second truth), §1.8 (never hide diagnosis), §2 (frozen vocabulary), §2.5 (secrets
  are brokered, never projected), §3 (never replicate AWX's job-events pathology), §9 (no ontology creep)
- **Reconciles with:** ADR-0025 (the AWX projection), ADR-0033 (facet-observation Baselines), ADR-0042
  (per-source liveness + tombstone), ADR-0060 (multi-source Facet ownership), ADR-0085 (the
  relation-presence Baseline), ADR-0089 (the transform is plugin breadth), ADR-0127 (one plugin, two
  Sources)
- **Closes:** `AWX-010` and, for this namespace, `AWX-014` in
  [docs/parity/awx-object-model.md](../parity/awx-object-model.md)

## Context

[The AWX object-model audit](../parity/awx-object-model.md) went a level below the component-level parity
doc and found the projection is a spine, not a mirror: `ansible.template` carries **five** of a job
template's ~50 fields — `name`, `jobType`, `playbook`, `surveyEnabled`, `description`. Three questions an
operator brings to an AWX mirror on day one are therefore unanswerable from the graph:

1. **"Which templates use this credential?"** — the single most-asked AWX audit question, and the one that
   matters most when a credential is rotated or compromised. `summary_fields.credentials` is read by the
   adopt deep-read and dropped by the projection.
2. **"What automation is failing?"** — `status` / `last_job_failed` are not read at all, so the mirror
   knows what automation _exists_ and nothing about whether it _works_.
3. **"What will this actually do?"** — `forks`, `limit`, `job_tags`, `timeout`, `become_enabled` and the
   rest are absent, so the mirror is **thinner than our own execution Contract**: `ansible.input.v6` models
   every one of these as a typed field, and the projection of the system we are replacing models none.

### The thin-projection trap, which is the part worth naming

`ansible.template` also has **no pinned Facet schema**, and that is not an oversight — it is a consequence.
§1.1 says a Facet schema is demanded by a shipping Contract, and the one governance consumer over
templates, ADR-0085's `awx-template-covered`, is a **relation-presence** Baseline: it asserts graph
_topology_ (every template has a `runs` edge), reads no facet content, and therefore demands no schema.
`awx-schedule-enabled` demands `ansible.schedule` precisely because it reads `enabled`.

So the loop is: the projection was too thin to be worth consuming → nothing consumed it → nothing demanded
a schema → the namespace writes unvalidated. Deepening the projection and shipping its consumer is one
move, not two, and doing only the first would leave the same hole one field wider.

## Decision

### D1 — Project the three field groups that answer the three questions, and pin the schema

`ansible.template` gains, beside the five it already carries:

| Group           | Fields                                                                                                    | Answers                     |
| --------------- | --------------------------------------------------------------------------------------------------------- | --------------------------- |
| **Run state**   | `lastRunStatus`, `lastRunFailed`, `lastRunAt`, `nextRunAt`                                                | "what is failing?"          |
| **Run knobs**   | `forks`, `limit`, `jobTags`, `skipTags`, `timeout`, `verbosity`, `diffMode`, `becomeEnabled`, `scmBranch` | "what will it do?"          |
| **Credentials** | _not a field_ — see D2                                                                                    | "who uses this credential?" |

The schema is **closed** (`additionalProperties: false`, §9) and pinned like every other Facet contract.
Field names are lowerCamel Stratt renderings of AWX's snake_case attributes, exactly as the existing
`jobType`/`surveyEnabled` already are — the §2 vendor-rendering latitude applies inside a tool-namespaced
Facet and stops at the core model.

### D2 — Credential usage is a **Relation** onto an observed `ansible.credential`, not an array of names

"Which templates use this credential" is a graph traversal, and the estate graph being able to answer it
_by traversal_ is the whole differentiator (charter §7.1). An array of credential names on the template
facet would answer it by scan, could not carry the credential's kind, and would give the credential no
identity to hang anything else off. So the controller half gains a **sixth** owned namespace,
`ansible.credential` (name + kind), and emits `ansible.template --uses-credential--> ansible.credential`.

**This is the ADR's close call, on three axes, and each is answered rather than waved past:**

- **§9, ontology creep.** A new namespace needs a demander, not an intuition. It has one: D5 ships the View
  and the credential question is the reason this ADR exists. The namespace mirrors an object AWX genuinely
  has, in the same shape `ansible.template` mirrors a job template — it invents nothing.
- **§2, vocabulary.** `credential` is not on the banned list (`inventory`, `playbook`, `job template`,
  `CI`, `CMDB`, `resource`), and **`ansible.credential` is not `CredentialRef`**. The relationship is
  exactly `ansible.template` ↔ Workflow: one is the read-only mirror of a foreign object, the other is the
  frozen Named Kind it becomes when `stratt adopt` takes authority. The `ansible.` prefix quarantines it,
  as it already does for the two §2-banned words `ansible.playbook` and `ansible.inventory`.
- **§2.5, secrets.** Name and kind only. AWX returns `$encrypted$` placeholders for material and we do not
  read even those — the same line [materialize/credentials.go](../../plugins/ansible-automation/controller/materialize/credentials.go)
  already holds for the transform. **No credential material is projected, ever**, and the closed schema is
  what makes that checkable rather than promised.

### D3 — Last-run state is projected; it is **not** job history

Charter §3 forbids replicating AWX's job-events-table pathology, and this is deliberately not that: four
scalar fields of _current_ state on the template, not a row per event. The precedent is
[`instance.state`](../../contracts/facets/instance.state.schema.json) — the awsec2 Syncer projects a cloud
instance's lifecycle state read-only and the next poll reflects the transition. Same shape, same §1.2
posture: AWX stays authoritative, the graph is the rebuildable read-model.

**On churn, since a run status changes far more often than a name:** it costs nothing new. Every Observe is
already a full sync that rewrites every projected facet each cycle (ADR-0042), so the volatile fields ride
a write that was happening anyway. That is also the argument against splitting state into its own
namespace — a split would buy a smaller write on a path where nothing is written smaller.

### D4 — What this does NOT project, each for a stated reason

- **`extra_vars`** — may carry secret material, and a projection is exactly where §2.5 says it must not go.
  Refused, not deferred. (The same reflex ADR-0118 D2 acted on when it made an imported survey **password**
  question a blocking re-broker rather than an import.)
- **The ~15 `ask_*_on_launch` booleans** — real, and cutover-fidelity rather than governance: they
  determine what a launch may override, which matters when migrating a template, not when auditing an
  estate. Deferred with its own ID rather than bulking the schema now.
- **`execution_environment`, `instance_groups`, `labels`** — already carry `AWX-007`, `AWX-008`, `AWX-006`,
  and each is a genuine mapping question (an EE is an Actuator declaration on our side, ADR-0117 D3a; an
  instance group maps to Sites/Cells). None is a field to copy.
- **Workflow topology (`AWX-002`)** — the audit's other Tier-1 item, and a separate decision: it is a graph
  of nodes and edges, not fields on an object.

### D5 — Ship the consumer, because §1.1 means a schema is demanded, not declared

Two estate declarations land with the projection:

- **`awx-template-failing`** — a facet-observation Baseline (ADR-0033) over `awx-templates`, asserting
  `lastRunFailed == false`, `@every 5m`, warning, damped. The direct analogue of `awx-schedule-enabled`,
  and the §1.1 consumer that makes the run-state fields demanded rather than free-floating.
- **`awx-credentials`** — a View over `ansible.credential`, so the credential-usage question is a first-class
  estate surface rather than an ad-hoc query.

Both are pure reads: Findings, no actuator, no write-back, no claim (§2.4).

## Charter alignment

- **§1.1.** The schema ships **with** its consumer, closing the thin-projection loop rather than pinning a
  schema nothing reads.
- **§1.2.** Read-only throughout; AWX stays the system of record and keeps executing. Run state is a
  projection of AWX's own truth, never a second one.
- **§2 / §9.** One new tool-namespaced namespace with a named demander; no Named Kind added; no core-model
  identifier touched. `ansible.credential` is the mirror, `CredentialRef` is the Named Kind.
- **§2.5.** Credential **name and kind only**; material is never read, and the closed schema makes that a
  structural property.
- **§3.** Current state, not an event table.

## Consequences

- **Positive.** The mirror answers the three questions operators actually bring it. `ansible.template` gets
  a pinned, closed, validated schema — closing `AWX-014` for the namespace that most needed it. Credential
  blast-radius becomes a graph traversal, which is the estate-graph thesis demonstrated on the system we
  are replacing rather than asserted.
- **Negative / trade-offs.** The controller half now owns **six** namespaces and six tombstone schemes, so
  its grant and manifest both widen (and `plugins/ansible-automation/automation_test.go` asserts the count,
  so the split stays honest). A sixth projected kind is a sixth thing a full sync enumerates each cycle —
  one more collection read per poll against a Controller we do not own (PLG-1's external-first rule
  applies: it is one more thing that can be slow, rate-limited, or RBAC-refused, and `Enumerate` fails the
  whole Observe on any one collection's error by design, §1.8).
- **The run-state fields make the graph's freshness visible in a way it was not.** A `lastRunAt` that
  trails the poll interval is the mirror honestly showing its own lag — good (§1.8), and worth saying out
  loud because it will look like a bug the first time someone sees it.
- **Follow-ups booked, not absorbed:** `ask_*_on_launch` for cutover fidelity (**AWX-015**, new);
  workflow topology (**AWX-002**); the authorization slice (**AWX-003/4/5**), which still needs its
  own decision before any code, because AWX RBAC facts must not become a second authorization truth beside
  OpenFGA.

## Alternatives considered

- **Credentials as a facet array of names.** Cheaper and it needs no new namespace, so it is the tempting
  one. Rejected in D2: it answers the question by scan rather than traversal, drops the credential's kind,
  and leaves nothing to attach the next credential fact to. The estate graph is the differentiator; this is
  precisely where to use it.
- **A separate volatile namespace for run state** (`ansible.template.state` or similar). Rejected in D3:
  the full sync already rewrites every facet each cycle, so the split optimises a write nobody makes, and
  it spends a namespace to do it.
- **Project all ~50 fields.** Rejected: `extra_vars` alone makes it a §2.5 violation, and a schema that
  mirrors a vendor's whole object is an ontology (§9) rather than a typed seam. The three groups are chosen
  because three questions demanded them.
- **Deepen nothing; document the gap and move on.** Defensible for one release — the audit already records
  it honestly, which is worth something. Rejected because the gap compounds: every governance surface built
  over the AWX mirror in the meantime is built over five fields, and the missing schema means none of them
  are validated.
