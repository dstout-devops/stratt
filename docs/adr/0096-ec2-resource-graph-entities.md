# ADR 0096 — The EC2 resource graph: VPC / subnet / security-group / volume as Observed Entities

- **Status:** **Accepted** (2026-07-22, steward) — charter-guardian **PASS** (multi-source Facet
  grain verified: migration 00035 keys `graph.facet` on `(entity_id, namespace, prov_source_id)` so
  three Sources co-owning `net.subnet` never collide; 00036 keeps authority exclusive, awsec2 registers
  non-authoritative). Two flags folded: (1) `resource` softened in prose — **no shipped identifier is
  named `resource`** (kinds are `vpc`/`subnet`/`security-group`/`volume`); vocabulary-linter **PASS**.
  (2) The dual `net.subnet` co-fidelity test is a **BLOCKING release gate**, not prose: because the
  union schema now governs crossplane's *live* write path, a core-side test asserts BOTH crossplane's
  `{claim,name,cidr}` and awsec2's `{cidr,availabilityZone,state,vpcId}` validate against the compiled
  schema (the same `ValidateFacet` crossplane's writes hit), plus the awsec2-side key-subset test.
- **Date:** 2026-07-22
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.1 (type the seams) · §1.2 (projections, never a second truth) · §2.1
  (Entity / Relation / Provenance, multi-source Facet ownership) · §2 (frozen vocabulary — new Entity
  kinds + identity schemes) · closes ADR-0095's F-1; builds on ADR-0059/0060 (crossplane/NetBox
  multi-source model, the `subnet` Entity + `net.subnet` Facet), ADR-0093 (Floci).

## Context

ADR-0095 C2 added fire-and-return resource Actions that provision real VPCs, subnets, security groups,
and volumes, each stamped `stratt:managed=true` — but the graph does not yet know these objects exist
as Entities (F-1). C3 closes that: the awsec2 Syncer enumerates them and projects them as first-class
**Observed Entities**, so the estate reflects the real cloud network/storage topology and the
`stratt:managed` marker becomes queryable (the orphan-Finding foundation).

**Alignment constraint (ADR-0059/0060).** The `subnet` Entity kind and the `net.subnet` Facet already
exist — crossplane and NetBox project them under the multi-source Facet-ownership model. So awsec2
must **reuse** the shared `subnet` kind + `net.subnet` Facet (as a third Source), not mint an
`aws-subnet` parallel. `net.subnet` is currently **uncovered** (no schema); authoring a closed schema
now — required to validate awsec2's live writes (§1.1) — makes this ADR responsible for **both**
plugins' co-fidelity against it (a closed schema that omits a field crossplane emits would break
crossplane's live projection). The schema is therefore a **union** covering every field any Source
emits, with a co-fidelity test per plugin.

**In scope:** four Observed Entity kinds from awsec2 — `vpc`, `subnet`, `security-group`, `volume` —
with identity schemes, Facet schemas, Relations, and Syncer enumeration; the `stratt.managed` label
derived from the marker tag; grant expansion.

**Out of scope:** AMI/image Entities; instance→subnet/sg/volume attachment Relations beyond the ones
below; orphan-Finding generation (the marker makes it *possible*; the Finding rule is a follow-up);
security-group *rule* modeling (ingress/egress as sub-Entities).

## Decision

The awsec2 Syncer observes four resource kinds in addition to `instance`, each projected material-free
with Source provenance (§1.2):

1. **Entity kinds + identity schemes** (AWS-native ids, per-Source):
   - `vpc` → `aws.vpcId`
   - `subnet` → `aws.subnetId` (the shared `subnet` kind; awsec2 is a third Source alongside
     crossplane/NetBox)
   - `security-group` → `aws.securityGroupId`
   - `volume` → `aws.volumeId`

2. **Facets (closed schemas, §1.1).**
   - `net.subnet` — **reused + newly schema'd as a union**: `{claim, name, cidr}` (crossplane) ∪
     `{cidr, availabilityZone, state, vpcId}` (awsec2), all optional, closed. Co-fidelity tests assert
     BOTH plugins' emissions validate.
   - `net.vpc` — `{cidr, state, isDefault}` (new).
   - `net.securitygroup` — `{groupName, description, vpcId}` (new).
   - `storage.volume` — `{sizeGiB, volumeType, state, availabilityZone}` (new).

