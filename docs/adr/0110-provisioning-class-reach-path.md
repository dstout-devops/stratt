# ADR 0110 — The `provisioning` class reach-path: `Intent.builder:` → `requires: [provisioning]`

- **Status:** **Proposed** (2026-07-23, steward) — charter-guardian **PASS** (four accuracy flags folded: the per-kind
  D5 migration, D4 both-axes fail-closed, the net.subnet-only co-fidelity, the ADR-0105 D3 binding-shape extension);
  vocabulary-linter **incorporated** (the binding is a CaC declaration form, not a new Named Kind).
- **Date:** 2026-07-23
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.5 (sovereign contracts — a dependency targets the capability _class_, never a
  named provider; the provider is a swappable transport) · §1.4 (boring spine, pluggable everything —
  provider breadth behind one core contract) · §1.1 (type the seam, not the world — the class contract is
  the request _envelope_; `params` stays opaque) · §1.2 (projections, never a second truth — the "coordinate
  handback" is the OBSERVE/Run-provenance projection, ADR-0017, not an injected desired-state) · §2.4 (no
  implicit precedence — provider selection is an explicit binding or an auto-bind of the sole verified
  provider, never a priority/last-writer tiebreak). **This ADR delivers [ADR-0107](0107-ec2-provisioning-provider.md)
  Follow-up #1** — the reach-path refactor ADR-0107 D2 pre-specified and called "the highest-value deferred
  contract." Builds on ADR-0104 (capability dependencies + verified-provider index), ADR-0105 (provider-agnostic
  class contract + sole-provider auto-bind), ADR-0106 (enablement-gate vs resolve-inject + the D1 reach-path
  guardrail), ADR-0096 (awsec2 native VPC/subnet/security-group Actions — the network providers this binds to),
  ADR-0017 (provision→configure — the coordinate consumer shape). **Refactors** ADR-0058 (`Intent.builder:`).
  **Supersedes** [ADR-0059](0059-network-topology-primitives.md)'s framing of Crossplane as _the_ reference
  network builder (D5).

## Context

A provisioning reach-path already ships and it is **provider-coupled**: every `Intent/Compute` and every
named-singleton network Intent (`Intent/Subnet|Vlan|Dmz`) carries a `builder:` field that names a provider
Action **directly** — the live estate uses `builder: awsec2/create-vm` (`estate/intents/web-fleet.yaml`) and
`builder: crossplane/provision` (`estate/intents/{app-subnet,app-tier,dmz-subnet,net-vlan}.yaml`). The Intent
_author_ picks the provider, hardcoded into Git. That is exactly the §1.5 vendor-coupling the capability
framework exists to remove, and ADR-0107 D2 booked its removal as the framework's most consequential deferred
contract — it governs provision→configure across every substrate (EC2, VMware, Kubernetes, bare-metal).

The framework is ready to absorb it. `provisioning` is a core-owned capability class
([`types.CapProvisioning`](../../types/capability.go)) with a verified provider (`awsec2`, ADR-0107) — but
**zero consumers**: `requires: [provisioning]` is declared nowhere. The provider half exists; the reach-path
half was deferred to here. Critically, the seam is _narrower than it looks_: the Intent's `params` is **already**
opaque and §1.5-clean (validated against the builder's own input Contract downstream, never typed in the Intent
schema). The **only** provider-coupled fields are `builder` and `buildWorkflow`. Remove those two and resolve the
provider from an estate binding, and the coupling is gone with no `params` rewrite — precisely the "binding change,
not a consumer+params rewrite" ADR-0107 D2 promised.

In scope: the class contract, the estate `capability→provider` binding, the schema refactor of the four Intent
kinds, the reconcile resolution, and the disposition of the three coexisting AWS provisioners (awsec2 Actions /
Crossplane / OpenTofu). Out of scope (booked follow-ups): the OpenTofu network provider and the `vsphere-network`
write path (their own slices); a single generic core build Workflow (an optimization, not the decision).

