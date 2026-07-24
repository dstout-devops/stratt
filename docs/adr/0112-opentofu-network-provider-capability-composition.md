# ADR 0112 — OpenTofu as the AWS network `provisioning` provider, composing statestore + ipam

- **Status:** **Proposed** (2026-07-24, steward) — vocabulary-linter **CLEAN**; charter-guardian **PASS after fixes**
  (D4: provider `required_providers` + committed `.terraform.lock.hcl` is the real §7.3 pin, module-source deferred;
  D2: the closed-`output`-Contract §2.5 invariant named + a CI lint booked; D5: the `aws.subnetId` identity-scheme
  correlation stated; D6: lands D5's opentofu recommended-default, superseding the never-shipped interim awsec2 binding).
- **Date:** 2026-07-24
- **Deciders:** Project steward (dstout)
- **Charter sections:** §5.1 (the canonical provision→configure flow IS `tofu apply` (Gate on plan) → outputs
  Contract → Normalizer projects Entities → ansible Step) · §5.2 (`tofu plan` on cron _is_ drift detection) · §3
  (OpenTofu preferred; Contracts are data) · §1.5 (target the capability _class_; providers swap by binding) · §1.2
  (Stratt drives + projects; the tofu tool state is the SoR, the graph is the projection). **This is [ADR-0110](0110-provisioning-class-reach-path.md)
  D5 / Follow-up #1** — the named "OpenTofu network provider… first explicit capability-binding rebinding subnets off
  Crossplane." Composes **ADR-0105** (statestore) + **ADR-0111** (ipam) on one build Actuator; builds on **ADR-0016**
  (OpenTofu Actuator), **ADR-0017** (tofu outputs → Entities), **ADR-0019** (tofu-plan Baseline drift), **ADR-0093**
  (floci real-EC2 backend). Reconciles with **ADR-0095/0096** (awsec2 Syncer owns `net.subnet`) and **ADR-0046**
  (additive-only plugin-port evolution — this ADR adds a proto field, D2).

## Context

Real teams provision AWS network estate with **Terraform/OpenTofu** (or CDK/CFN) — not imperative `create-subnet` API
calls. An AAP-replacement PoC that provisioned via direct API calls would read as a toy, and it would contradict the
charter's own canonical flow (§5.1/§5.2/§3, all tofu). So the AWS network slice leads with **OpenTofu**: a module owns
the VPC/subnet/SG/route-table/IGW stack; Stratt allocates the CIDR (ipam), holds the state (statestore), gates the
plan, and projects the result. This is the charter's division of labor, and it dissolves the "new-ground" heaviness an
earlier scan feared (`Intent/Vpc`, route-table Entities) — the _module_ owns that, not Stratt.

It is also the convergence slice: the OpenTofu build Actuator declares **`requires: [statestore, ipam]`**, exercising
provisioning (ADR-0110) + statestore (ADR-0105, finally dogfooded live) + ipam (ADR-0111) together, against **floci**
— whose canonical docs confirm **real SSH-able Docker-backed instances _and_ full network write** (`CreateVpc`/
`CreateSubnet`/`CreateSecurityGroup`/`CreateRouteTable`/IGW/NAT), so the whole module runs against a genuine (if local)
AWS surface with real hosts.

A prior-art scan pre-authorized the direction (it is ADR-0110 D5's literal follow-up) but caught **two mechanics I must
not gloss**: the capability-handle payload path is statestore-shaped (not generic), and the tofu module has no delivery
story. Both are addressed below.

## Decision

### D1 — An OpenTofu network Actuator provides `provisioning` for Subnet, requiring statestore + ipam

A `opentofu-network` Actuator declaration (`provides: [provisioning]`, `provisions: {Subnet: opentofu-subnet-build}`,
`requires: [statestore, ipam]`) — mirroring the `estate/actuators/{crossplane,awsec2}.yaml` `provisions:` form
(ADR-0110 D3). Its build Workflow runs an AWS network module via the shipped OpenTofu Actuator (ADR-0016) with the S3
statestore backend (ADR-0105) and the ipam-allocated CIDR injected as a module var. This is additive — the existing
`opentofu`/`opentofu-s3` Actuators are untouched (ADR-0105 D4).

### D2 — Generalize the injected capability handle to carry the resolve output (the real core work)

Today `resolveCapabilities` decodes every resolve-Action output into a **statestore-shaped** struct
(`{backend, config, credentialRef}` → `pluginhost.CapabilityHandle{Kind, Config, CredentialRef}`,
`orchestrate.go`/`host.go`). `ipam.output` (`{cidr, vlanId, gateway, credentialRef}`) has no `backend`/`config` keys,
so that decode **silently drops the CIDR**. ADR-0111 D2's "reuse `resolveCapabilities` with no new framework code" is
true for the _gate/invoke/validate_ loop (which is generic) but **false for the handle _payload_**. So:

- **The injected handle carries the validated resolve-output bytes verbatim** — an **additive** `bytes output` field on
  the proto `CapabilityHandle` (ADR-0046 additive-only), so _any_ capability's differently-shaped output reaches the
  consumer intact. The existing typed `{Kind, Config, CredentialRef}` fields stay (statestore back-compat); new
  consumers decode `output` against their own class Contract. The core validates the output against
  `capabilities/<class>.output` before injecting (as it does today), so the bytes are contract-checked.
- This is the **enabler for every future non-statestore capability** (ipam is the first; artifactstore, eventbus
  follow) — a generalization, not a one-off. It is new core + proto code, named honestly, not "purely additive."
- **Standing invariant the generic channel now depends on (§2.5).** The typed `{Kind, Config, CredentialRef}` fields
  gave a _structural_ shape-guard; a raw `output` does not, so secret-safety becomes purely contractual. Therefore
  **every `capabilities/<class>.output` Contract MUST stay `additionalProperties: false` and carry credentials only as
  a CredentialRef _name_, never inline material** — the core `ValidateNamed`s the bytes against that closed Contract
  before injection, so no material can ride through (verified today for `ipam.output`/`statestore.output`). Follow-up:
  a CI lint asserting every `capabilities/*.output.schema.json` is closed + name-only, so the guard can't silently rot.

### D3 — The opentofu plugin reads the ipam handle and renders the CIDR as a TF var

The opentofu plugin already reads `resolved_capabilities["statestore"]` (renders `-backend-config`) and already writes
`Vars` to a `-var-file` (`plugins/opentofu/server.go`). It gains a symmetric path: read `resolved_capabilities["ipam"]`,
decode `{cidr, vlanId, gateway}` against `capabilities/ipam.output`, and add them to the var-file (e.g.
`stratt_ipam_cidr`, `stratt_ipam_vlan_id`) so the module references `var.stratt_ipam_cidr`. New plugin consumption
code — a different injection mechanism (module var) than the backend-config path, not a copy of it.

### D4 — The AWS network module: where it lives, and the (accepted) no-versioning posture

The module is a subdirectory under the opentofu plugin's `ModuleRoot` (a deploy asset, mounted like the other dev
assets) — there is **no module registry today** (`plugins/opentofu/tofu.go`), and this slice does **not** invent one.
The module `backend "s3" {}` (filled by statestore) and consumes `var.stratt_ipam_cidr` to create a VPC + subnet
(+ SG/route-table/IGW as the enterprise stack needs), tagged `stratt:managed`.

**The real executable-content pin is the _provider_, not the module source (§7.3/§1.7).** As a first-party bundled
asset, the module inherits the opentofu plugin image's digest pin, so deferring remote _module-source_ pinning
(OCI/git-ref) to a follow-up is genuinely acceptable — **but** an unpinned `aws`/floci provider floats on every
`tofu init`, which is a live §7.3 pinned-digest hole _and_ a §1.7 evergreen hole (you cannot assert N-1 on a floating
provider). **Binding condition:** the module MUST ship a `required_providers` version constraint **and** a committed
`.terraform.lock.hcl` (provider hash pins), and reference **no** unpinned remote module sources (`registry`/`git::`)
until the module-source follow-up lands. Provider-lock-at-ship is the control; module-source pinning is the deferred
convenience.

### D5 — `net.subnet` stays the awsec2 Syncer's Facet; tofu output creates the Entity by identity only

Per ADR-0017, the tofu `stratt_entities` output carries **only `{kind, identityKeys, labels}` — no Facets**. So the
built subnet's `net.subnet` Facet (`cidr`, `availabilityZone`, `vpcId`, `state`) is **not** written by tofu; it is the
**awsec2 Syncer's OBSERVE projection** of the real floci subnet — exactly as it is today for awsec2-native subnets, and
exactly the separation ADR-0111 D3 relied on for ipam. So the **ADR-0096 `net.subnet` closed-union + its blocking
co-fidelity test are untouched** (no fourth Facet writer). The three roles at `net.subnet` are explicit: tofu output
(Entity identity/labels) / awsec2 Syncer (the Facet) / Crossplane (its own claim-shaped subnets) — a co-owned union,
not a collision.

**Correlation constraint (§1.2) — the two roles must key the SAME Entity.** For the tofu identity-only Entity and the
awsec2 Syncer's Facet to co-own **one** subnet Entity (not split into two), the module's `stratt_entities` output MUST
emit the subnet under the **same identity scheme the awsec2 Syncer uses — `aws.subnetId` (ADR-0096)**. A scheme mismatch
would silently produce a duplicate Entity and break the co-owned-union claim; so the module reads back the created
subnet's AWS id and emits it as the identity key.

### D6 — The first live explicit `capability-binding` selects opentofu for Subnet (the Crossplane demotion)

With `opentofu-network` advertising `provisions: {Subnet: …}`, **Subnet has ≥2 verified providers** (opentofu +
crossplane) → auto-bind fails closed (§2.4), so this slice writes the **first live `estate/capability-bindings/` entry**
selecting `opentofu` for `Subnet`. This lands ADR-0110 D5's **opentofu recommended-default end-state** — and thereby
**supersedes** D5's table's _interim_ awsec2 subnet binding, which was never shipped (`estate/actuators/awsec2.yaml`
deliberately does not advertise `provisions: {Subnet}` — comment: "Subnet stays on Crossplane until then"). So subnet
demotion goes crossplane → **opentofu** directly, not crossplane → awsec2 → opentofu. `net-vlan`'s Crossplane binding is
**untouched** (the acknowledged sole-VLAN-provider exception); `web-fleet`/`app-tier` (Compute) stay on awsec2.

### D7 — Drift reuses the shipped tofu-plan Baseline; floci coverage is doc-confirmed, live-proof deferred

Drift is a Baseline over the network workspace running `tofu plan` (ADR-0019, live end-to-end) — no new mechanism.
floci's docs confirm real SSH-able instances + full network write (VPC/subnet/SG/route-table/IGW/NAT), and tofu's `aws`
provider points at floci's endpoint (the standard "tofu against localstack" pattern, `endpoints { ec2 = … }` + dummy
region/creds). The **live run** (tofu apply landing real VPC/subnet/instance on floci, SSH into it) is the deferred
deployment validation — and, given the DeepWiki-vs-official-docs conflict on floci's instance realness, it is the
**definitive settle**, booked as this slice's live proof.

