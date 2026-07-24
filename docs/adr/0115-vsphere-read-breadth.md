# ADR 0115 — vSphere read breadth: the inventory graph (region/AZ reuse, uncovered-Facet posture)

- **Status:** **Proposed** (2026-07-24, steward) — vocabulary-linter **CLEAN**; charter-guardian **PASS after fixes**
  (F1: reconcile the pre-existing `net.subnet` closed-union gap — vSphere's `normalizeNetwork` emits
  `{name, moref, kind, source}` but the closed schema only declares `{claim,name,cidr,availabilityZone,state,vpcId}`,
  so the undeclared keys would be REJECTED at the write path; fix = drop the provider-local keys from the shared blob
  (moref is already the identity key, source is already a label) rather than widen the shared union, + add a vSphere
  co-fidelity test row — scheduled in slice 1; F2: the slice-2 `datastores` View must genuinely consume
  `storage.datastore` FACET FIELDS via a `facets:` selector, else the pinned schema has no consumer — §1.1; F3:
  vocabulary-linter CLEAN).
- **Date:** 2026-07-24
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.1 (type the seams, not the world — every Facet schema demanded by a shipping
  Contract; no whole-Entity schemas; §9 ontology-creep) · §1.2 (the Syncer projects observed state; the
  graph is a rebuildable read-model) · §2 (frozen vocabulary — `resource` is BANNED) · §2.2 (Syncers use
  only the hand-written top rung for Facet schemas). **Extends [ADR-0007](0007-phase0-syncer-sdk-and-dev-harness.md)**
  (the shipped govmomi/vcsim vcenter Syncer). **Reuses [ADR-0059](0059-network-topology-primitives.md)**
  (the SHARED `region`/`availability-zone`/`subnet` kinds + placement Relations, and its "no schema ahead
  of a consumer" self-check). **Mirrors [ADR-0096](0096-ec2-resource-graph-entities.md)** (the EC2
  resource-graph read template — per-kind identity + tombstone schemes, observe-all, grant expansion).
  **Coexists with [ADR-0113](0113-vsphere-provisioning-provider.md)/[ADR-0114](0114-entity-lifecycle-and-decommission-reach-path.md)**
  (build/teardown correlate on the existing `vcenter.uuid`/`.host.uuid`/`.network.moref` schemes — this ADR
  only ADDS schemes, never renames them).

## Context

The vcenter Syncer reads 3 kinds (vm, host, subnet/portgroup); a vCenter GUI shows far more. This ADR
ingests the rest of the inventory so the graph answers topology/capacity questions: **where things run**
(datacenters, clusters, resource pools), **where they're stored** (datastores), **how they're wired/
organized** (the DVS switch, folders). It is a **read-only** Syncer expansion — no new Actions, no proto
change, no new dependency (govmomi already pinned). A prior-art scan fixed the charter-honest shape and
flagged the sharp risks: minting provider-specific kinds where shared ones exist, ontology creep (Facets
with no consumer), and the `resource` banned-term trap (ResourcePool).

## Decision

### D1 — Reuse the SHARED `region`/`availability-zone` kinds; vSphere is the first projector

