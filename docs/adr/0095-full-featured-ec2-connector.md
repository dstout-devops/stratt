# ADR 0095 — Full-featured EC2 connector: instance lifecycle + resource Actions, and the instance.* Facet contracts

- **Status:** **Accepted** (2026-07-22, steward) — scope **C** chosen (full resource graph, phased:
  C1 instance lifecycle + facets, C2 resource Actions, C3 resources-as-Entities under a follow-up
  ADR-0096). charter-guardian **PASS** (4 flags folded): (1) fire-and-return resources get a
  **stratt-owned marker tag** at creation so the C3 Syncer / an orphan scan can find them — no silent
  billable leak; (2) the closed `instance.*` schemas must EXACTLY match `normalizeInstance` (drift is a
  blocking write-path rejection now the Syncer is live) — a **co-fidelity test** asserts a real
  normalized instance validates against the shipped schemas; (3) lifecycle Actions (start/stop/reboot/
  terminate) project **only `instance.state`** (the Facet they authoritatively affect) — the Syncer
  owns compute/network, tighter provenance lineage; (4) bindable outputs are typed **per-op**
  (`securityGroupId`, `volumeId`, …), never a generic `resourceId`. vocabulary-linter **PASS**.
- **Date:** 2026-07-22
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.1 (type the seams — the missing `instance.*` Facet schemas) · §1.2
  (projections, never a second truth) · §1.5 (sovereign contracts) · §1.8 (never hide diagnosis) ·
  §2 (frozen vocabulary — new Action nouns) · builds on ADR-0046 (plugin port), ADR-0058
  (provisioning seam), ADR-0093 (Floci real-host dev backend), ADR-0094 (vault creds).

## Context

`plugins/awsec2` today is a thin slice: one Action (`awsec2/create-vm`), an instance `Observe` Syncer
that is **defined but opt-in** (started only when `STRATT_AWS_INTERVAL` is set), and three Facet
namespaces — `instance.compute` / `instance.network` / `instance.state` — that are emitted **with no
pinned schema**. That last point is a real §1.1/§1.5 gap: the plugin proposes Facet values the core
cannot validate, because no shipping Contract demands them. With a real EC2 backend now in the harness
(Floci, ADR-0093) and vault-brokered creds (ADR-0094), this slice makes the connector **full-featured
for the instance lifecycle** and closes the schema gap.

**Empirically grounding scope (Floci probe, 2026-07-22):** Floci backs `RunInstances`,
`DescribeInstances`, `TerminateInstances`, `RebootInstances`, `Start/StopInstances` (state-permitting),
`CreateTags`, and even `CreateSecurityGroup` / `CreateKeyPair` / `CreateVolume` / `CreateVpc`. So the
"within reason" boundary is **not** Floci capability — it is **Entity-kind expansion**: modeling
security-groups, volumes, VPCs, subnets, images as first-class *Observed Entities* (each its own
identity scheme, Facet namespaces, and Syncer enumeration) is a large resource-graph effort that
deserves its own ADR. This slice deliberately stops short of that.

**In scope:**
1. **Instance lifecycle Actions** on the existing `instance` Entity: `awsec2/start`, `awsec2/stop`,
   `awsec2/reboot`, `awsec2/terminate` — each driving a real state transition, projecting the instance
   with Run provenance (§1.2), live-proven against Floci.
2. **Tagging**: `awsec2/tag` (CreateTags) — instance metadata the next Syncer poll reflects.
3. **Resource-provisioning Actions, fire-and-return**: `awsec2/create-security-group`,
   `create-key-pair`, `create-volume`, `create-vpc`, `create-subnet` — provision the resource and
   return its typed id as bindable output (the crossplane `create-subnet` pattern), **without** minting
   a new Observed Entity kind. They are convenience seams for building an instance's network/storage
   context; the resources are not yet graph Entities.
4. **The `instance.*` Facet contracts** — author `contracts/facets/instance.{compute,network,state}.
   schema.json` (closed schemas matching what `normalizeInstance` emits), closing the §1.1 gap and
   making the Syncer's Facet writes validated.
5. **Wire the instance Syncer** into the dev/e2e substrate (enable `STRATT_AWS_INTERVAL`) and prove a
   live projection of Floci instances as graph Entities.

**Out of scope (a later "EC2 resource graph" ADR):** observing security-groups / volumes / VPCs /
subnets / images as first-class Entities (new identity schemes + Facet namespaces + Syncer
enumeration); AMI creation from a running instance (`CreateImage`); autoscaling; spot; the richer
`RunInstances` surface beyond keypair/SG/subnet/user-data (which this slice *does* thread through
create-vm where cheap).

## Decision

Grow `plugins/awsec2` from one Action to a lifecycle-complete instance connector, keeping the
**instance** as the only Observed Entity kind:

1. **`EC2API` interface** gains exactly the methods the new Actions call — `StartInstances`,
   `StopInstances`, `RebootInstances`, `TerminateInstances`, `CreateTags`, `CreateSecurityGroup`,
   `CreateKeyPair`, `CreateVolume`, `CreateVpc`, `CreateSubnet` — each matching the `*ec2.Client`
   signature so the real client still satisfies it and tests still inject a fake (the ADR-0046
   isolation proof).
