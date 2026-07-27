# ADR 0143 — The reach coordinate is observed, and it is a name: the vcenter Syncer projects `mgmt.address`

- **Status:** **Proposed** (2026-07-27, steward) — implemented for the vSphere half. Charter review by
  hand (this session's rules bar the subagent); §1.2/§1.8/§2.1/§2.4/§9 answered inline. **No new
  dependency. No new Named Kind, no new Facet namespace, no schema change** — this ADR adds a WRITER to
  a namespace that has specified it since ADR-0084.
- **Date:** 2026-07-27
- **Deciders:** steward
- **Charter sections:** §1.2 (projections, never a second truth — the coordinate is OBSERVED, never
  declared into existence), §2.1 (per-source write ownership), §2.4 (no implicit precedence between
  sources), §1.8 (never hide diagnosis — absence of a coordinate is a real, visible state), §9 (the
  reachability seam stays closed and never grows a device ontology)
- **Reconciles with:** ADR-0084 (the `mgmt.address` Facet — this builds the writer it specified),
  ADR-0060 (multi-source Facet ownership + declared authority — the mechanism this is the second real
  user of), ADR-0017 (provision→configure — the flow this unblocks), ADR-0113 (vSphere as a
  `provisioning` provider), ADR-0126 (machine credential + jump — the other half of reaching a node),
  ADR-0115 (vcenter read breadth). **Addresses ADDR-1** in
  [enterprise-readiness.md](../enterprise-readiness.md), producer #2 of three.

## Context

`mgmt.address` is the coordinate every connection Actuator resolves — `orchestrate.renderTarget` turns
it into `Target.Address`, and the ansible shim renders that as `ansible_host`. Its schema has named its
writers since ADR-0084:

> Written by Syncers/Normalizers with provenance (§1.2) — the declared-estate Syncer projects it from
> `estate/hosts`, **the vcenter Syncer from the VM IP, a build from its project-back**

**Only the first was ever built.** The vcenter Syncer's grant carries
`vm.config, vm.runtime, net.guest, net.subnet, storage.datastore, compute.pool, net.dvswitch` — no
`mgmt.address` — so the projection could not have been written even had the plugin emitted one. No
build projects it either.

The consequence is not cosmetic. The **only** working way to give a machine a reach coordinate is to
hand-write it in Git (`estate/hosts/managed-web.yaml: address: …`), and you cannot hand-write the
coordinate of a machine that does not exist until it is built. So **a provisioned machine, on any
substrate, could not be targeted** — ADR-0017's provision→configure flow was structurally open, and the
gap was invisible because the demo that proves configuration converges a **declared** node.

This is the fifth instance this session of one shape: a seam designed correctly, and a producer or
consumer never wired (cf. PRV-1's dropped placement, `Intent/DnsRecord` with no provider, `dns.fqdn`
with no consumer, `opentofu-subnet-build` advertised and never written, `environments` with no referent).

### Why the schema's own wording is the trap

ADR-0084 said "from the VM **IP**", and that framing is the thing to correct. A reach coordinate is
whatever the connection mechanism resolves, and its form is per-mechanism: `ansible_host` for ansible,
`service.namespace.svc.cluster.local:port` for Kubernetes, a GUID for AWX, a device id for Intune. The
schema was built for this — `address` is a free string, the seam is deliberately CLOSED to
`{address, port}` (§9), and the reference estate already carries a **name**
(`managed-web.stratt.svc.cluster.local`), not an address.

An IP is a **fact**, worth observing and already projected on `net.guest` for diagnosis. It is not the
reach path: estates moved off addressing because addresses change and names do not.

## Decision

### D1 — The vcenter Syncer projects `mgmt.address`, and it is a NAME first

`normalizeVM` emits `mgmt.address` from what vCenter observed, in this order:

1. `guest.hostName` when it is **dotted** (an FQDN), lowercased — DNS is case-insensitive and the graph
   should not be.
2. otherwise `guest.ipAddress`.
3. otherwise **nothing**.

A **bare** hostname is deliberately skipped: whether `web-01` resolves depends on search domains we
neither control nor observe, so promising it as a reach coordinate would be guessing. Dotted ⇒ usable;
undotted ⇒ prefer the address we know routes.

**"Otherwise nothing" is a real outcome, not a failure to handle.** A VM with no tools, or mid-boot,
genuinely has no known coordinate. Projecting a guess would make an unreachable host look reachable and
fail the next Run far from the cause (§1.8). The absence is observable: the Entity is in the graph with
`net.guest` and without `mgmt.address` — precisely "built, not yet reachable."

### D2 — This is field precedence within one source, which §2.4 does not govern

D1 orders **one source's own observations of one Entity**. §2.4 forbids implicit precedence between
**claims and sources** — that is a different thing, and it is untouched: cross-source resolution stays
ADR-0060's declared-authority mechanism. The distinction is stated here because "precedence" appearing
in a design is worth a second look, and this one is inside a single writer's own read of its own system.

### D3 — `mgmt.address` becomes multi-source; NO authority is declared, deliberately

The grant addition makes `declared` and `vcenter` both registered owners. ADR-0060 explicitly permits
this — it dropped the per-namespace lock naming _"vSphere and a cloud Syncer would too"_ — and the store
has supported it since migration `00035`.

Neither is marked authoritative, and that is a decision rather than an omission. The two write
**disjoint Entities** in practice: `declared` writes CaC-declared hosts, `vcenter` writes observed VMs.
Where they genuinely correlate onto one Entity — a CaC host that is also a vSphere VM, reachable via the
shared `dns.fqdn` scheme — the fail-safe read **omits** the value and raises an ownership-contention
Finding rather than picking one. That is the correct outcome, so there is nothing to declare.

### D4 — Out of scope: the other two producers

ADDR-1 names three producers. This ADR builds the **observed** one. The **derived** producer (a name
composed from the estate's own naming plus an environment's DNS zone) waits on
[ADR-0142](0142-environments-are-declared-not-just-referenced.md) D4, which is a live §2.4 question. The
**registered** producer (`Intent/DnsRecord`, which ships as a kind with a schema and reconcile support
and no provider) is its own slice.

## Charter alignment

- **§1.2** — the coordinate is OBSERVED and provenance-stamped, never declared into existence. Nothing
  becomes a second truth: vCenter remains the SoR for what it reports.
- **§2.1 / §2.4** — a second registered owner, no silent precedence; contention surfaces as a Finding.
- **§1.8** — absence of a coordinate is visible rather than papered over with a guess.
- **§9** — no schema change; the closed `{address, port}` seam is unchanged.

## Consequences

- **Positive:** a vSphere-provisioned VM is targetable for the first time, with no estate change and no
  hand-written address. ADR-0060's declared-authority mechanism gains a second real user. The estate can
  stop teaching hard-coded addresses by example.
- **Negative / trade-offs:** the FQDN-vs-IP ordering is a judgement that will occasionally pick an IP
  where a name existed but was bare. The `net.guest` facet always shows both, so the choice is auditable
  — but it is not recorded in `mgmt.address` itself, because the schema is closed and should stay so.
- **Follow-ups:** the same producer for **awsec2** (private DNS name / address) and for a K8s Compute
  provider (service name), so the property holds on every substrate rather than one · `GetFacetOwner`
  ([registry.go:46](../../core/internal/graph/registry.go#L46)) still does `QueryRow` over a
  now-multi-row table and returns an arbitrary owner; benign for this change (both owners are
  `syncer`-kind, so the compiler's read-only branch is taken either way) but it is a latent §2.4 smell
  worth closing · a View of "built, not yet reachable" Entities would make D1's honest absence
  actionable rather than merely visible.
- **Why nothing caught this, which is the more useful follow-up.** Two structural reasons, both open:
  (a) **nothing asserts that the namespaces a plugin EMITS are a subset of its grant.** A plugin can
  emit a Facet its grant omits and the write is dropped at `govern` — so the projection and the
  authority to write it are two facts nothing reconciles, exactly the shape ADR-0126 D1 found between
  an authorized credential and the file actually read. A conformance check belongs in
  `sdk/mockstratt`, where every plugin already runs a suite.
  (b) **the vcenter Syncer's grant is boot-wired in `cmd/strattd/main.go`, not CaC**, so it is
  unreviewable from Git and untestable from the estate. ansible, script and cert-issuer have already
  migrated off their boot-env blocks to CaC declarations (ADR-0103, ADR-0140 D2); the vcenter Syncer
  has not, and this ADR had to edit Go to grant a Facet namespace — which is the thing ADR-0140's
  "a grant that lives in Go is unanswerable from Git" already argued against.

## Alternatives considered

- **Project the IP only, as ADR-0084's wording said** — rejected: it hard-codes the least stable
  coordinate as the primary one and contradicts how estates actually reach hosts.
- **Project a bare hostname when that is all vCenter reports** — rejected: resolvability would depend on
  unobserved search domains, which is a guess dressed as a fact.
- **Have the BUILD project the coordinate instead of the Syncer** — rejected for this slice: the build
  knows the VM's identity, not its eventual address, and ADR-0113 D3 deliberately keeps build output
  identity-only so the build never becomes a second writer. Observation is the right producer.
- **Declare `vcenter` authoritative for `mgmt.address`** — rejected: it would silently outrank a
  CaC-declared address on the one Entity where both apply, which is exactly the contention a Finding
  should surface (§2.4).