3. **Relations (§2.1, topology edges the Syncer observes):**
   - `subnet --in-vpc--> vpc` (by `aws.vpcId`)
   - `security-group --in-vpc--> vpc`
   - `volume --attached-to--> instance` (by `aws.instanceId`, when attached)
   Emitted as `ObservedRelation`s; the core resolves them by identity and stamps provenance.

4. **The `stratt.managed` label.** When an observed object carries the `stratt:managed=true` marker
   (ADR-0095 C2), the projection sets a `stratt.managed` label — so a View/query can enumerate exactly
   what Stratt provisioned (the orphan-Finding foundation). Objects Stratt did not create are still
   observed (the graph is a full projection, §1.2), just unlabeled.

5. **Observe-all, not managed-only.** The Syncer enumerates every VPC/subnet/SG/volume the account
   reports (`DescribeVpcs`/`DescribeSubnets`/`DescribeSecurityGroups`/`DescribeVolumes`), full-sync
   with per-kind tombstone schemes — the graph is the real estate, not just Stratt's slice. Each kind
   is normalized in isolation (pure content-expertise) and streamed in the one Observe full-sync.

6. **Grant expansion (strattd).** The awsec2 grant adds the four identity/tombstone schemes, the new
   Facet namespaces, and `stratt.managed` to LabelKeys. Registered on the existing Syncer host.

## Charter alignment

- **§1.1.** Four narrow Facets, each demanded by the shipping Syncer Contract, closed schemas. Closes
  ADR-0095 F-1 and retroactively covers `net.subnet` (previously uncovered).
- **§1.2.** Every projection is Source-provenance, material-free; the Syncer is the read-model, desired
  state is unaffected. Multi-source `net.subnet` co-ownership follows ADR-0060 (awsec2 is not
  authoritative unless the operator grant says so).
- **§2.1 (multi-source Facet ownership).** awsec2 co-owns `net.subnet` with crossplane/NetBox; the
  per-Source Facet grain (ADR-0060) keeps their rows distinct — no writer fight.
- **§2 (vocabulary).** New Entity kinds (`vpc`, `security-group`, `volume`) are cloud-native nouns, not
  frozen Named Kinds and not banned terms; new identity schemes are `aws.*`. vocabulary-linter gate.
- **§1.8.** The `stratt:managed` marker + full observation means abandoned Stratt-created state is
  *findable*, not hidden — the orphan-Finding foundation ADR-0095 F-1 promised.

## Consequences

- **Positive:** the estate reflects the real EC2 network/storage topology with typed Facets +
  Relations; `stratt:managed` becomes queryable; awsec2 becomes a full multi-kind Syncer (the template
  for cloud connectors).
- **Negative / trade-offs:** authoring `net.subnet`'s schema now binds crossplane's live projection to
  it — a cross-plugin co-fidelity dependency (mitigated by tests for both). Observe-all can be large on
  a real account (bounded later by tag/region filters if needed). Five Describe calls per sync cycle.
- **Follow-ups:** orphan-Finding rule (stratt.managed + no owning Intent → Finding); instance↔resource
  attachment Relations; security-group rule sub-modeling; region/tag-scoped observation for large
  accounts.

## Alternatives considered

- **awsec2-specific facet namespaces (`aws.subnet` etc.).** Rejected — fragments the estate; a subnet
  is a subnet (ADR-0059 one-estate-multiple-sources). Reuse the shared `subnet` kind + `net.subnet`.
- **Observe only `stratt:managed` objects.** Rejected — the graph is a projection of the real estate
  (§1.2), not just Stratt's slice; managed-only would hide real topology and defeat drift detection.
- **Leave `net.subnet` uncovered (no schema) to avoid the cross-plugin dependency.** Rejected —
  inconsistent with C1's §1.1 discipline; the union schema + dual co-fidelity tests is the correct
  cost of a shared seam.