## Decision

### D1 — The Intent targets the capability class, not a provider

`Intent/Compute` and the singleton Intents (`Subnet`/`Vlan`/`Dmz`) drop `builder:` and `buildWorkflow:` and gain
**`requires: [provisioning]`**. `params`, `count`/`namePrefix` (compute), `projectKind`, `labels`, `placement`,
and `maxDelta` are unchanged — they were never provider-coupled. This is a schema version bump (compute → v3;
singletons → v2) that removes two fields and adds one; the whole in-repo estate is migrated in the same slice
(CaC Intents are Git-declared, not DB rows — no rolling-replica concern, so the schema and its consumers change
atomically). Closes the ADR-0107 D2 gap: the Intent no longer names a provider.

### D2 — The `provisioning` class contract is the request envelope + a coordinate handback (enablement-gate)

The class-pinned "provision → coordinates" shape ADR-0107 D2 specified is: **request** = the typed Intent envelope
(cardinality/identity/`projectKind`/`placement` + opaque provider `params`); **response** = the built Entity's
correlation identity and its projected coordinate Facets (e.g. a machine's `instance.*`, a subnet's `net.subnet`),
consumed by the next provision→configure Step _by identity through the graph_ (ADR-0017), **not** an injected
handle. Provisioning stays **enablement-gate** (ADR-0106 D1 / ADR-0107 D1): `requires: [provisioning]` gates only
that a verified provider exists; there is no resolve Action and no core-injected desired-state. The provider
validates `params` against its **own** input Contract downstream (§1.1/§1.5) — the class never grows per-provider
network fields (the §1.1 no-universal-ontology line ADR-0059 already drew). This honors the ADR-0106 D1 guardrail:
the reach-path is the **class** envelope, never a named provider's mechanism.

Precisely (avoiding the overclaim): a provider swap is stable in the _typed_ consumer — the Intent's envelope and
`requires: [provisioning]` never move. `params` are the bound provider's **opaque inputs** and were never portable
across providers (§1.5: EC2's `{region,ami,instanceType}` ≠ GCE's `{zone,image,machineType}`); supplying the new
provider's inputs is expected, not a rewrite of the consumer. ADR-0107 D2's "binding change, not a consumer+params
rewrite" holds for the _typed_ envelope; the opaque `params` stay provider-shaped by design.

### D3 — Provider selection is a CaC `capability-binding` declaration (not a new Kind); resolution is per-(class, Intent kind)