2. **`Invoke` becomes a switch-on-action-name** dispatcher (today it is a single-action guard). Each
   case unmarshals its own params struct, does manual required-field validation, performs the AWS
   call, streams a typed progress `TaskEvent` + a terminal `InvokeResult` carrying **its own**
   `OutputContract` SchemaId (`actions/awsec2/<op>.output`, which the core pins and drift-checks) and,
   for the instance Actions, the projected instance `ObservedEntity` with Run provenance.
3. **Contracts (data, §1.5).** Each new Action ships `contracts/actions/awsec2/<op>.{input,output}.
   schema.json` (closed schemas, credential-free inputs — creds are a CredentialRef, §2.5). The three
   `instance.*` Facet schemas are authored to match `normalizeInstance`. `TestPinsAreStable` bumps from
   60 by the number of new schema files.
4. **DryRun honesty (§1.8).** Actions whose EC2 call supports `DryRun` (start/stop/reboot/terminate,
   tag, the create-* resource ops) advertise `DryRunnable: true` and surface the `DryRunOperation`
   API signal as plan-success (the existing `isDryRunSuccess` pattern). `terminate` is
   `Idempotent`-shaped but effectful; it is DryRunnable via the API's own dry-run.
5. **Registration.** One `registerPluginAction("awsec2/<op>", awsHost, dryRunnable)` per Action on the
   existing `awsHost` (the crossplane/helm multi-registration pattern). The Syncer wiring (Block B) is
   unchanged; the dev/e2e values set `STRATT_AWS_INTERVAL` so it runs.
6. **Authz stays the CredentialRef use-check.** Every Action is gated by its CredentialRef (§2.5,
   ADR-0052) — the dev estate binds a gate-only or material-bearing ref per the backend.

## Charter alignment

- **§1.1 (type the seams).** Closes the real gap: `instance.*` Facets get pinned, hash-verified
  schemas demanded by the shipping Syncer Contract — no whole-Entity ontology, three narrow Facets.
- **§1.2 (projections).** Every Action projects the instance with **Run provenance**; the Syncer
  reconciles with **Source provenance**. No second writer; resources created fire-and-return are not
  projected as a competing truth.
- **§1.5 (sovereign contracts).** New Actions are typed input/output Contracts (data), pinned and
  drift-checked at the port; the plugin never introduces a schema.
- **§1.8 (never hide diagnosis).** Each Action streams typed TaskEvents; DryRun is honest; a failure
  rides the terminal not-ok event, not a swallowed error.
- **§2 (vocabulary).** New identifiers are Action nouns under the connector namespace
  (`awsec2/start`, …) — no banned core-model term, no new Named Kind (vocabulary-linter gate).
- **Tension noted (F-1, the "within reason" line).** Resource Actions (SG/keypair/volume/vpc/subnet)
  provision real cloud objects that are **not** modeled as Observed Entities *until C3*. To avoid a
  silent billable-leak window (guardian flag 1), every fire-and-return Action stamps a **stratt-owned
  marker tag** (`stratt:managed=true` + a `stratt:correlation` id) on the created object at creation,
  so the C3 Syncer — or an interim orphan scan — can always enumerate what the platform made. The
  boundary is disclosed (§1.8), not hidden, and closes in C3 (ADR-0096).

## Consequences

- **Positive:** the `instance` Entity becomes lifecycle-complete and fully observable with validated
  Facets; a real, rug-pull-safe backend (Floci) proves every Action end-to-end; the switch-on-action
  dispatcher becomes the template every multi-Action plugin copies.
- **Negative / trade-offs:** ~20 new contract files + a broader `EC2API` surface to maintain; the
  resource Actions create objects the graph can't yet see (follow-up); Floci's fidelity for
  SG/volume/vpc is stub-ish, so those Actions are round-trip-proven, not deep-state-proven.
- **Follow-ups:** the "EC2 resource graph" ADR (observe SG/volume/vpc/subnet/image as Entities);
  richer `RunInstances` (keypair/SG/subnet/user-data threaded through create-vm); a KubeVirt/Incus
  true-VM backend (the deferred Slice-A follow-up) for Actions Floci only stubs.

## Alternatives considered

- **Full resource-graph modeling now (observe SG/volume/vpc as Entities).** Rejected for this slice —
  each new Entity kind is its own identity scheme + Facet namespaces + Syncer enumeration + grant; five
  of them at once is a multi-ADR effort that would balloon the slice past "within reason."
- **Reclassify awsec2 as an ACTUATOR (View-targeted Plan/Apply lifecycle).** Rejected — EC2 instance
  lifecycle is imperative per-instance, not a declarative View reconcile; the Action model fits, and
  ADR-0058's provisioning seam already routes create-vm as an Action. Keep `Verbs:[OBSERVE, INVOKE]`.
- **Skip the instance.* schemas (leave Facets uncovered).** Rejected — uncovered Facets pass
  validation silently, the exact §1.1 "type the seams" gap; a full-featured connector must ship them.
