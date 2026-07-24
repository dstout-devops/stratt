# ADR 0111 — The `ipam` capability: global IP/VLAN allocation as resolve-inject, NetBox provider #1

- **Status:** **Proposed** (2026-07-23, steward) — vocabulary-linter **CLEAN**; charter-guardian **PASS after fixes**
  (F1: idempotency anchored in the provider, no Stratt-held allocation record — D4; F2: the builder-fixed scope is not
  per-workload sovereignty and not a guarantee yet — D5; `pool`/`role` schema-exclusive — D1; allocation-orphan
  lifecycle booked — Follow-ups).
- **Date:** 2026-07-23
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.5 (sovereign contracts — a consumer targets the `ipam` _class_; the provider is a
  swappable transport) · §1.4 (boring spine, pluggable everything — NetBox is community-tier plugin breadth,
  **never** spine; Postgres/NATS/Temporal stay the only spine deps) · §1.2 (projections, never a second truth —
  the IPAM tool is the authoritative allocation SoR; Stratt **drives + projects** it, never _becomes_ an IPAM;
  respects the "we feed CMDBs, we don't become one" non-goal) · §2.5 (a NetBox API token is a CredentialRef NAME,
  never inline material) · §1.7 (evergreen — NetBox pinned + N-1, CI-gated). Mirrors **ADR-0105** (the `statestore`
  resolve-inject pattern this reuses near-verbatim). Builds on **ADR-0104/0106** (capability framework;
  resolve-inject vs enablement-gate) and **ADR-0110** (the `provisioning` reach-path this composes with).
  Reconciles with **ADR-0059/0060/0096** (network topology, multi-source Facet ownership, the `net.subnet`
  blocking union) and cites **ADR-0060**'s dual-verb Crossplane precedent. dependency-scout: **RECOMMEND** for
  NetBox with binding conditions (D6).

## Context

ADR-0110 lets a network Intent target the `provisioning` capability (resolve a _builder_), but the subnet's
**CIDR and VLAN are still hand-authored in Git** (`estate/intents/app-subnet.yaml: params.cidr: 10.30.0.0/24`).
In a real enterprise, IP/VLAN allocation is a **global, cross-substrate** concern — prefixes and VLANs are carved
from supernets scoped by **region, availability zone, sovereignty (tenant), and VLAN group**, owned by an IPAM
system-of-record so two allocations never collide. That is precisely the substrate-transcending role Stratt is
built to orchestrate: **Stratt drives the IPAM (allocate), injects the assignment into the build, and projects the
result** — it must never _become_ the IPAM (§1.2 no writable CMDB).

The capability framework already has the exact shape for "resolve an external coordinate and inject it": `statestore`
(ADR-0105) — a consumer `requires` the class, the core invokes the sole verified provider's resolve Action, validates
a class-level Contract, and injects a handle at Run dispatch (`resolveCapabilities`, `orchestrate.go`). `ipam` is the
same shape with a different payload: allocate a prefix/VLAN → inject `{cidr, vlanId, gateway}`. NetBox is the
recognizable, de-facto-standard IPAM SoR (Apache-2.0 core; dependency-scout RECOMMEND) and models regions / sites (AZs)
/ tenants (sovereignty) / VLAN groups / hierarchical prefixes natively — so it is **provider #1** (Nautobot, the
Cluster-API IPAM contract, Infoblox are siblings behind the same class).

In scope: the `ipam` capability class + its class Contract, the netbox plugin's allocate Action, the consumer wiring
(reusing the shipped resolve-inject), and the enterprise-scope request shape. Out of scope (booked follow-ups): the
Intent-declares-the-request param-flow, the PDP sovereignty-policy admission, and the estate binding once ≥2 providers
exist.

## Decision

### D1 — `ipam` is a new capability class, resolve-inject (ADR-0106 D1), mirroring `statestore` (ADR-0105)

Add `ipam` to the core-owned capability vocabulary (`types.CapIPAM = "ipam"`). It is **resolve-inject**, not
enablement-gate: allocation is a low-rate _coordinate resolution_ (a CIDR/VLAN handback the consumer injects), not a
target-scoped Apply — the textbook resolve-inject case (ADR-0106 D1), identical in shape to statestore. It ships a
class-level, provider-agnostic Contract `capabilities/ipam.{input,output}` and a provider resolve Action
`<provider>/ipam-resolve`; the sole verified provider auto-binds (ADR-0105 D5), an explicit estate binding
disambiguates ≥2. NetBox is provider #1 (§1.5); the class never encodes NetBox specifics.