Which verified provider fulfills `provisioning` for a given Intent kind is an **operator landscape choice** (§1.5),
not the Intent author's and not hardcoded in one actuator. It is expressed as a **CaC declaration** under
`estate/capability-bindings/` — deliberately **not a new Named Kind** (the vocabulary is frozen at v1.0, §2; this
is a declaration _form_ the capability registry reconciles, exactly as `estate/authz/` tuples configure OpenFGA
without being a Kind). It is the **first materialization** of the `capability-binding` surface booked by ADR-0104 D3
/ ADR-0105 D5, and it **extends** ADR-0105 D3's binding shape from `(class → provider)` to `(class, provider,
Intent kind)` — the binding selects **only the provider**; the provider owns its per-kind build mechanism (see
below), so a binding never re-specifies a mechanism (§1.5). Named here as an explicit extension so the two ADRs do
not silently disagree on what a binding carries. Scoped per-environment / View overlay like every other estate
declaration.

**The provider advertises its per-kind build Workflow.** Each `provisioning` provider carries a `provisions` map
(`Intent kind → build Workflow`) on its Actuator/Connector declaration — the concrete, gated Workflow that builds
that kind (it wraps the provider's build Action _and_ the provider-specific opaque params, both of which are the
provider's own, §1.5). This is what makes auto-bind possible with no binding at all (the sole verified provider's
`provisions[kind]` is the answer), mirroring how ADR-0105 auto-binds statestore's resolve Action — but _per kind_,
since provisioning has no single convention-derived name (a VM build ≠ a subnet build).

Resolution is **per-(capability class, Intent kind)**, over the ADR-0104 slice-2 verified-provider index:

- exactly one verified provider advertises a build Workflow **for that kind** → **auto-bind** (no declaration needed,
  reusing ADR-0105's sole-provider auto-bind);
- more than one → an **explicit** `capability-binding` entry selects the provider (the ADR-0104 "disambiguation at
  estate-level binding" resolution); **>1 with no entry is a compile error** (§2.4 — never a priority/last-writer
  tiebreak);
- the bound provider's build Workflow (its `provisions[kind]`) is what the gated Finding references — the Intent
  stays kind-typed and provider-blind.

### D4 — Resolution is fail-closed on BOTH axes and observable

The Intent's `requires: [provisioning]` resolves in the provision reconcile / desiredstate controller, over the
**verified**-provider index (ADR-0104 slice 2 — a phantom `provides` its Manifest doesn't back never counts). It
**fails closed** — an **observable pending Finding** (§1.8), never a silent no-op and never a build against an
unverified provider — in **either** case: **(a)** no verified provider provides the class; **or (b)** a provider
_is_ verified but **advertises no build Workflow for the required Intent kind** (e.g. an `Intent/Vlan` where the
bound provider builds only subnets). Case (b) is a real coverage gap (an AWS provider has no VLAN builder): it is a
pending Finding — _never_ a silent skip and _never_ an implicit fallback to a **different** provider (that would be
the §2.4 tiebreak D3 forbids). The gated build Finding stamps the resolved provider + build Workflow instead of the
Intent's removed `builder:`, so §1.8 one-click descent now _shows which provider was resolved and what to launch_.

### D5 — Crossplane is demoted from _the_ builder to _one_ bindable provider (supersedes ADR-0059's reference-builder framing)

ADR-0059 built Crossplane as the reference landscape-agnostic builder; four of the five live Intents bind
`crossplane/provision`. Evidence (this PoC's scans): Crossplane has **never provisioned a real cloud** — its only
reconcile target is an in-cluster `provider-kubernetes` stand-in (ConfigMap = subnet), and it runs **Syncer-only in
e2e**; its headline feature (a continuous in-cluster reconciler) **overlaps our own `tofu plan`/Findings drift loop**
and would be a second control plane (§1.2/§1.4) if made load-bearing. Therefore:

- **As a builder we _drive_:** Crossplane is **demoted to one bindable `provisioning` provider** — it advertises
  `provides: [provisioning]` (a new `estate/actuators/crossplane.yaml`) and stays _bindable_, but is no Intent's
  default. Migrating the five live Intents to `requires: [provisioning]` resolves — **honestly, per kind** (D3/D4).
  This lands in **two slices**, because the _seam_ and the _provider rebind_ are separable and the rebind is the
  bigger lift (it needs an awsec2 subnet build Workflow **and** a VPC to place subnets in — that is Slice 1, the
  real-AWS network path):

  | Intent(s)                  | Kind    | Seam slice (this ADR) — provider (build Workflow)                     | End state (post-Slice 1)                          |
  | -------------------------- | ------- | --------------------------------------------------------------------- | ------------------------------------------------- |
  | `web-fleet`, `app-tier`    | Compute | **awsec2** (`compute-build`) — auto-bind, sole provider               | unchanged                                         |
  | `app-subnet`, `dmz-subnet` | Subnet  | **Crossplane** (`subnet-build`) — auto-bind, _currently sole_ builder | **awsec2** via an explicit binding (the demotion) |
  | `net-vlan`                 | Vlan    | **Crossplane** (`vlan-build`) — auto-bind, **sole** VLAN builder      | unchanged (the permanent exception)               |

  **The seam slice changes no behavior** — every kind auto-binds the provider that builds it today (compute→awsec2,
  subnet/vlan→Crossplane), so it is a pure decoupling: the Intent stops naming the provider, the reconcile resolves
  it. Crossplane advertises `provides: [provisioning]` + its `provisions` map, so it is a _resolved_ provider now,
  not a hardcoded `builder:`. The **build-demotion** of subnets to awsec2 is the Slice-1 rebind below (it introduces
  the first explicit `capability-binding` and the first real >1-provider disambiguation):

  - `app-tier` is an **`Intent/Compute`** (a host with `placement.subnet`), so it re-binds to the _compute_ builder
    `create-vm`, **not** a network Action — it is not a "network Intent."
  - **AWS has no VLAN primitive** and awsec2 ships only vpc/subnet/security-group/volume Actions (ADR-0095/0096), so
    `net-vlan` **cannot** bind to awsec2. Crossplane is the _sole_ VLAN builder (its `net.vlan` Contract), so
    `net-vlan` **auto-binds to Crossplane** — the acknowledged exception that is precisely _why_ Crossplane stays a
    registered, bindable provider rather than deleted. It holds the VLAN leg until an alternative VLAN provider lands
    (Slice 1+), at which point `net-vlan` becomes a one-entry binding change.
  - The **subnet** re-bind is where the build-demotion concretely bites: both awsec2 and Crossplane build subnets, so
    an explicit `capability-binding` selects awsec2's native, typed, moto-tested `create-subnet` (ADR-0095/0096) over
    Crossplane's opaque stand-in Claim — the charter-aligned choice (typed Contracts over opaque Claims). This
    preserves the **`net.subnet`** union-schema co-fidelity (ADR-0096 — awsec2 already co-owns `net.subnet`, so the
    observe side is undisturbed). _(There is **no** `net.vlan` co-fidelity test to preserve: `net.vlan` is
    NetBox-observed and owned-but-**uncovered** per ADR-0059, not part of the ADR-0096 union.)_
  - OpenTofu network modules (§5.2, on the ADR-0105 statestore) become the **recommended default** for real-cloud
    network provisioning once that provider lands (Slice 1) — at which point Crossplane vs OpenTofu vs awsec2 for a
    substrate is a one-entry binding change, and §7.6 per-route accounting can settle it empirically.

- **As a system-of-record we _observe_:** Crossplane's OBSERVE/Syncer role is **unchanged** — projecting an existing
  Crossplane install's resources into the graph is a legitimate Connector use (identical to how §1.2 treats Terraform
  state, vCenter, and etcd as authoritative SoRs). Crossplane-as-a-Connector survives; Crossplane-as-our-engine does
  not.

This is the reconciliation the prior-art scan flagged as _owed_: the four `crossplane/provision` bindings get an
explicit, per-kind migration (never a silent drop, never left as the default), and the ADR-0096 `net.subnet`
co-fidelity discipline is preserved.

## Charter alignment

Upholds §1.5 (the Intent targets the class; the provider is a swappable binding), §1.4 (breadth behind one
contract; the boring spine keeps the reconcile, not a second controller fleet), §1.1 (the class contract types only
the envelope; `params` stays opaque — no per-provider ontology), §1.2 (coordinates are an OBSERVE projection, not an
injected truth; Crossplane-as-SoR is projected, not authored), §1.8 (the pending/gated Findings keep provisioning
diagnosable and now surface the resolved binding), and §2.4 (explicit binding or sole-provider auto-bind; a
double-provider is a compile error — no implicit precedence). It **touches the data model and Contracts** (the four
Intent schemas + a CaC `capability-binding` declaration form). On **vocabulary** (§2, frozen v1.0): the binding is
deliberately **not** a new Named Kind — it is a declaration _form_ the capability registry reconciles (like
`estate/authz/` tuples configuring OpenFGA), so the §2 surface is a directory/route/table name
(vocabulary-linter-ruled: `estate/capability-bindings/`, `/api/v1/capability-bindings`, table
`capability_provider_bindings`), not a Kind addition. It still carries the **highest review bar** (charter-guardian

- vocabulary-linter, both run before acceptance — see below). It does not approach a non-goal: `provisioning`
  remains machine/resource-**coordinate** provisioning (ADR-0107 D3), never OS imaging or a writable CMDB.

## Consequences

- **Positive.** Closes the framework's highest-value deferred contract (ADR-0107 D2): swapping EC2 → GCE/KubeVirt,
  or Crossplane → OpenTofu → awsec2 for a subnet, becomes a one-line binding change, not a consumer rewrite. Lands
  the **first `requires: [provisioning]` consumer**, making the capability framework's Intent-side edge real (it was
  provider-only). Crossplane is no longer hardcoded into four Intents. Sets up the §7.6 per-route accounting to
  decide provider fate on evidence once ≥2 providers back a substrate.
- **Negative / trade-offs.** A schema version bump on four Intent kinds + an estate migration of five live
  declarations (atomic, in-repo — but it _is_ a breaking Contract change, re-pinned in `TestPinsAreStable`). A new
  CaC **declaration form** for the binding — `estate/capability-bindings/` (route `/api/v1/capability-bindings`,
  table `capability_provider_bindings`), **not** a new Named Kind (vocabulary-linter) — with a small store/reconcile
  addition. `net-vlan` stays Crossplane-bound (the acknowledged sole-VLAN-provider exception) until an alternative
  VLAN provider lands. The build Workflow moves from the Intent to the bound provider; a single generic core build
  Workflow (Gate → resolved build Action → project-back) is the cleaner end state but is deferred (follow-up) to keep
  this slice to the seam.
- **Follow-ups.** (1) **Slice 1** — the OpenTofu (and/or native awsec2) network provider (VPC/subnet/security-group/**route
  table** — the one absent AWS primitive) on the ADR-0105 statestore, with `tofu plan`-on-cron drift (§5.2); this is
  the **first alternative subnet provider**, which turns the subnet leg into a >1-provider case and lands the **first
  explicit `capability-binding`** rebinding `app-subnet`/`dmz-subnet` off Crossplane (the build-demotion, D5). (2)
  **Slice 2** — the `vsphere-network` write Actuator (ADR-0059's pre-named slot) for the VMware leg. (3) A generic
  core build Workflow (Gate → resolved build Action → project-back), replacing the per-(provider,kind) build Workflows
  and closing the per-instance-parameterization gap the current `compute-build`/`subnet-build` carry. (4) Turn on §7.6
  per-route cost/latency/failure accounting once ≥2 providers back one substrate.

## Alternatives considered

- **Leave the reach-path as ADR-0058's `builder: <provider/action>`.** Rejected — ADR-0107 D2 already rejected this
  as the _end state_; it is the §1.5 coupling the framework prevents. This ADR is the booked refactor.
- **Let the Intent optionally name a provider (`builder:` as an override on top of `requires:`).** Rejected: it
  re-opens the exact coupling — an Intent author must not be able to pin the provider, or the vendor-lock returns by
  the back door. Provider selection lives in the estate binding (§1.5), full stop.
- **Give provisioning a resolve-inject handle (mirror statestore, ADR-0105).** Rejected (ADR-0107 D1 / ADR-0106 D1):
  provisioning is a target-scoped Apply that _creates_ a resource, not a low-rate config handle the core injects.
  Enablement-gate + coordinate-by-identity (ADR-0017) is the honest shape.
- **No binding declaration — bind by a `default: true` flag on the provider's Actuator declaration.** Rejected:
  provider selection is often per-environment/View (dev → localstack tofu, prod → real AWS), which a single boolean on
  the provider can't express; and it scatters the landscape decision across provider declarations instead of a legible
  `capability-binding`. Auto-bind covers the sole-provider case with no declaration at all; the explicit binding
  handles the rest.
- **Keep Crossplane as the default network builder.** Rejected (D5): it has never touched real cloud, runs Syncer-only
  in e2e, and its continuous reconciler is a second control plane (§1.2/§1.4). It stays _bindable_ and keeps its
  Syncer role, but is not the default.
