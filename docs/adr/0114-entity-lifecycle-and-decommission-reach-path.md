# ADR 0114 — Entity lifecycle Actions + the desired-state decommission reach-path

- **Status:** **Proposed** (2026-07-24, steward) — vocabulary-linter **CLEAN**; charter-guardian **PASS after fixes**
  (fix 1: D4 now routes BOTH whole-Intent withdrawal AND count-down through the one reach-path — deterministic
  ordinal-descending exclusive selection — genuinely closing ADR-0058 D4's count-down item; fix 2: D2 delete is
  idempotent-on-absence + D4 suppresses an already-torn-down Finding, making the lagged tombstone _safe_ across the
  sync window; flag 3: D4 anchors teardown-provider resolution to the Entity's BUILD provider via Run provenance,
  so a post-build binding change can't misroute teardown).
- **Implementation note (slice 3, steward):** the **count-down** teardown reach-path is WIRED end to end
  (`reconcileDecommission` surfaces gated ordinal-descending `decommission/<entity>` Findings for
  Intent/Compute count-down excess — the ADR-0058 D4 item). **Whole-Intent withdrawal** reuses the shipped
  "retain" limitation (compiler.go: an Intent gone from CaC leaves its `onRemove` unknowable, so its built
  Entities retain + raise the orphan Finding, per ADR-0042's deferred run-retract) — a booked follow-up, not
  a silent gap. Finding-suppression across the sync-lag window relies on the idempotent-on-absence delete
  (slice 1) making any double-launch a safe no-op, plus natural resolution when the torn-down Entity drops
  from the next full-sync; a lag-window Finding-suppression refinement is booked.
- **Date:** 2026-07-24
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.5 (an Intent targets a capability _class_; teardown resolves a provider the same
  way build does) · §1.2 (the Syncer is the sole authority for observed state; a lifecycle Action mutates the
  real system and lets the Syncer re-project — it never becomes a second truth) · §2.4 (`onRemove:
retain|revert|remove`; a destructive teardown is **gated**, never an implicit precedence or auto-destroy) ·
  §1.8 (teardown is a legible, one-click-descent Finding, never a silent GC). **Extends
  [ADR-0113](0113-vsphere-provisioning-provider.md)** (the vcenter provisioning provider — lifecycle lands in
  the same dual-verb module, D1). **Mirrors [ADR-0095](0095-full-featured-ec2-connector.md)** (awsec2's
  imperative lifecycle Actions — and _confirms_ its recorded rejection of the View-scoped-Actuator model for
  per-instance lifecycle). **Reuses [ADR-0042](0042-cross-source-entity-liveness.md)** (presence-union
  liveness → tombstone-by-absence). **Builds [ADR-0058](0058-provisioning-from-intent.md)'s** booked-but-never-
  shipped "Destroy/decommission verb for count-down (§2.4)" as the symmetric counterpart to
  **[ADR-0110](0110-provisioning-class-reach-path.md)'s** provisioning reach-path. **Distinguishes**
  **[ADR-0047](0047-plugin-port-v1-full-surface.md)/[ADR-0050](0050-certificate-reconcile-actuator.md)** (the
  Actuator `Destroy` RPC verb — a different device class). **Settles the Actuator-builder-vs-Action-builder
  question** twice deferred (ADR-0112 follow-up #7 / ADR-0113 follow-up #2).

## Context

vSphere provisioning (ADR-0113) can CREATE a VM and a DVS portgroup but cannot power/reconfigure/snapshot/
migrate/**delete** them — create-but-can't-delete. Closing that asymmetry is the immediate need; the stated
goal is bigger: **validate that Stratt's provider-agnostic lifecycle patterns are functional and generalize
beyond vSphere.** A prior-art scan established the precedent (awsec2 ADR-0095) and surfaced the open decisions
this exercise forces into the light — chiefly ADR-0058's unbuilt decommission verb and the twice-deferred
"how does a Workflow Step target an existing thing" question.

vcenter is the **first** delete/lifecycle Action on a Syncer-observed _identity_ object: awsec2 and awss3 act
on raw id strings, but vcenter must **resolve a govmomi object handle by `vcenter.uuid`**, act, then rely on
**tombstone-by-absence**. That resolve-then-act + delete-then-tombstone flow is the concrete "does the pattern
extend" proof.

## Decision

### D1 — Imperative lifecycle Actions on the vcenter plugin, targeted by identity param

Lifecycle ops are **Actions** (invoke-only, `registerPluginAction`), not a View-scoped Actuator — mirroring
awsec2 (ADR-0095), whose "Alternatives considered" _explicitly rejected_ reclassifying per-instance lifecycle
as a declarative View reconcile. Each op takes the Syncer's own identity as its target param — `{uuid}` for a
VM (mirroring awsec2's `instanceId`), `{moref}` for a portgroup — plus op-specific fields. The one genuinely
new primitive vs awsec2 (which passes id strings straight to the API) is a **`resolveVM(ctx, client, uuid)`**
handle lookup (`SearchIndex.FindByUuid`, keyed on `config.uuid` = the BIOS uuid the Syncer projects) +
`resolvePortgroup(ctx, client, moref)`. Handlers reuse `invokeCreateVM`'s shape (unmarshal → resolve → govmomi
Task → typed outputs → terminal `InvokeResponse`), including dry-run. All ops land in `plugins/vcenter`
(ADR-0113 D1: one dual-verb module, so lifecycle shares the OBSERVE identity schemes structurally).

Op set (user-chosen breadth = Core + snapshot + mobility): `power-off`/`power-on`/`reset`/`suspend`/
`shutdown-guest`, `reconfigure` (cpu/mem), `snapshot-create`/`snapshot-revert`/`snapshot-remove`, `migrate`,
`clone`, `delete-vm`; portgroup `reconfigure-portgroup` (VLAN) + `delete-portgroup`.

### D2 — Delete is outputs-only; the graph tombstones by absence (ADR-0042)

`delete-vm`/`delete-portgroup` call `Destroy_Task`, return typed outputs, and emit **NO Entity and NO
tombstone** — `InvokeResult` structurally has no `gone` field (only the Actuator `Destroy` RPC verb does,
D-distinguish below). The object vanishes from vSphere, so the next Syncer full-sync drops its
`vcenter.uuid`/`vcenter.network.moref` from the seen-set and `TombstoneAbsent` retracts the Entity — exactly
as `awsec2/terminate` and `awss3/delete-bucket` rely on it (ADR-0042). This is the correct choice for a
Syncer-correlated object (vs certissuer's explicit-tombstone `Destroy`, which is a reconcile Actuator that
owns its Entities without a Syncer). The guard is a `create → delete → enumerate → assert-absent` test.

**Idempotent on an absent handle (the §1.8/§2.4 safety of D2's lagged tombstone).** Because delete emits no
_synchronous_ tombstone, the torn-down Entity lingers in the graph until the next full-sync — so a second
teardown can be issued against a `uuid`/`moref` that no longer resolves. Therefore `delete-vm`/
`delete-portgroup` are **idempotent on absence**: a `resolveVM`/`resolvePortgroup` miss is a terminal
**success (already-gone)**, never a hard error. This converts D2's "bounded latency" into _safe_ bounded
latency, and it is the teardown mirror of ADR-0058 decision 5 (a re-run targets the same unit, never a
duplicate). The reconcile side of that safety is in D4 (a decommission Finding whose teardown Run already
succeeded is not re-surfaced during the sync-lag window).

### D3 — State-changing ops project outputs-only; the Syncer stays the sole observed-state authority (§1.2)

power/reconfigure/migrate/snapshot return typed outputs and project **no Facet** — the Syncer re-observes the
new `vm.config`/`vm.runtime` on its next poll. This is _cleaner than awsec2's_ narrow-write-scope split (which
projects a transient `instance.state` Facet for immediacy): it keeps the plugin's write surface identity-only
(ADR-0113 D3) and avoids any Action↔Syncer Facet co-write, at the cost of state visibility latency bounded by
the sync interval (acceptable; the Syncer is authoritative). **Exception — `clone` CREATES a VM**, so it
projects the new VM **identity-only** (a fresh `vcenter.uuid` + the estate overlay/correlation labels), exactly
like `create-vm` (ADR-0113 D3). Snapshots are VM sub-objects, not graph Entities → no projection at all.

### D4 — The desired-state decommission reach-path: the symmetric counterpart to the build reach-path

Imperative Actions are the mechanism; the **reach-path** is how a _declarative_ withdrawal reaches teardown —
the mirror of ADR-0110's `requires:[provisioning] → build`. Three parts:

1. **`onRemove: remove` becomes valid for `Intent/Compute` and `Intent/Subnet`** (`validateOnRemove` today
   allows it only for Certificate/Access). It never auto-destroys — it _escalates_ §2.4's orphan Finding into
   an actionable, **gated** teardown.
2. **A provider advertises a per-kind teardown Workflow** via a new `decommissions: {Compute:
vsphere-vm-teardown}` map on the Actuator/Connector declaration, symmetric to `provisions` (ADR-0110 D3).
   Resolution reuses the pure `capability.Resolve` machinery, env-scoped (ADR-0113 D2) — a thin
   `resolveDecommission` assembler mirroring `resolveProvisioning`; fail-closed (§2.4). **Anchored to the
   build provider (§1.5).** The Entity being torn down was built by a _specific_ provider, recorded in Run
   provenance (the build Run's actuator/provider). `resolveDecommission` resolves the teardown Workflow **of
   that build provider** (verifying it against the fresh env-scoped class resolution), so a post-build binding
   change can never route a vSphere-built VM to a different provider's teardown. Class resolution is the
   common-case path; build-provenance is the correctness anchor.
3. **Both triggers route through this ONE reach-path (closing ADR-0058 decision 4 fully).** `reconcileDecommission`
   surfaces a gated `decommission/<entity>` Finding per Entity that should no longer exist, from **either**
   trigger: (a) **whole-Intent withdrawal** — an `onRemove: remove` Intent removed from CaC → every Entity it
   built is torn down; (b) **count-down** — an `Intent/Compute` whose `count` drops (5→3) → the **excess**
   Entities are torn down while the Intent lives (the mirror of the build shortfall Finding, ADR-0058 D4). To
   avoid a §2.4 tiebreak over _which_ instances die, count-down selection is **deterministic, ordinal-descending,
   exclusive-claim**: the highest-ordinal built instances (web-05, web-04 …) are chosen first, one Finding each.
   It finds the built Entities by Run-provenance / the `stratt.intent/instance` correlation label.
4. **Gated, per-entity, and idempotent across the sync lag.** An operator launches the gated teardown Workflow
   (`approve` → `vcenter/delete-vm` with `{{.launch.uuid}}`) — never an auto-run (§5 Flow: destructive ⇒ gated).
   Teardown is **intrinsically per-entity** (a generated `uuid` cannot be hardcoded, so it binds at launch —
   mirroring provisioning's own deferred per-instance parameterization). A decommission Finding whose teardown
   Run has already **succeeded is not re-surfaced** while D2's tombstone lag leaves the Entity transiently
   present — so the sync-lag window cannot double-fire teardown (the reconcile complement to D2's
   idempotent-on-absence delete).

This closes ADR-0058's "Destroy/decommission verb for count-down (§2.4)" as a first-class reach-path, not a
disconnected imperative call.

### D5 — This settles the Actuator-builder-vs-Action-builder Workflow-Step form (ADR-0112 #7 / ADR-0113 #2)

Both **build** (ADR-0110/0113) and **teardown** (D4) use the **targetless Action in a gated Workflow**, with
params/step-output/launch bindings — NOT a synthetic anchor View actuation. This ADR records that as the
resolution for **identity-object lifecycle**: the Action model (identity-param, credential-use-gated, ADR-0031/ 0028) is the canonical shape for "do X to this existing entity." The Actuator `Plan/Apply/Destroy` RPC verbs
(ADR-0047/0050) remain the model for **workspace-scoped reconcile devices** (opentofu, certissuer) that own
their Entities without a Syncer. The two coexist by device class; the twice-deferred question is now decided.

### D6 — A destructive-op protection guard (mirror awss3 `isProtected`)

`delete-vm`/`delete-portgroup` refuse (`codes.PermissionDenied`) a target bearing a protection marker — the
awss3 convention that guards the Evidence WORM store. The marker source is a vSphere custom attribute/tag read
on the resolved object; if wiring that read is more than a small add, it is **booked as a follow-up** (the
gated teardown Workflow's `approve` Step is the interim human guard) rather than bloating slice 1.

## Charter alignment

Upholds §1.2 (Actions mutate the real system; the Syncer is the single observed-state writer — D3), §2.4
(`onRemove` gated, fail-closed, no auto-destroy — D4), §1.5 (teardown resolves a provider by class, env-scoped
— D4), §1.8 (teardown is a legible gated Finding on descent, never silent GC), §1.4 (no new dependency; govmomi
stays plugin-tier). It **touches the data model** (`decommissions` on the Actuator/Connector Kinds), the
**intent compiler** (`onRemove` for provisioning kinds; `reconcileDecommission`), and **authz/gating** — the
highest review bar (charter-guardian + vocabulary-linter). It does **not** touch the sovereign plugin port
proto: lifecycle Actions reuse the shipped `Invoke`/`InvokeResult` surface.

## Consequences

- **Positive.** Closes create-but-can't-delete; validates the imperative-Action lifecycle pattern **extends to
  a Syncer-observed identity object** (resolve-then-act, delete-then-tombstone) — the user's pattern-validation
  goal. Closes ADR-0058's long-booked decommission verb as a symmetric reach-path, and **settles** the
  twice-deferred Actuator-builder question (D5). All provider-agnostic: the reach-path is core; any future
  provisioning provider gets teardown by declaring `decommissions`.
- **Negative / trade-offs.** The decommission reach-path (slice 3) is real core work (entity↔intent
  correlation, per-entity gated teardown). State-change visibility is sync-interval-latent (D3). The protection
  guard's vSphere-side marker may be deferred (D6). Delete correctness rides the Syncer full-sync, not a
  synchronous tombstone (D2) — a deleted object lingers in the graph until the next sync (bounded, observable).
- **The teardown was UNLAUNCHABLE, and D4's own text is where the tell was.** D4 said the operator
  "launches the gated teardown Workflow (`approve` → `vcenter/delete-vm` with `{{.launch.uuid}}`)", and the
  reconcile recorded the resolved Workflow and the target's identity **only inside the Finding's `diff`
  detail blob**. `resolveFindingLaunch` reads `launchWorkflow` first and otherwise falls through to a
  Baseline read — and there is no `decommission/<intent>` Baseline; nothing creates one — so every teardown
  answered **404 "no baseline and no launch spec, nothing to launch"**. The remaining path was to read the
  blob and hand-launch the Workflow by name with a retyped identity, which **also sidesteps D4's
  build-provenance anchor**, the entire point of resolving the teardown of the provider that built the
  thing. `diff` is documented as redacted and size-capped, so parsing a launch back out of it was never an
  option (ADR-0118 made the same call for the withdrawal path). Fixed: the decommission Finding now carries
  ADR-0120 D1's typed launch spec, rederived every pass like the provision Finding's.

  **`launchKind` is `remove`, and the enum stays at three.** ADR-0120 D1 left the rule "a fourth act must
  argue membership rather than add a field", and named this as the first real test of it. The argument
  fails, correctly: D1's three acts are converging live state, **retiring abandoned state**, and creating
  declared state. A count-down teardown retires state Stratt created whose declaration no longer asks for
  it — the same act as an orphan's `removeWorkflow`. What differs is only _which_ declaration lapsed (an
  Assignment vs. an Intent's cardinality) and _who_ supplies the Workflow (a Blueprint vs. a provider's
  `decommissions` map), and both of those already ride in `launchWorkflow`. It also lands on the same side
  of the read split: `remediate` reads its spec from the Baseline, `build` and `remove` from the Finding.

  **The identity is a (scheme, value) pair, and core refuses to guess it.** A teardown needs an identity
  for the thing it destroys, and that identity is provider-shaped — §1.5 says core does not know the
  spelling. So `provision.TeardownLaunchParams` sends the pair and the provider's own Workflow renames it
  (`uuid: "{{.launch.identityValue}}"`), exactly as a build Workflow maps the uniform launch shape onto its
  opaque params. Not a nested `identity` object keyed by scheme, which was the first idea and does not
  work: the substituter splits a binding path on `.`, so a dotted scheme like `vcenter.uuid` is
  unaddressable inside one. A built Entity carrying **more than one** identity is a §2.4 tiebreak core must
  not make, so the Finding surfaces with **nothing to launch** and a reason naming every competing scheme.
  Deliberately not solved by extending `decommissions:` to name a scheme: no built Entity in tree carries
  two, and inventing a declaration for a case that does not exist is how a schema grows fields nothing
  demands (§1.1). If it ever fires, that is the moment to decide.

  **And the check ADR-0120 D3 built for builders now covers teardowns**, because it had to: until the
  Finding carried a launch spec, a mismatched teardown Workflow was invisible — nothing launched it from
  the Finding at all. `validateDecommissions` was the same partial check `validateProvisions` was (entry
  non-empty, nothing more), so a **dangling teardown target** was possible, which is worse than a dangling
  builder because the operator meets it while trying to destroy something. The per-(Intent, Workflow) rules
  are now one shared function over both halves: inputs declared, every supplied key declared, every
  required key supplied, no hand-written correlation label, no hardcoded declaration literal, and an
  **approval gate** — the last one sharper here than on the build side, since §5 Flow's destructive-⇒-gated
  is what stops a launch from deleting a VM with no approval on the path.

- **Follow-ups.** (1) The protection-marker read (D6) if deferred. (2) Per-instance/entity build+teardown
  parameterization (shared with ADR-0058/0113 — launch-param driven). (3) `onRemove: revert` for provisioning
  kinds (restore-to-declared), distinct from `remove`. (4) A symmetric **ipam release** Step on
  `delete-portgroup` (return the VLAN/prefix to NetBox, the mirror of ADR-0113 D4's allocate) — unconfirmed
  whether NetBox exposes a release Action; booked. (5) Live real-vCenter teardown proof (shared with ADR-0113).
  (6) **§1.8 descent strengthener** — the delete Run records a pending-tombstone expectation so one-click descent
  shows "deleted; awaiting Syncer retraction" during the sync-lag window (D2), rather than an apparently-live
  Entity (charter-guardian flag 4, optional).

## Alternatives considered

- **Promote vcenter to a Plan/Apply/Destroy Actuator and use the `Destroy` RPC verb (explicit `gone`).**
  Rejected (D2/D5): vcenter is a Syncer-correlated device, not a workspace-scoped reconcile device; per-object
  VM lifecycle is imperative-per-instance (ADR-0095's recorded call), and the natural tombstone (ADR-0042) is
  the consistent, lower-surface path. The RPC-Destroy model stays for opentofu/certissuer.
- **Project the power/config state Facet from the lifecycle Action (awsec2's immediacy split).** Rejected
  (D3): it makes the Action a second writer of `vm.runtime`/`vm.config`, which the Syncer owns; outputs-only +
  Syncer-refresh is the cleaner §1.2 stance. (awsec2's split is acceptable there because it carved a dedicated
  transient `instance.state` Facet; vcenter need not.)
- **A plain imperative `vcenter/delete-vm` with no desired-state reach-path.** Rejected (D4): it would ship a
  second, disconnected deletion path while ADR-0058's charter-mandated decommission reach-path stays unbuilt —
  the exact "greenfield illusion" the prior-art discipline exists to prevent. The user chose to close it.
- **Auto-destroy on Intent withdrawal.** Rejected (D4, §2.4): withdrawn-but-retained raises a Finding; teardown
  is always gated. `onRemove` default stays `retain`.
