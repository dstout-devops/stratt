# ADR 0107 — EC2 as the `provisioning` capability provider (enablement-gate)

- **Status:** **Accepted** (2026-07-23, steward) — charter-guardian PASS (ADR-0058 reconciliation + non-goal boundary folded).
- **Date:** 2026-07-23
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.5 (sovereign contracts — `provisioning` is a class; EC2 is provider #1,
  swappable) · §1.4 (boring spine, pluggable everything — cloud provisioning is community breadth behind a
  core contract) · §1.1 (build only what a Contract demands — declare `provides` on the *existing*
  awsec2, defer the consumer + reach-path) · §2.5 (no key material crosses the core — awsec2 already
  ImportKeyPair-only, never CreateKeyPair). Builds on ADR-0106 (the enablement-gate vs resolve-inject
  distinction + the D1 reach-path guardrail), ADR-0104 (capability dependencies + verification), ADR-0095
  (the full-featured EC2 Connector), ADR-0096 (EC2 resource-graph Entities), ADR-0017 (provision→configure),
  ADR-0058 (`Intent/Compute` + the `builder:` field — the **already-shipped** provisioning reach-path this
  ADR must reconcile with, D2).

## Context

EC2 is the last of the four enterprise adds (Temporal = spine, D6; OpenBao, S3 landed). The `awsec2`
plugin already provisions and observes EC2 — `create-vm` + lifecycle Actions (start/stop/terminate/tag),
network Actions (vpc/subnet/security-group/volume), an ImportKeyPair path that never returns a private key
(§2.5), and a Syncer projecting the resource graph (ADR-0095/0096). It advertises **no** capability token
yet. Under the framework it is the natural **provider of `provisioning`** (the capability class already in
`types.ValidCapability`, ADR-0104): "provision machines other plugins target."

`provisioning` is **enablement-gate** (ADR-0106 D1), not resolve-inject: provisioning is a **target-scoped
Apply/Action** (create the machine, then provision→configure it — ADR-0017), not a low-rate config handle
the core injects. So — exactly like OpenBao's keycustodian/certissuer — EC2 gets **no resolve Action**;
`requires: [provisioning]` gates only that a verified provisioner exists.

## Decision

### D1 — EC2 advertises + provides `provisioning`, verified; no resolve Action (enablement-gate)

The awsec2 plugin advertises `Capabilities: [..., "provisioning"]` — **unconditionally**, because
provisioning is the plugin's core function and needs only the Region + AWS credentials a running plugin
must already have (the same honesty argument as keycustodian, ADR-0106 D2; there is no mount-like feature
to gate on). A registry declaration `estate/actuators/awsec2.yaml` declares `provides: [provisioning]`,
dials the awsec2 pod, and is verified (ADR-0104 D1). It carries **no resolve Action** (enablement-gate,
ADR-0106 D1); it exists to advertise + be verified. This is honest advertisement of *existing, working*
capability — not speculative machinery (§1.1).

### D2 — the reach-path is bound by the ADR-0106 D1 guardrail

**A provisioning reach-path already ships — and it is provider-coupled (ADR-0058).** `Intent/Compute`
carries a `builder:` field that names the provider Action **directly** — the live estate already uses
`builder: awsec2/create-vm` and `builder: crossplane/provision` (`estate/intents/*.yaml`), with `params`
as opaque pass-through validated against *that builder's own* input Contract. So provisioning is **not**
greenfield: it has real consumers, but via a seam that couples the Intent to a named provider — exactly the
§1.5 coupling the ADR-0106 D1 guardrail forbids. This ADR does not pretend that gap away.