- **`ipam.input`** (the allocation _request_): `{ (pool XOR role), size, region?, availabilityZone?, tenant?, vlanGroup? }`
  — `size` is the prefix length. `pool` and `role` are **schema-exclusive** — the Contract encodes them as a JSON
  Schema `oneOf` (exactly one required), never a field pair with a runtime "role overrides pool" pick, which would be
  the implicit precedence §2.4 forbids _inside the seam_. The scope fields reference the **existing** ADR-0059 topology
  (see D5). Provider-agnostic.
- **`ipam.output`** (the injected _handle_): `{ cidr, vlanId?, gateway?, credentialRef? }` — the assignment the build
  consumes; `credentialRef` is a §2.5 CredentialRef **name** only (never material), exactly as `statestore.output`.
  Every provider fills the same shape, so a provider swap changes zero consumer declarations.

**A mutating resolve — accepted, but only because it is idempotent and provider-anchored.** Unlike statestore's resolve
(pure — it _locates_ existing state), an `ipam` resolve **mutates external state**: it carves a durable reservation from
a finite pool. It is still correctly resolve-inject (the output is a coordinate the builder _injects_, not a target
Entity created-then-consumed-by-graph-identity — the test that keeps `provisioning` on the enablement-gate side, ADR-0106
D1). This is acceptable on the shared `resolveCapabilities` path **only** under D4's idempotency-anchored-in-the-provider
guarantee; a non-idempotent or Stratt-anchored allocation record would fail §1.2 (see D4).

### D2 — Consumed by the build ACTUATOR, not the Intent — reusing the shipped resolve-inject; no new framework machinery

This is the load-bearing decision. The **builder** — the Actuator/Action that creates the subnet — is what needs the
CIDR to do its job, so it is the natural consumer: the build Actuator declares **`requires: [ipam]`**, exactly as
`opentofu-s3` declares `requires: [statestore]` (`estate/actuators/opentofu-s3.yaml`). At the build's Run dispatch the
shipped `resolveCapabilities` path invokes the provider's `ipam-resolve` Action (input = the allocation request) and
injects the `{cidr, vlanId}` handle into the build; the builder creates the subnet with it.

This **deliberately avoids** Intent-level multi-capability composition. The prior-art scan flagged that
`requires: [provisioning, ipam]` on an _Intent_ is schema-legal (the schemas use a `contains` constraint) but
**unbuilt** — the reconcile resolver ignores non-`provisioning` tokens and the resolve-inject path fires only for
Actuator-level `requires` at Run dispatch. Putting `ipam` on the **builder** sidesteps that gap: the Intent still just
`requires: [provisioning]`; allocation is the builder's concern, resolved by machinery that already ships. No new
framework code — the same reason EC2 was "one manifest line" once the framework existed. (The refinement where the
Intent _declares_ the request and it flows Intent→build rides the separate per-instance-parameterization follow-up
already booked in ADR-0110; not needed for this slice's value.)

### D3 — Allocation returns an injected HANDLE, never a `net.subnet` Facet write-back

The `ipam.output` handle is injected into the build (as statestore's backend handle is) and is **never written to the
`net.subnet` Facet**. The built subnet is still _observed_ into `net.subnet` by the Syncer exactly as today. So
allocation touches **neither** the ADR-0096 blocking union schema **nor** its cross-plugin co-fidelity test — NetBox
wearing two hats stays cleanly separated: **`ipam-resolve` is a resolve Action (allocate); the `net.subnet` Facet is
the Syncer's OBSERVE projection**. This resolves the sharpest collision the prior-art scan named (a fourth writer
against the blocking union) by construction — the resolve-inject shape means there is no new Facet writer at all.

### D4 — The netbox plugin becomes dual-verb (Syncer + `ipam-resolve` Action), per the ADR-0060 Crossplane precedent

The netbox plugin is OBSERVE-only today (`plugins/netbox`, `Class: SYNCER, Verbs: [OBSERVE]`, authoritative for
`net.subnet`/`net.vlan`). It gains `provides: [ipam]` (advertised in its Manifest, verified per ADR-0104 D1) and an
**idempotent** `netbox/ipam-resolve` Action — allocate-or-return-existing, keyed by the request identity, so a rebuild
returns the _same_ CIDR (allocation is not re-drawn each Run). **The idempotency is anchored IN THE PROVIDER, never in
Stratt (§1.2).** NetBox's `available-prefixes` API is not natively idempotent (it draws the next free child each call),
so the Action first **queries NetBox** for a prefix already tagged/described with the request identity and allocates a
new one only if absent — the durable `request → CIDR` mapping lives as a NetBox prefix tag, so **NetBox stays the sole
authoritative allocation record**. Stratt persists **no** allocation datum: it never becomes the address-space
system-of-record (the writable-CMDB non-goal). Dual-verb (Syncer + Action) is already charter-blessed
by the ADR-0060 Crossplane precedent (APPLY+OBSERVE; the plugin host gates on the OBSERVE _verb_, not `Class`) — cited,
not re-litigated. Per dependency-scout (D6) the Action talks **only** to NetBox's Apache-2.0 core REST API (available-
prefixes + VLAN allocation), never a Polyform-licensed add-on. §2.5: the NetBox API token is a CredentialRef name.