vSphere **datacenter → `region`** and **cluster → `availability-zone`** (ADR-0059's shared kinds), exactly
as portgroups already project as the shared `subnet` (ADR-0113). Nothing projects region/AZ today, so
vSphere becomes the **first projector** — a deliberate, precedent-setting reuse (ADR-0096: "reuse before
minting; provider-specific kinds fragment the estate"). This invites awsec2 to later co-own by promoting
its `aws.region` label / `subnet.availabilityZone` field into the same kinds, so "the AZs across EC2 and
vSphere" becomes **one cross-substrate topology View**. A vSphere region and an AWS region are distinct
Entities (distinct identity schemes — `vcenter.datacenter.moref` vs a future `aws.region`), which is
correct: they are different regions. Only **`datastore`, `compute-pool`, `dvswitch`, `folder`** are new,
vSphere-specific kinds (no shared analog). (A vSphere **cluster** — a host failure-domain — is unrelated
to Stratt's Named Kind **Cell** — a control-plane shard; no collision, noted to avoid conflation.)

### D2 — Facets stay UNCOVERED by default; a schema ships only with a consumer (§1.1)

"A Facet demanded by a Contract" is a **review-time** discipline, not an automated gate: uncovered Facet
namespaces pass through the write path legally (today's `vm.config`/`vm.runtime`/`net.guest` are three
owned-but-uncovered Facets with NO schema files). So new kinds emit their rich attributes as
**owned-but-uncovered Facet blobs** (`storage.datastore`, `compute.pool`, `net.dvswitch`) — the data lands
in the graph, queryable, just not schema-pinned. A `contracts/facets/*.schema.json` is authored **only**
where a consuming View/Baseline ships in the same slice — mirroring ADR-0059's own M1 self-check ("this
slice ships no JSON Schema" because its consumers were deferred). **`region`/`availability-zone`/`folder`
ship as bare Entities** (identity + labels, no Facet), exactly as ADR-0059 shipped region/AZ. The single
Facet **schema** this ADR pins is **`storage.datastore`** (slice 2), paired with a `datastores` View whose
`facets:` selector is its shipping consumer — the §1.1 exemplar done right. Every pinned schema is
hand-written (§2.2 top rung, Syncers only), closed (`additionalProperties: false`), pinned + hash-verified.

### D3 — `compute-pool`, never `resource*` (§2); additive `vcenter.<type>.<field>` identity schemes

vSphere's `ResourcePool` becomes the **`compute-pool`** kind / **`compute.pool`** namespace — `resource`
is a §2-banned core-model term (the same scrub ADR-0059 S6 and ADR-0096 already applied). New identity
schemes follow the shipped `vcenter.<type>.<field>` convention: `vcenter.datacenter.moref` (region),
`vcenter.cluster.moref` (AZ), `vcenter.datastore.moref`, `vcenter.pool.moref`, `vcenter.dvs.uuid` (the DVS
has a native UUID), `vcenter.folder.moref`. Each is added to the vcenter operator Grant's `IdentitySchemes`
**and** `TombstoneSchemes` (the `TombstoneSchemes ⊆ IdentitySchemes` subset is compile-enforced), plus the
plugin Manifest — additive only; the existing schemes ADR-0113/0114 correlate build/teardown on are
untouched.

### D4 — Observe-all, full-sync tombstone (ADR-0096)

Every datastore/cluster/pool/… the account reports is observed, not filtered to Stratt-created objects
(ADR-0096's observe-all discipline). Each new kind is enumerated in the same full-sync pass; its identity
scheme is in `TombstoneSchemes`, so a removed object tombstones by absence on the next sync (ADR-0042) —
the same liveness the shipped kinds use.

### D5 — The topology Relation graph

Read breadth is most valuable as **edges**, not just nodes. New Relations (emitted inline in `enumerate`
like `runs-on`/`placed-in`, keyed by the target's identity scheme): `availability-zone --in-region-->
region`, `host --member-of--> availability-zone`, `vm --stored-on--> datastore`, `host --has-datastore-->
datastore`, `vm --in-pool--> compute-pool`, `subnet --on-switch--> dvswitch`, `vm --contained-in-->
folder` (the sovereignty-tenant edge, pairing with the seed's tenant folders). Most targets are direct
from object properties; **AZ→region** is the one non-trivial edge — `cluster.parent` is an intermediate
folder, so a `moref→parent` map is walked up to the datacenter set. Relations are Syncer-observation
writes (data-layer-gated to Syncer/Run provenance, ADR-0059).

## Charter alignment

Upholds §1.1 (uncovered Facets by default; one pinned schema WITH its consumer — no ontology creep, D2),
§1.2 (pure observed projection; the graph rebuildable), §2 (`compute-pool`, no banned term — D3), §2.2
(hand-written closed Facet schema — D2), §1.4 (govmomi plugin-tier; no new dep). It **touches the data
model** (new Entity kinds + one pinned Facet + new identity/tombstone schemes on the grant) — the highest
review bar (charter-guardian + vocabulary-linter). It does **not** touch the sovereign plugin port proto
or add any Action.

## Consequences

- **Positive.** The graph gains the full vSphere inventory picture (where things run/are stored/are wired),
  navigable by Relation. **Reusing region/AZ sets the cross-substrate topology precedent** — the first step
  toward one estate View spanning EC2 + vSphere regions/AZs. The uncovered-Facet posture gets the data in
  now while deferring schema commitment to real consumers (§1.1-clean). Mirrors the proven ADR-0096
  multi-kind read pattern.
- **Negative / trade-offs.** vSphere is the first region/AZ projector, so the shared-kind co-ownership
  (awsec2 promoting its region/AZ) is a _future_ reconciliation this enables but does not itself complete.
  Uncovered Facets are unvalidated at the write path until a consumer pins them (accepted — the §1.1
  default). The AZ→region parent-walk is the one non-trivial normalizer bit.
- **Follow-ups.** (1) awsec2 promoting `aws.region`/`subnet.availabilityZone` into the shared `region`/
  `availability-zone` kinds → the cross-substrate topology View. (2) Pinning `compute.pool`/`net.dvswitch`
  schemas when a consumer ships. (3) vSphere **tags** — prefer projecting into Stratt's existing Entity
  **label** surface over a `tag` kind (needs the vAPI/REST tagging service, not vim25; vcsim support
  unconfirmed) — booked, not in scope. (4) Structural **create** Actions (promote the seed's
  datacenter/cluster/DVS creation into the plugin surface) — a separate write-side feature.

## Alternatives considered

- **Mint `vsphere-datacenter`/`vsphere-cluster` kinds.** Rejected (D1, user-chosen): fragments the estate
  (ADR-0096's recorded rejection); a cross-substrate topology View could never span EC2 + vSphere.
- **Author Facet schemas for every new kind now.** Rejected (D2): ontology creep (§1.1/§9) — the exact
  violation ADR-0059's M1 review caught and fixed. Schemas follow consumers, not precede them.
- **`resource-pool`/`resource.pool` (the native vSphere noun).** Rejected (D3): `resource` is §2-banned.
- **A `tag` Entity kind for vSphere tags.** Rejected (follow-up): shadows Stratt's first-class label
  mechanism; project tags as labels if pursued.
- **Facets on region/AZ now.** Rejected (D2): they have no consumer; bare Entities suffice (ADR-0059 shipped
  them bare).