The deferred `provisioning`-CLASS reach-path (follow-up #1) is therefore a **refactor of ADR-0058's
`builder:` seam toward provider-agnostic selection**, bound by the ADR-0106 D1 guardrail: a **class-pinned
"provision a machine → machine coordinates" Actuator shape** + an estate `capability→provider` binding, so
`builder:` becomes `requires: [provisioning]` and swapping EC2 → GCE/KubeVirt is a **binding change, not a
consumer+params rewrite**. Until that lands, ADR-0058's provider-named `builder:` coexists as the current
(coupled) reach-path — an acknowledged, booked gap, not a claim of purity. The provision→configure flow
(ADR-0017) is the consumer shape the class contract will serve: provision (class contract), then a configure
Step targets the resulting machine by identity.

### D3 — EC2 is provider #1 of `provisioning` (§1.5)

GCE, KubeVirt, and (via an external provisioner plugin) bare-metal are sibling providers of the **same**
`provisioning` contract — **once the class-pinned reach-path lands (D2)**, a consumer moves between them by
changing an estate binding, not consumer code. This is the "against VMs, against clouds, against bare metal"
breadth the estate-automation thesis wants, behind one capability.

**Non-goal boundary (§1 — "OS imaging/bare-metal").** `provisioning` is **machine-coordinate provisioning**
— *request a machine → machine coordinates* against an existing image/substrate — never OS-image authoring,
PXE, or an imaging pipeline. Exactly as Intune/Jamf are MDM **Connectors** (never Stratt implementing an MDM
protocol), a bare-metal provisioner (e.g. Tinkerbell/Cobbler) is a **plugin Stratt drives**, never Stratt
building imaging itself. The capability keeps the breadth thesis on the in-scope side of the non-goal.

## Consequences

- **Positive.** All four enterprise adds are now landed as first-class capability participants (Temporal
  spine; OpenBao keycustodian/certissuer; S3 statestore; EC2 provisioning). The change is a manifest line +
  an estate declaration — the framework (ADR-0104/0105/0106) absorbed the cost, exactly as intended. EC2 is
  the ADR-0106 enablement-gate pattern applied a second time, confirming the pattern generalizes.
- **Negative / cost.** `provisioning` now has a *verified provider* but its consumers still reach it via
  ADR-0058's provider-**coupled** `builder:` seam (D2), not the provider-agnostic class contract. That
  coupling is a real, acknowledged §1.5 gap the class reach-path refactor (follow-up #1) closes — the most
  consequential deferred contract (it governs provision→configure across every substrate), warranting its
  own ADR.
- **Scope discipline.** Ships: awsec2 advertises + is declared a verified `provisioning` provider. Defers
  (§1.1): the `requires: [provisioning]` consumer + the class-pinned provisioning reach-path contract
  (bound by D2); migrating the full awsec2 boot-env onto the registry (the provider declaration coexists,
  as `s3-statestore`/`openbao` do).

## Alternatives considered (rejected)

- **Give provisioning a resolve Action (mirror statestore).** Rejected (D1, ADR-0106 D1): provisioning is a
  target-scoped Apply that *creates* machines, not a config handle to inject. Enablement-gate is the honest
  shape.
- **Leave the reach-path as ADR-0058's `builder: awsec2/create-vm`.** Rejected as the *end state* (D2, §1.5):
  the provider-named `builder:` couples the Intent to EC2, the exact vendor-lock the framework prevents. It
  coexists as the *current* reach-path (it ships), but follow-up #1 refactors it to `requires: [provisioning]`
  + a binding. This ADR advertises the provider now; it does not bless `builder:` as the final shape.
- **Build the class reach-path + refactor `builder:` now.** Rejected (§1.1, scope): declaring `provides` on
  the existing awsec2 is honest advertisement (zero new machinery); refactoring the shipped ADR-0058 seam is
  a consequential change with live estate consumers, so it earns its own ADR (follow-up #1), not a rider here.

## Follow-ups (separate slices / ADRs)

1. The `provisioning` class reach-path contract — a **refactor of ADR-0058's provider-named `builder:`
   field toward `requires: [provisioning]` + an estate `capability→provider` binding** (bound by ADR-0106 D1
   / this D2): a class-pinned "provision → machine coordinates" Actuator shape so EC2 → GCE/KubeVirt is a
   binding change, not a consumer+params rewrite. The highest-value deferred contract; its own ADR.
2. Sibling providers — GCE / KubeVirt / Cobbler / bare-metal advertising `provisioning` behind the same
   contract (D3), each its own plugin.