### D5 — The request carries enterprise scope, reusing the ADR-0059 topology; sovereignty _policy_ rides the PDP (follow-up)

The `ipam.input` scope fields (`region`/`availabilityZone`/`tenant`/`vlanGroup`) reference the **existing** ADR-0059
topology Entity kinds (`region`, `availability-zone` are already distinct kinds with `in-az`/`placed-in` Relations) —
they are **not** a new concept minted inside allocation logic (which would duplicate a shipped seam and violate ADR-0059
decision 3). NetBox allocates within that scope from its region/tenant-scoped prefixes and VLAN groups. The
**enforcement** of sovereignty as a _rule_ ("an EU-sovereign workload may only draw from EU-region prefixes") is a
**policy** concern: it rides Stratt's existing **PDP admission** seam (ADR-0073/0076) as a compile-time check over the
Intent's region/sovereignty scope — a booked follow-up, **never** a bespoke sovereignty check bolted onto the ipam
plugin (which would fragment the one-PDP model, §1.6). This ADR ships the _scoped request_; the _policy gate_ is the
follow-up. (Charter-guardian to adjudicate the enforcement mechanism.) Note: the estate-topology `region` kind is
distinct from the deployment-plane `Cell` (ADR-0044) — they must not be conflated.

**Honesty about what this slice actually delivers (§1.8) — the scope is builder-fixed, not per-workload, and not a
guarantee yet.** Because D2 fixes `requires: [ipam]` + its request on the **builder Actuator** (the Intent→request
param-flow is the deferred follow-up), the scope fields are **set at the builder, not driven by the consuming Intent's
region/tenant** this slice. So the scoped request does **not** provide per-workload sovereignty isolation yet, and —
until _both_ the Intent→request flow **and** the PDP admission (above) land — a mis-authored request naming the wrong
parent pool is **not blocked**. The scope fields are a _routing input_, not a security boundary; this ADR must not be
read as shipping sovereignty enforcement. That is precisely why the enforcement is a named follow-up, not a silent gap.

### D6 — Evergreen + supply-chain conditions (dependency-scout, binding)

- **Pin NetBox 4.5.x** (N-1 = 4.4.x); reference the image by **full digest**, never a floating tag.
- The netbox plugin's **contract tests run against both the pinned version and N-1 in CI** — NetBox offers no
  client-side API version negotiation, so REST/GraphQL schema drift between minors is _Stratt's_ to detect.
- Add NetBox to `task evergreen`'s tracked-version matrix with an **N-1 floor of the previous minor** (its cadence is
  fixed Apr/Aug/Dec, semver-disciplined; majors cannot skip a step).
- Verify cosign/SLSA/SBOM on the `netbox-community/netbox` image before hard-pinning; until confirmed, scan the pulled
  image with Trivy/Grype rather than trusting upstream attestation.
- The plugin README documents the **Apache-2.0-core-only** boundary (no Polyform add-ons) so a future contributor
  can't casually wire one in.

