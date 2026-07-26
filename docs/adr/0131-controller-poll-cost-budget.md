# ADR 0131 — A poll-cost budget for the AAP Controller half: tiered cadence, and a partial read degrades instead of failing

- **Status:** **Accepted** (2026-07-26, steward) and **implemented** — tiered cadence, degrade-not-fail,
  cost logged; four request-counting tests; `task ci` green. Charter review by hand (this session's rules bar the
  subagent); §1.2/§1.4/§1.8 answered inline. **No new dependency.** No new namespace, identifier, or
  Contract — this is about read cost and failure shape.
- **Date:** 2026-07-26
- **Deciders:** steward
- **Charter sections:** §1.2 (projections — a stale mirror is still a projection, a silently stale one is
  not), §1.4 (boring spine — do not invent local exceptions to spine semantics), §1.8 (never hide
  diagnosis)
- **Reconciles with:** ADR-0042 (per-source liveness; the full-sync tombstone sweep), ADR-0046/0047 (the
  port; `full_sync_complete` is part of it), ADR-0117 D4 / PLG-1 (external-first: the backing system is not
  ours and not assumed reachable), ADR-0129 (**amends** its accepted-as-is consequence — see D2),
  ADR-0130 (the third N+1)
- **Closes:** `AWX-018` in [docs/parity/awx-object-model.md](../parity/awx-object-model.md)

## Context

Three ADRs in a row each added a read to the controller half's full sync, each individually justified, and
**none of them owned the total**:

| Added by | Reads                                                         | Count per poll |
| -------- | ------------------------------------------------------------- | -------------- |
| baseline | job_templates, workflow_job_templates, schedules, orgs, teams | 5              |
| ADR-0128 | credentials                                                   | +1             |
| ADR-0129 | **workflow nodes, one request per workflow**                  | +N             |
| ADR-0130 | users; **team members, one request per team**                 | +1 +M          |

So a Controller with 50 workflows and 20 teams costs **77 requests per 60-second poll** — ~110k/day against
a production system we do not own, that has rate limits, and whose operators did not agree to that traffic.

The second half is worse than the traffic. `Enumerate` fails the **whole** Observe on any one read's error,
by deliberate design (§1.8: an empty projection must never be presented as a successful full sync). That
was a sound rule when the sync was five requests. At 77 it inverts: with a per-request failure probability
of even 0.1%, a full sync succeeds ~93% of the time, and the mirror silently degrades to whatever it last
managed to read. **The projection gets less reliable the richer it gets** — and each ADR that made it
richer said "accepted as-is" about a cost it was measuring alone.

### The mistake in ADR-0129 worth naming

ADR-0129's consequences said partial-success semantics "is a spine decision that would have to hold for
every Syncer, and inventing it inside one plugin is precisely the kind of local exception §1.4 exists to
prevent." The instinct was right and the conclusion was wrong: **the spine already has the primitive.**
`ObserveResponse.full_sync_complete` is part of the port, and `pluginhost.Sync` upserts every streamed
entity _before_ checking it — the flag gates only the tombstone sweep and the per-source edge
delete-and-replace. The empty-snapshot guardrail in this very plugin already uses it. There was nothing to
invent.

## Decision

### D1 — Two tiers, two cadences

The full sync splits by cost:

- **Collections** (the seven list endpoints) — every poll. Cheap, bounded, and they carry the entities.
- **Detail** (per-workflow nodes, per-team members) — refreshed on its own interval,
  `STRATT_ANSIBLE_AUTOMATION_DETAIL_INTERVAL` (default **5m**), and served from a plugin-held cache in
  between.

Steady-state cost drops from `7+N+M` every poll to `7` every poll plus `N+M` every detail interval. At the
defaults (60s poll, 5m detail) that is a **5× reduction** on the expensive half, and — more importantly —
it makes the cost a **declared number an operator can change**, rather than an emergent property of how
many workflows they happen to have.

The cache is a read cache of an external SoR held inside the plugin. It is not a second truth (§1.2): the
plugin still writes nothing, still proposes values, and the core still governs every write.

