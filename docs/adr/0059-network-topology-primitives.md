# ADR 0059 — Network & topology primitives: subnets, zones, DNS, and placement as a Relation

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout), charter-guardian
- **Charter sections:** §1.1, §1.2, §1.4, §1.5, §2, §2.1, §2.4, §4.3, §5, §8
- **Frames under:** [ADR-0055](0055-estate-composition.md) (Estate Composition) · [ADR-0058](0058-provisioning-from-intent.md)
  (Provisioning from Intent — the seam this reuses for desired topology)

## Context

ADR-0058 shipped compute provisioning: declare desired infrastructure → gated build → project back. The estate
can now grow hosts. But "three web servers **on network X**, in the **DMZ**, with a **DNS** record" — the shape
of a real onboarding — has no home. There is **no subnet, VLAN, DNS-record, availability-zone, or DMZ**
construct anywhere (verified: the only Entity kinds any plugin projects are `host`, `vm`, `instance`, `cert`,
`device`; the only network data is `instance.network` — a schema-uncovered facet blob carrying `subnetId`/
`vpcId`/`availabilityZone` as opaque strings inside a compute facet).

This is exactly where a platform grows a **universal network ontology** and violates §1.1 ("type the seams, not
the world — never a universal ontology"). The discipline this ADR must hold: model network topology as **the
same cheap, typed seams everything else uses** — projected Entity kinds, named Facets demanded by Contracts,
and typed Relations — never a schema-of-all-networks, and never new spine concepts.

Grounding (what already exists, so this is composition not a rewrite):
- **Entity `kind` is a free string** with no registry (`types/entity.go:13`: "a label for querying, not a
  schema; no ontology hangs off it, §1.1"). New kinds cost **nothing** in core.
- **`types.Relation{Type, FromID, ToID}`** is a bare typed edge; `Type` is a free string (vcenter already emits
  `runs-on`). Placement is just new `Type` values — no edge-schema change.
- **Facets** need ownership registration (a grant's `FacetNamespaces`); a JSON Schema is added **only** when a
  Contract consumes the fields (§1.1) — `instance.network`/`net.guest` are legal "owned-but-uncovered" today.
- **Three genuine gaps** this ADR must budget: Relations **cannot be declared in CaC** (only observed or
  Run-written); View selectors join only kind/label/facet (**no relation-aware selection**); and there is **no
  relation tombstone/GC** (edges to soft-deleted entities dangle).

## Decision

**1. Topology primitives are ordinary projected Entity kinds + owned-but-UNCOVERED Facet namespaces — no
schema ahead of a consumer (§1.1).** `subnet`, `dns-record` (and later `vlan`) are new Entity **kinds** — free
strings, zero core change, projected exactly like `host`. Their attributes live in named Facet **namespaces**
(`net.subnet`, `dns.record`), registered **owned-but-schema-uncovered** — exactly the legal current state of
`instance.network`/`net.guest`. **No JSON Schema hardens in this slice (M1):** this ADR ships no consumer (every
Actuator, the selector, any routing Blueprint is deferred), so per §1.1 / §9 — "a Facet schema exists only when
a shipping Contract demands it" — a `net.subnet {cidr, …}` schema minted now with nothing reading it *is* the
ontology creep this ADR exists to prevent. The schemas land in the Actuator/selector ADRs that demand them, and
each namespace's ownership is assigned to that **consuming plugin's grant** `FacetNamespaces` (§2.1), never
core-in-tree (S4) — so the spine never holds a canonical network schema.

**2. Placement is a typed Relation — the composition backbone (§2.1 relation graph).** "What is *in* what" is a
typed edge, reusing `types.Relation` with new `Type` values: `host --placed-in--> subnet`, `subnet --in-dmz-->
dmz`, `subnet --in-az--> availability-zone`. No edge-schema change. This is authoritative for topology traversal
and one-click descent (Intent → … → the subnet a host sits in). It is written **only** by the two legal §1.2
paths — a Syncer's observation (a cloud connector emits `placed-in` the way vcenter emits `runs-on`) or a build
Run — and this is **enforced in the data layer, not by convention (M3):** the existing `relation_write_path`
trigger already rejects any `graph.relation` write not declaring `stratt.write_path ∈ {normalizer,
run-provenance}` — the edge-level twin of `enforce_write_path`. A build's `placed-in` edge rides the **Actuator's
output projection (Run provenance)** — never a reconcile-side or API write (ADR-0058 M1, applied to edges).
There is no CaC path to a raw graph edge; desired placement lives on the Intent (decision 5).

**3. DMZ, availability-zone, region are each their OWN free-string kind — no generic `zone` discriminator
(M2).** Consistent with decision 1's distinct-kinds rule (and the same reason we reject a `network`
super-Entity), `dmz`, `availability-zone`, and `region` are **separate Entity kinds**, not one generic `zone`
kind tagged by a `zone.kind` enum. A discriminator-Facet on a universal node is precisely "a type tag on a
generic thing" — the single most ontology-shaped construct §1.1 forbids — so it is dropped. A **DMZ** is a `dmz`
Entity (its security posture, when a policy Contract demands it, is a Facet on `dmz`); an **availability-zone**
is an `availability-zone` Entity (a failure domain). Membership is placement Relations to the specific kind
(`--in-dmz-->`, `--in-az-->`), each modeled/declared/observed like any other Entity — the spine never grows a
"DMZ" or "zone" concept. (This also removes the AZ double-representation the draft had.)

**4. Desired topology reuses ADR-0058's seam, generalized for named singletons.** `Intent/Subnet`,
`Intent/DnsRecord`, `Intent/Dmz` are provisioning Intents: CaC → the provision reconcile surfaces a **gated
build Finding** (framework `provision`, `entity_id` NULL — no phantom, §1.2) → the operator launches the
`buildWorkflow` → the build Run projects the Entity back with its `projectKind`/labels/correlation label. The
**Finding/gate/project-back plumbing is reused verbatim** (`WriteProvisionFinding`, `ResolveProvisionFindingsExcept`).
The one generalization: ADR-0058's planner is Compute-specific (`count`+`namePrefix`+ordinal); subnet/DNS/dmz are
**cardinality-1 named singletons** (a subnet *is* a CIDR, not `count: N`). So `core/internal/provision` gains a
**named-singleton planning mode** (desired = the one named Entity; built = its correlated projection) alongside
the Compute count/ordinal mode, and `reconcileProvisioning` branches per Intent kind instead of hard-filtering
`IntentCompute`.
- **Exclusive claim (S2):** the correlation key is **`(intentKind, name)`** — a per-kind namespace, NOT the
  `stratt.intent/instance` label (a subnet is not an instance, §2; a flat shared label would falsely collide an
  `Intent/Subnet web-dmz` with a Compute instance named `web-dmz`). Two Intents claiming the same `(kind, name)`
  is a **compile error through the same ownership-registry `ErrOwnerConflict` path** as ADR-0058, never an
  ad-hoc check or a tiebreak (§2.4).
- **Max-delta (S3):** named singletons have no per-Intent count fan-out, so the compute count-fraction gate
  does not apply. §4.3 bites on the **number of singleton builds surfaced per reconcile pass** (a DNS-zone
  import minting 500 `Intent/DnsRecord` → 500 gated builds pauses the batch) plus any placement-cascade fan-out
  — the same "pause, never silent fan-out" guard, keyed on build count not ordinal count.

`builder`/`params` stay opaque per §1.1 — a `crossplane`/`opentofu`/`dns`/`vsphere-network` Actuator is the swap
point (§1.4/§1.5), each its own plugin ADR; the spine learns no provider's network model.

**5. Placement is *composed*, not free-floating — its CaC home is the Intent (§1.2).** Because a Relation
cannot be declared in Git, desired placement rides the **provisioning Intent**: `Intent/Compute` (and the
network Intents) gain an optional `placement: {subnet: web-dmz, zone: dmz}`. The build honors it — it creates
the host *in* that subnet — and its Run projects **both** the host Entity **and** the `placed-in` Relation
(Run-provenance). So "three web servers in the DMZ subnet" is one declaration; the placement edge is a
projection of the built reality, never a hand-authored graph row. Observed placement (a cloud Syncer) confirms
and maintains the same edge. **Placement drift is surfaced, never silent (S5, §1.8):** when an Intent's declared
`placement` diverges from the *observed* placement of an existing host, the reconcile raises a placement-drift
Finding — the desired-vs-observed gap is diagnosable, not quietly wrong. Converging it (re-placing a live host)
is a **gated move Workflow**, not a reconcile edit — a deferred follow-up; until it lands the Finding is the
signal.

**6. Relation-aware View selection is the new selection capability.** "Select the hosts in the DMZ" is a View
that filters by **placement edge** — genuinely new (today's selector joins only kind/label/facet, never
`graph.relation`). ADR-0059 adds a `Relations []RelationPredicate` clause to `ViewSelector` (`{type, targetKind,
targetLabels}`) compiled to an `EXISTS` join over `graph.relation` — a versioned `View` bump, additive. This is
how a fleet is targeted *by topology* ("the web tier in the DMZ") rather than by label alone. Sequenced as its
own slice; until it lands, fleets select by kind/label as today.

**7. Relation GC is a prerequisite, not an afterthought — and it is TWO paths (S1, §1.2, ADR-0042).** Placement
makes dangling edges a correctness problem: a decommissioned host must not leave a `placed-in` edge behind. This
ADR's build **must** close the existing gap, and the two edge origins need two distinct GC mechanisms because a
build-Run-written edge is **never re-observed by any Syncer** (so no seen-set covers it):
- **(a) Syncer delta retraction** — wire the currently-unconsumed `GoneRelations` ObserveResponse field
  (`plugin.proto` `gone_relations`) through the host, so a connector that stops observing an edge retracts it.
- **(b) Endpoint-tombstone cascade** — referential integrity for **Run-provenance** placement edges: when either
  endpoint Entity is tombstoned, its placement edges are retracted (entities are *soft*-deleted, so the SQL
  `ON DELETE CASCADE` never fires — this is an explicit relation-GC sweep on the tombstone path).

No new edge type ships without its GC. This is why relation GC is a build prerequisite, not a follow-up.

**Scope.** This ADR ships the **model** (topology kinds, named Facets, placement Relation, the zone construct)
+ the **generalized provision seam** for named-singleton network Intents + the **placement-on-Intent**
composition. Deferred to their own slices/ADRs: the `crossplane`/`opentofu-network`/`dns`/`vsphere-network`
build Actuators (plugin ADRs); the relation-aware View selector (decision 6); relation GC wiring (decision 7,
a build prerequisite); VLAN (a straightforward second network kind once subnet lands).

## Charter alignment

- **§1.1 — type the seams, not the world.** Every construct is a demanded named seam: a free-string kind, a
  Facet **namespace owned-but-uncovered until a Contract consumes it** (no schema ships in this slice), a typed
  Relation. No universal network schema; DMZ/AZ/region are their **own distinct kinds**, not a generic zone
  tagged by a discriminator (which would be the type-tag-on-a-universal-node smell). `params` stay opaque per
  builder; Facet ownership sits on the consuming plugin, never core-in-tree.
- **§1.2 — projections, never a second truth.** Subnets/zones/DNS/placement are projections (observed or
  build-Run-written); desired-but-unbuilt topology lives in the Intent (Git), never as a graph row. Placement
  is a projection of built reality, not a hand-authored edge. Relation GC keeps the projection honest.
- **§5 Flow 1 + §4.3 — gated, blast-radius-bounded.** Network provisioning reuses the compute gate: a gated
  Finding, never auto-launch; the max-delta batch pause applies to network fan-out too.
- **§2.4 — no implicit precedence.** Named-singleton correlation is an exclusive claim (two Intents for the
  same subnet name is a compile error, like the Compute exclusive-claim); no last-writer-wins.
- **§1.4/§1.5 — boring spine, sovereign port.** Each landscape's network Actuator is a plugin behind `builder:`;
  core learns no provider's network model.

## Consequences

- **Positive:** the full onboarding shape — "N servers on network X, in the DMZ, with DNS" — becomes declarable
  and composes on one seam; topology is first-class (descent, traversal, selection-by-placement) without a
  network ontology; every landscape's networking slots in as an Actuator + typed Intent kind.
- **Negative / trade-offs:** three genuinely-new mechanisms (named-singleton provisioning mode, relation-aware
  selection, relation GC) — each a real slice, not free. The provision controller grows an intent-kind branch.
  Placement-via-build couples a host's network to its build Run (re-placing an existing host is a follow-up: a
  gated move Workflow, not a reconcile edit).
- **Follow-ups:** the network Actuator plugin ADRs; the relation-aware selector + relation GC builds; VLAN;
  re-placement (gated move); DNS-record lifecycle (like the certissuer reconcile, ADR-0050).

## Alternatives considered

- **A `network` super-Entity / one schema for all topology** — rejected: the universal-ontology violation §1.1
  exists to prevent. Distinct kinds + narrow Facets is the charter-clean shape.
- **Placement as a Facet only** (`net.placement` on the host) — rejected as the *model*: placement is a
  relationship between two Entities, which is precisely what a Relation is; a facet blob loses descent and
  traversal. (Where a cloud Syncer already carries `subnetId` in `instance.network`, that stays as observed
  data, but the authoritative topology is the edge.)
- **Declaring placement Relations directly in CaC** — rejected under §1.2: the graph holds projections; desired
  placement belongs on the Intent (decision 5), realized by the build, not written as a graph row from Git.
- **A generic `zone` kind + `zone.kind` discriminator for DMZ/AZ/region** — rejected: a type tag on a universal
  node is the most ontology-shaped construct available, and it contradicts the distinct-kinds rule this ADR
  applies everywhere else. Each is its own free-string kind.
- **Extending Intent/Compute's count/ordinal to subnets** — rejected: a subnet is a named singleton, not a
  fungible count; forcing ordinals would mismodel it. Hence the named-singleton planning mode.

## Reviews

- **charter-guardian (2026-07-18): SOUND-WITH-CHANGES → folded.** The machinery is charter-clean and visibly
  written to resist §1.1 (free-string kinds, free-string Relation `Type`, the gated build/Finding/project-back
  seam reused verbatim from ADR-0058, no auto-launch); it does not smuggle a universal network ontology at the
  machinery level. Two places re-admitted ontology and are fixed; six flags folded.
  - **M1 (§1.1 schema ahead of a consumer):** the draft hardened `net.subnet`/`dns.record`/`zone.kind` schemas
    while every consumer was deferred — the exact creep this ADR prevents. Fixed: this slice ships **no JSON
    Schema**; Facet namespaces are registered owned-but-uncovered, schemas land in the Actuator/selector ADRs
    that demand them (decision 1).
  - **M2 (§1.1 `zone` discriminator + AZ double-modeling):** the generic `zone` + `zone.kind` enum was a
    type-tag on a universal node (and AZ was modeled twice). Fixed: `dmz`/`availability-zone`/`region` are each
    their own free-string kind, consistent with the distinct-kinds rule; the generic `zone` construct is dropped
    (decision 3).
  - **M3 (§1.2 enforcement not convention):** placement-edge write-restriction is now stated as **data-layer**
    enforced — the existing `relation_write_path` trigger (the edge twin of `enforce_write_path`) — and the
    build edge rides Run-provenance, never a reconcile/API write (decision 2).
  - **Flags folded:** S1 — relation GC is **two** paths (Syncer `gone_relations` retraction + endpoint-tombstone
    cascade for Run-written edges), decision 7; S2 — exclusive-claim key is `(intentKind, name)` via the
    ownership-registry `ErrOwnerConflict` path, not the overloaded `stratt.intent/instance` label, decision 4;
    S3 — §4.3 max-delta keyed on singleton builds **per reconcile pass**, not count-fraction, decision 4;
    S4 — Facet ownership assigned to the consuming plugin, not core, decisions 1 + §1.1 alignment; S5 —
    placement drift raises a Finding (§1.8), decision 5; S6 — scrubbed the §2-banned "resource" from the
    named-singleton framing, and the identifiers (`subnet`/`dmz`/`availability-zone`/`dns-record`/`vlan`/
    `placed-in`/`in-dmz`/`in-az`/`net.subnet`/`dns.record`) run through `vocabulary-linter` before they freeze
    in code (they live in the free-string kind/edge space, outside the frozen v1.0 vocabulary; none use
    `inventory`/`playbook`/`CMDB`/`CI`). Also corrected decision 2's loose §1.8→§2.1 cite for topology traversal.
  - **What held (no change):** the verbatim gated-build reuse (no phantom, no auto-launch); placement-as-Relation
    over placement-as-Facet; desired placement on the Intent (not a hand-authored edge); relation GC as a build
    **prerequisite**.