## Charter alignment

Upholds §1.5 (the builder targets the `ipam` class; NetBox is a swappable provider), §1.4 (NetBox + its Redis are
community-tier plugin breadth — NetBox's own Postgres + Redis are the tool's private substrate, structurally separate
from Stratt's spine and never shared with it; the spine stays Postgres/NATS/Temporal), §1.2 (Stratt drives + projects an external
IPAM SoR; the allocation handle is injected, the `net.subnet` Facet stays the Syncer's projection — Stratt never
becomes an authoritative IPAM), §2.5 (CredentialRef only), §1.7 (pinned + N-1, CI-gated). It **touches vocabulary** (a
new `ipam` capability class + `ipam-resolve` Action + `capabilities/ipam.*` Contracts), the **data model** (the
request/handle Contracts), a plugin gaining an **Action**, and the **policy** boundary (sovereignty ↔ PDP) — so it
carries the **highest review bar** (charter-guardian + vocabulary-linter). It does not approach a non-goal: the IPAM
SoR stays external (no writable CMDB); Stratt orchestrates allocation, it does not own the address space.

## Consequences

- **Positive.** Cross-substrate IP/VLAN allocation without Stratt becoming an IPAM. **Reuses the shipped resolve-inject
  machinery** (D2 — the builder-`requires:[ipam]` path is the statestore path, no new framework code). NetBox-as-
  allocator is cleanly separated from NetBox-as-observer (D3 — no new `net.subnet` writer, union untouched). Real NetBox
  in dev = a genuine cross-tool interaction (not a simulated call), seeded with a realistic enterprise topology.
- **Negative / trade-offs.** A new capability class + two Contracts + a new netbox Action (net-new plugin code — netbox
  has zero Action surface today). NetBox's lack of API version negotiation forces the CI two-version contract test.
  A new dev/kind container (NetBox + its private Redis). The declarative "Intent states the request" experience is
  deferred to the per-instance-parameterization follow-up (this slice's request lives on the builder).
- **Follow-ups.** (1) The Intent-declares-the-request param-flow (Intent → build), on the ADR-0110 per-instance thread —
  this is also what makes the D5 scope per-workload (and thus sovereignty-enforceable). (2) The PDP sovereignty-policy
  admission (D5). (3) An estate `capability-binding` for `ipam` once ≥2 providers exist (Nautobot / CAPI-IPAM). (4)
  Confirm the image attestation (D6) before hard-pin. (5) **Allocation-orphan lifecycle:** a permanently-failed build
  leaves a NetBox reservation with no built subnet. It is _observable_ (the netbox Syncer projects the reserved prefix
  as a `subnet` Entity → orphan-Finding territory, §1.8 — never hidden), but a reclaim/lifecycle path (release the
  reservation, or reconcile it to a retry) should be booked so stranded allocations don't silently accumulate.

## Alternatives considered

- **Put `requires: [ipam]` on the Intent (multi-capability Intent composition).** Rejected (D2): it needs unbuilt
  Intent-level resolution machinery for no added value this slice — the builder is the natural consumer of the CIDR,
  and the builder path already ships.
- **Have the allocate Action write the `net.subnet` Facet.** Rejected (D3): a fourth writer against the ADR-0096
  blocking union + its co-fidelity test. The handle-inject shape avoids a new Facet writer entirely.
- **Make `ipam` enablement-gate (like `provisioning`).** Rejected (D1, ADR-0106 D1): allocation is a low-rate coordinate
  handback, not a target-scoped Apply — resolve-inject is the honest shape.
- **Stratt as the IPAM (graph holds allocations authoritatively).** Rejected (§1.2): IPAM is a system of record; Stratt
  feeds SoRs, it does not become one. The allocation authority stays in NetBox (or any conformant provider).
- **A lightweight K8s-native allocator (Cluster-API IPAM) instead of NetBox.** Rejected for _this_ slice (research):
  the flat IP-pool allocators cannot model regions/AZs/sovereignty/VLANs, which the enterprise-simulation requirement
  demands; NetBox models them natively. (The CAPI IPAM contract remains a valid _sibling provider_ of the same `ipam`
  class later — the point of D1's provider-agnostic contract.)