**Bounded staleness, stated rather than hidden.** Between detail refreshes the plugin re-asserts cached
edges, so a workflow node deleted at AWX keeps its `invokes` edge for up to one detail interval. That is a
declared bound on a read-model, which is what a projection is; the alternative — omitting the edges on
uncached cycles — would make every such cycle retract and re-create real edges, which is worse in every
way.

**First sync always reads detail.** An empty cache is a cache miss, not a reason to project a workflow with
no edges.

### D2 — A failed **detail** read degrades the sync; it does not fail it

When a per-workflow or per-team read errors, the plugin now:

1. keeps the entities and edges it did read,
2. streams them with **`full_sync_complete: false`**,
3. logs the failure with the object it was reading.

The host then upserts everything received and runs **no** sweep — so nothing is retracted on the strength
of a read that did not finish. The next successful cycle reconciles. This is the same mechanism, and the
same reasoning, as the empty-snapshot guardrail one layer up.

A failed **collection** read still fails the whole Observe, unchanged: the collections carry the entities
themselves, so a missing one is not a degraded mirror but a missing third of it, and the existing
retry-next-tick behaviour is correct for that.

This strictly improves on today, where one 404 on the 71st request throws away the six successful
collection reads with it.

### D3 — The cost is observable, because a budget nobody can see is not a budget

Each sync logs the request count it issued and the age of the detail cache. An operator who wants to know
what Stratt is doing to their Controller can read it off the plugin rather than off the Controller's access
log (§1.8).

## Charter alignment

- **§1.2.** Still a projection: nothing is written back, and the read cache is a cache of the SoR, not an
  authority. Staleness is bounded and declared.
- **§1.4.** No spine change and no local exception — D2 uses a port field that already exists for exactly
  this purpose.
- **§1.8.** Partial reads are visible (a declined full-sync boundary, a logged cause) rather than silently
  swallowed or silently fatal; cost is observable.
- **PLG-1 / ADR-0117 D4.** External-first, applied to load rather than reachability: the Controller is
  someone else's production system, so the traffic we generate against it is a design parameter, not a
  side effect.

## Consequences

- **Positive.** Poll cost becomes declared and tunable. A flaky sub-read costs one object's freshness for
  one cycle instead of the entire mirror's. The richer-means-less-reliable curve is flattened: adding an
  eighth collection no longer degrades the probability that anything syncs at all.
- **Negative — edges can be up to one detail interval stale**, so `awx-workflow-covered` and the membership
  picture trail the Controller by that much. This is the same class of lag `awx-template-covered` already
  documents and damps, and it is now larger and configurable. Named on both declarations.
- **A new workflow or team shows up with no edges until the next detail refresh**, which reads as a
  momentary orphan. Damped by the Baselines' existing `dampingObservations`, and worth knowing.
- **The cache is per-process**, so a plugin restart re-reads detail immediately. Correct, and it means a
  crash-looping plugin does not get cheaper — deliberately: that should be visible, not smoothed over.
- **This does not make role grants (`AWX-005`) affordable.** `/roles/` is unbounded and `access_list` is an
  N+1 over _every_ object, which is a different order of cost than per-workflow and per-team. The budget
  makes the question tractable to reason about; it does not answer it.

## Alternatives considered

- **Leave it; the cost is the cost.** What the last three ADRs each did. Rejected: the failure-probability
  argument is the one that changes it from a preference into a defect — a mirror that silently stops
  updating because it grew is exactly the hidden failure §1.8 forbids.
- **A hard request cap with round-robin refresh** (do 10 workflows per cycle, rotate). Bounded cost with
  eventually-consistent edges. Rejected: a cycle that never covers everything can never honestly assert
  `full_sync_complete`, so the tombstone sweep for those schemes would never run and stale edges would
  accumulate permanently. A slower honest cadence beats a faster dishonest one.
- **Make detail reads opt-in and off by default.** Cheap, and it silently removes the capability the last
  two ADRs shipped — an operator gets a mirror that is quietly missing edges rather than a slower one.
- **Change the spine's full-sync semantics to support partial success.** What ADR-0129 assumed was
  required. Unnecessary — see the correction above — and it would have been a large blast radius for
  nothing.