## Charter alignment

Upholds §5.1/§5.2/§3 (tofu is the canonical provisioning + drift path — this ADR _stops contradicting_ it), §1.5 (the
Subnet Intent targets `provisioning`; opentofu is a swappable provider chosen by the binding), §1.2 (tofu state is the
tool SoR; the graph projects it via the Syncer + tofu-outputs; Stratt does not become the IaC state store — statestore
externalizes it to S3), §1.4 (opentofu/NetBox/floci are all plugin-tier; the spine is untouched), §2.4 (≥2 providers →
explicit binding, never a tiebreak). It **touches the sovereign plugin port** (an additive `CapabilityHandle.output`
field, ADR-0046 additive-only — proto:breaking gate must pass) and the **data model** (a new estate Actuator +
capability-binding) — highest review bar (charter-guardian + vocabulary-linter).

## Consequences

- **Positive.** The AWS network slice is _realistic_ (tofu, not API calls) and _charter-canonical_. It dogfoods
  **statestore live** (the roadmap's outstanding proof) and lands **ipam's first real consumer** + the **first explicit
  capability-binding** + the **Crossplane subnet demotion** (ADR-0110 D5) — several deferred items close at once. The
  D2 handle generalization unblocks _every_ future non-statestore capability. floci gives real hosts + real network, so
  the cross-tool chain (NetBox → tofu → real SSH-able instance) is genuine.
- **Negative / trade-offs.** D2 is a **proto + core change** (additive, but real) — not the "free reuse" ADR-0111 D2
  implied for a second capability shape, and it shifts secret-safety onto a contractual invariant (D2). New opentofu
  plugin consumption code (D3). The module **must ship a committed provider lockfile** (D4, §7.3); remote module-source
  pinning stays a follow-up. floci network fidelity is **doc-confirmed, not yet live-run** (D7).
- **Follow-ups.** (1) A CI lint asserting every `capabilities/*.output.schema.json` is `additionalProperties: false` +
  CredentialRef-name-only (the D2 §2.5 guard). (2) Remote module-source pinning (OCI/git-ref). (3) The Ansible
  **configure** Step on the real floci host — the §5.1 provision→configure close. (4) The live-run proof (tofu apply on
  floci + SSH) — which also generates + commits the module's `.terraform.lock.hcl` (D4). (5) `Intent/Vpc` if a VPC must
  be a first-class Intent rather than module-internal. (6) The PDP sovereignty gate (ADR-0111 D5). (7) **The build-Step
  form for an Actuator builder (discovered in B3).** A Workflow Step is either an _actuation_ (`viewName + actuator`)
  or a targetless _Action_. Crossplane's `subnet-build` is a targetless Action; the opentofu Actuator's apply is
  _workspace-scoped_, so `opentofu-subnet-build` needs either a synthetic/anchor View for the actuation **or** a
  targetless `opentofu/apply` Action wrapper. The provisions→Workflow model (ADR-0110) is clean for Action-builders but
  under-specified for Actuator-builders — decide the form (a small ADR or a D-note) before the build Workflow ships.

## Alternatives considered

- **Provision via awsec2-native `create-subnet` Actions (my earlier plan).** Rejected: unrealistic (no team manages an
  AWS estate by imperative API calls), off-charter (§5.1/§5.2 are tofu), and it doesn't dogfood statestore. awsec2's
  Actions/Syncer coexist (the Syncer still owns `net.subnet`); they're just not the _primary_ network builder.
- **Force ipam's output into the statestore-shaped handle (`Config` map).** Rejected (D2): statestore's `config` is a
  nested map; ipam's fields are flat and typed — cramming them loses the contract shape. Carrying the validated output
  bytes is the clean, general fix.
- **Mint `Intent/Vpc` + route-table Entities now.** Rejected: the tofu module encapsulates the VPC/route-table stack;
  Stratt-side network primitives are only needed if a VPC must be an independently-managed Intent (booked follow-up).
- **Keep Crossplane as the Subnet builder.** Rejected (ADR-0110 D5): opentofu is the charter-recommended default; the
  binding makes the swap a one-line estate change, which is the whole point of the capability framework.
