# ADR 0113 — vSphere as a `provisioning` provider: the vcenter plugin gains a build verb (VM + DVPortgroup)

- **Status:** **Proposed** (2026-07-24, steward) — vocabulary-linter **CLEAN**; charter-guardian **PASS after fixes**
  (F1: D2 now states that landing vcenter requires scoping the sibling providers or adding explicit bindings, else the
  shared/default environment goes AMBIGUOUS on merge; F2: the stale `ScopeToEnvironment` doc-comment update is booked as
  slice-1 work; F3: `Actuator`/`CapabilityBinding` gain a `ScopedEnvironments()` (the `EnvScoped` interface) rather than
  a one-off field read). **D4 amended during slice 2** (steward): the portgroup VLAN composes ipam via an EXPLICIT
  `netbox/ipam-resolve` Workflow Step + step-output binding, **not** resolve-inject — resolve-inject is workspace-shaped
  and Actuator-`Apply`-only, and a per-build VLAN allocation is more legibly a visible Step (§1.8). vcenter drops
  `requires: [ipam]`.
- **Date:** 2026-07-24
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.5 (an Intent targets the `provisioning` _class_; vcenter is a swappable provider chosen
  by an estate binding — never named by the Intent) · §1.2 (vCenter is the SoR; the graph projects it via the Syncer +
  identity-only build outputs — Stratt does not become a second truth) · §2.4 (≥2 providers of a kind → explicit
  binding, never a silent tiebreak; environment scope is additive, not precedence) · §2.5 (guest-customization secrets
  resolve at the plugin's own broker, never through the core) · §1.4 (govmomi/vcsim are plugin-tier; the spine is
  untouched). **This realizes [ADR-0059](0059-network-topology-primitives.md)'s booked `vsphere-network` build-Actuator
  slot and swaps [ADR-0058](0058-provisioning-from-intent.md)'s named `vsphere` builder onto the
  [ADR-0110](0110-provisioning-class-reach-path.md) reach-path** (ADR-0058's `builder:` field is **superseded** — the
  provider binds via `requires: [provisioning]`, never `builder: vsphere/create-vm`). Mirrors **ADR-0107** (EC2 as
  provisioning provider #1, enablement-gate) as provider #N; composes **ADR-0111** (ipam) for the portgroup VLAN via an
  explicit `netbox/ipam-resolve` Workflow Step + step-output binding (D4, corrected mechanism); follows **ADR-0112 D5** (build output identity-only,
  keyed by the Syncer's own scheme) and **ADR-0017** (provision→configure identity projection); extends **ADR-0057**
  (environment scope) to the provisioning provider-selection path; builds on **ADR-0007** (the shipped vcenter Syncer +
  vcsim) and **ADR-0060** (the dual-verb OBSERVE+INVOKE plugin shape, as netbox `ipam` used). Reconciles with
  **ADR-0095/0096** (the Syncer owns `net.subnet`; §2.5 credential hygiene) and **ADR-0104/0106** (capability
  verification + the enablement-gate reach-path guardrail).

## Context

The AAP-replacement PoC needs the VMware half: **read** vSphere config **and** **project** Intent into vSphere. A
prior-art scan established that the **read half already ships** — `plugins/vcenter/` is a working govmomi Syncer
(OBSERVE-only) that reads VMs/hosts/portgroups from **vcsim** (already in the dev stack) and projects `vm`/`host`/
`subnet` Entities with `vm.config`/`vm.runtime`/`net.guest`/`net.subnet` Facets, identity schemes `vcenter.uuid`/
`vcenter.host.uuid`/`vcenter.network.moref` (ADR-0007). The real gap is **projection**: a vSphere `provisioning`
provider that creates VMs and DVS portgroups — the slot ADR-0059 explicitly booked (`vsphere-network`) and the swap
point ADR-0058 named (`builder: vsphere`), now reachable only through the ADR-0110 capability reach-path.

The chosen reach (steward) is **VM + network/VLAN**: vSphere provisions **Compute** (VMs) and **Subnet** (DVS
portgroups), with the portgroup **VLAN allocated via the netbox `ipam` capability** (ADR-0111) — exercising the full
availability-zone / region / sovereignty / VLAN enterprise topology the PoC must simulate.

Two things a scan flagged and this ADR must not gloss: (1) both `Compute` (awsec2) and `Subnet` (opentofu) **already
have bound providers**, and the ADR-0110 resolver picks **one provider per (capability, IntentKind)** — so vSphere must
_coexist_, not replace, which is not wired today; and (2) vcsim was validated by ADR-0007 for the _read_ path only —
its _write_ fidelity (CreateVM, DVPortgroup, power ops) was an open question. Both are settled below.

## Decision

### D1 — Extend the vcenter plugin to a dual-verb Actuator (do not add a second plugin)

`plugins/vcenter/` gains `VERB_INVOKE` and two build Actions — `vcenter/create-vm` (Compute) and
`vcenter/create-portgroup` (Subnet) — alongside its existing `VERB_OBSERVE`. This is the **ADR-0060 dual-verb shape**
netbox already uses for `ipam` (`plugins/netbox/ipam.go`, OBSERVE+INVOKE) and awsec2 uses for its resource Actions.
The estate declaration `estate/actuators/vcenter.yaml` carries `provides: [provisioning]`, `requires: [ipam]`, and
`provisions: {Compute: vsphere-vm-build, Subnet: vsphere-subnet-build}` (ADR-0110 D3 form, mirroring
`estate/actuators/{awsec2,opentofu-network}.yaml`).

**Why one plugin, not a parallel `plugins/vsphere/`:** the Syncer's OBSERVE identity schemes and the build output's
identity schemes must correlate exactly (D3). Keeping both verbs in **one module** makes that correlation _structural_
— there is no cross-plugin scheme to drift. govmomi (Apache-2.0, de-facto-standard, pinned `v0.55.1`) is already this
module's dependency; a second plugin would duplicate the connection code and re-open the correlation risk.
`vcenter`/`portgroup`/`subnet` are plugin/tool identifiers (a Source name and vendor nouns), not banned core-model
vocabulary (§2) — `subnet` remains the shared graph kind (ADR-0059), never a `vsphere.subnet` parallel.

### D2 — Multi-substrate coexistence via environment-scoped provider selection (extends ADR-0057)

vSphere must coexist with EC2/opentofu, not replace them. The resolver `capability.Resolve` is pure and already
consumes the caller's _in-scope_ provider + binding snapshot — but today the assembler
(`verifiedProvisioningProviders` / `resolveProvisioning`) reads **all** verified providers and **all** bindings,
unscoped: `ScopeToEnvironment` (ADR-0057) filters only Assignment/Trigger/Baseline in v1, even though `Actuator` and
`CapabilityBinding` already carry an `Environments` field. So two Compute providers visible at once → AMBIGUOUS, and a
single global binding could only force _all_ Compute onto one substrate.

**This ADR extends ADR-0057's scope to the provisioning provider-selection path**: `verifiedProvisioningProviders` and
the binding set are filtered by `store.ActiveEnvironment()` via the existing `types.InScope(x.Environments, env)`, so an
**environment is the substrate/sovereignty boundary**. A `vsphere-dc`-scoped daemon (or Cell) resolves `Compute→vcenter`
/ `Subnet→vcenter`; an `aws`-scoped daemon keeps `Compute→awsec2` / `Subnet→opentofu`. Each environment resolves
independently over its own in-scope providers; ambiguity _within_ one environment still fails closed (§2.4 — this is
additive **scope**, never a precedence/last-writer tiebreak). This is a small, contained core change (two list reads
gain an `InScope` filter; the `Environments` fields and helper already exist) and the honest enterprise model the PoC's
sovereignty story requires. An unscoped dev daemon (`env == ""`) still sees every provider — so demonstrating both
substrates at once means running scoped environments, exactly the Cells posture already shipped.

**Merge condition (charter-guardian F1).** Every shipped provisioning provider — `awsec2`, `crossplane`,
`opentofu-network` — is currently **unscoped** (no `environments:` field ⇒ in _every_ environment). So landing a
`vcenter` scoped to `[vsphere-dc]` makes the `vsphere-dc` environment see `awsec2` + `crossplane` + `vcenter` for
`Compute` → **AMBIGUOUS** (correctly fail-closed and observable, §1.8/§2.4 — never a silent pick). Landing vcenter
therefore REQUIRES one of: (a) scope the sibling providers to their environments (`estate/actuators/{awsec2,crossplane,
opentofu-network}.yaml` gain `environments:`), or (b) an explicit `CapabilityBinding` disambiguating each affected kind.
This slice takes path (a) for the vsphere environment's kinds and keeps the shared/default environment resolvable.
**Implementation notes:** `Actuator` and `CapabilityBinding` gain a `ScopedEnvironments()` method so they join the
`EnvScoped` structural contract (F3) instead of a one-off field read; and the `ScopeToEnvironment` doc-comment — which
still claims only Assignment/Trigger/Baseline are env-scoped in v1 — is corrected when this lands (F2).

### D3 — Build output is identity-only, keyed by the Syncer's own schemes (ADR-0112 D5 / ADR-0017)

The terminal `InvokeResult` of each build carries an `ObservedEntity` with **only `{kind, identityKeys, labels}` plus
the Run-provenance overlay** (`projectKind`, `projectLabels`, the `stratt.intent/instance` correlation label) — **never
a Facet**. `vcenter/create-vm` keys by `vcenter.uuid`; `vcenter/create-portgroup` keys by `vcenter.network.moref` — the
_same_ schemes the vcenter Syncer already OBSERVEs on (ADR-0007). So `vm.config`/`vm.runtime`/`net.subnet` remain the
**Syncer's OBSERVE projection** of the real vCenter object; the build only creates the Entity + correlation, then the
next sync fills the Facets. Because one module owns both verbs (D1), the co-owned-Entity correlation is guaranteed — no
duplicate Entity, no fourth `net.subnet` writer, so **ADR-0096's `net.subnet` closed-union + blocking co-fidelity test
are untouched**.

### D4 — The Subnet build composes ipam for the portgroup VLAN via an EXPLICIT allocation Step (not resolve-inject)

**Corrected mechanism (supersedes this ADR's original resolve-inject framing).** The build implementation revealed that
resolve-inject (ADR-0105/0111) is **workspace-shaped and Actuator-`Apply`-only**: the core's `resolveCapabilities`
assembles the resolve _input_ as `{workspace}` and injects only onto `Plan`/`Apply` (`InvokeRequest` carries no
`resolved_capabilities`). That model fits an **ambient backend** (statestore — one backend keyed by a workspace,
injected transparently). It fits **ipam poorly**: an allocation's input is **per-build** (`{key, role|pool, size,
vlanGroup, region, …}`), not a workspace, and a VLAN allocation is a discrete act that §1.8 wants **visible**, not
hidden in an injected handle. And `vcenter/create-portgroup` is an INVOKE **Action**, which the resolve-inject path
does not serve.

So the `vsphere-subnet-build` Workflow composes the two Actions **explicitly**, using the shipped step-output binding
(ADR-0031): step 1 `netbox/ipam-resolve` allocates the prefix + VLAN and emits `capabilities/ipam.output`
(`{cidr, vlanId, gateway}`); step 2 `vcenter/create-portgroup` reads `vlanId` (and `cidr`) via
`{{.steps.allocate-vlan.outputs.vlanId}}` — the template engine preserves the native integer type — and sets the
DVPortgroup VLAN. This is the **general composition primitive** (any capability Action composes this way), it keeps the
allocation a **legible descent Step** (§1.8), and NetBox stays the sole allocation SoR (ADR-0111 D4, §1.2 — Stratt
persists no allocation record). So the vcenter Actuator does **NOT** declare `requires: [ipam]` (that is the ambient
resolve-inject gate, which this build does not use); the dependency is the Workflow's `needs: [allocate-vlan]` edge,
observable in the DAG. (`vcenter/create-vm` needs no allocation; a VM is placed on a portgroup a prior Subnet build
already created.)

**Reconciliation (ADR-0112 follow-up #7).** This deliberately does **not** generalize resolve-inject to the Invoke
verb. Generalizing the resolve-_input_ assembly so ambient-backend capabilities can also be consumed on the Action verb
is a real, separate port evolution — it earns its own ADR when an _ambient_ capability first needs the Action verb. For
a per-build allocation like ipam, explicit-Step composition is the correct model, not a stopgap.

### D5 — vcsim write-fidelity is PROVEN; real-vCenter smoke test is the deferred validation

The open question ADR-0007 left (vcsim validated for reads only) is settled by a slice-0 spike against the in-process
simulator on the pinned **govmomi `v0.55.1`**: `CreateVM_Task`, `PowerOn` (→ `poweredOn`), and `AddDVPortgroup` with a
VLAN all succeed and **persist**, and the Syncer's `enumerate` path then OBSERVEs both the created VM (by `vcenter.uuid`)
and the portgroup (by `vcenter.network.moref`) — the read↔build loop closes in-process. So vcsim is a sufficient dev/CI
_write_ backend for this slice, not just reads. **Named caveat (unchanged from ADR-0007):** vcsim simulates a _subset_
of the API with no published coverage matrix, so the **live real-vCenter (or Broadcom HOL) smoke test remains the
deferred deployment validation** for guest customization + power-state edge cases vcsim may not model. govmomi is
**plugin-tier only** and pinned exactly (`v0.55.1`) — 0.x semver is loose, so bumps are integration-tested against
vcsim, never taken blindly (dependency-scout: RECOMMEND).

### D6 — Credential hygiene: guest-customization secrets never cross the core (§2.5 / ADR-0095)

Any secret a build needs (guest-customization admin password, injected SSH key) resolves at the **plugin's own broker**
at pod spawn from a `CredentialRef` — mirroring ADR-0095's `ImportKeyPair`-only posture. The build's `InvokeResult`
returns identity + labels only (D3); it **never** returns secret material through the core. The operator grant widens
only to permit the two new Actions on the vcenter channel — no new facet/label ownership.

## Charter alignment

Upholds §1.5 (the Compute/Subnet Intents target `provisioning`; vcenter is a binding-selected provider), §1.2 (vCenter
is the SoR, the graph projects it — the build writes identity only, the Syncer owns the Facets), §2.4 (≥2 providers →
explicit binding; environment scope is additive, D2), §2.5 (D6), §1.4 (govmomi/vcsim plugin-tier; spine untouched). It
**touches the data model** (a new estate Actuator + capability-binding + two Action Contracts) and a **core
reconcile-path change** (D2 environment scoping of provider selection) — highest review bar (charter-guardian +
vocabulary-linter). It does **not** touch the sovereign plugin port proto: the portgroup VLAN composes ipam via an
explicit Workflow Step + step-output binding (D4), not resolve-inject.

## Consequences

- **Positive.** The VMware half becomes real: Intent → vcenter builds a VM / a VLAN-tagged portgroup in vCenter, and
  the existing Syncer observes it straight back (identity-correlated) — the read↔build loop the charter's descent
  discipline (§1.8) wants, proven in-process. It lands **ipam's second consumer**, realizes ADR-0059's booked
  `vsphere-network` slot, and delivers the AZ/region/sovereignty/VLAN enterprise topology via **environment-scoped
  provider selection** — a genuinely useful generalization (D2) that any future multi-substrate estate reuses.
- **Negative / trade-offs.** D2 is a real (if small) core reconcile-path change extending ADR-0057's scope model — it
  must preserve fail-closed within an environment. vcsim write-fidelity is proven but a _subset_; the live real-vCenter
  smoke test is deferred (D5). A dual-verb vcenter plugin widens that plugin's blast radius from pure-read to
  read+build (mitigated: identity-only output D3, narrow grant D6).
- **Follow-ups.** (1) The live real-vCenter (or HOL) smoke test — guest customization + power transitions vcsim may not
  model (D5). (2) The Actuator-builder Workflow-Step form (the ADR-0112 follow-up #7 open question — an Actuator's apply
  is workspace-scoped; `vsphere-vm-build`/`vsphere-subnet-build` need either a synthetic anchor View or a targetless
  `vcenter/apply`-style Action wrapper; reuse whatever ADR-0112's follow-up settles). (3) ~~The
  enterprise-topology dev seed~~ **DONE** — `plugins/vcenter/cmd/vsphere-seed` + `task dev:vsphere:bootstrap` shape
  vcsim into multi-region datacenters / AZ clusters / sovereignty tenant folders / VLAN portgroups, idempotently.
  (4) Ansible **configure**
  Step on the built VM — the §5.1 provision→configure close, shared with ADR-0112. (5) A PDP sovereignty gate on the
  region/tenant selection (shared with ADR-0111 D5). (6) **Eliminate the ipam-resolve contract mirror.** Invoking the
  capability-resolve Action `netbox/ipam-resolve` as an explicit Workflow Step required a Workflow-facing
  `actions/netbox/ipam-resolve.{input,output}` Contract surface mirroring the class `capabilities/ipam.{input,output}`
  (bound by a co-fidelity test) — because the Workflow-step validator resolves an Action's Contract by the
  `actions/<name>` convention, while a capability-resolve Action declares the class contract. A follow-up could teach
  the validator to resolve a capability-resolve Action's Contract from the estate's action→class mapping, removing the
  per-provider mirror.

## Alternatives considered

- **A new `plugins/vsphere/` provisioning plugin separate from the vcenter Syncer.** Rejected (D1): it would split the
  OBSERVE and build identity schemes across two modules, re-opening the ADR-0112-D5 correlation risk that one module
  makes structural; and it duplicates govmomi connection code for no benefit.
- **A global capability-binding selecting one Compute/Subnet provider estate-wide.** Rejected (D2): it forces _all_
  Compute (or Subnet) onto a single substrate — the opposite of the multi-substrate/sovereignty PoC. Environment scope
  is the charter-consistent boundary (ADR-0057), not a global switch.
- **Write `net.subnet`/`vm.config` Facets from the build output.** Rejected (D3): it makes the build a second/fourth
  Facet writer, colliding with the Syncer's OBSERVE and ADR-0096's closed-union co-fidelity test. Identity-only + let
  the Syncer observe is the shipped ADR-0112 D5 pattern.
- **Allocate the portgroup VLAN inside the plugin (or in Stratt).** Rejected (D4): VLAN allocation is the `ipam`
  capability's job, anchored in NetBox (ADR-0111) — a plugin-local or Stratt-side allocator would be a second
  allocation truth (§1.2). Compose ipam, don't reinvent it.
- **Keep ADR-0058's `builder: vsphere/create-vm`.** Rejected: that field is superseded by the ADR-0110 reach-path; the
  Intent targets the `provisioning` class and the binding selects vcenter — the whole point of the capability framework.
