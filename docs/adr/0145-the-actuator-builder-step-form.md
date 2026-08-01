# ADR 0145 — The build-Step form for a workspace-scoped Actuator: capability injection reaches the Action seam, and the network leg goes live

- **Status:** **Proposed** (2026-07-28, steward) — implemented and **live-proven**. Charter review by
  hand (this session's rules bar the subagent); §1.1/§1.2/§1.4/§1.5/§2.4/§2.5/§1.8 answered inline.
  **One new toolchain dependency** (a pinned `tofu` binary, D5). **No new Named Kind**, no new
  capability class, no new Step shape.
- **Date:** 2026-07-28
- **Deciders:** steward
- **Charter sections:** §1.5 (sovereign contracts — the Action's typed seam, and OpenTofu as a
  transport beneath it), §2.4 (no implicit precedence — the projection overlay REFUSES a conflict
  rather than resolving one), §1.2 (projections never a second truth — a dry-run projects nothing),
  §1.8 (never hide diagnosis — every silent-success path found here is made loud), §7.3/§1.7
  (pinned executable content — the committed provider lockfile and the pinned tofu), §2.5 (brokered
  credentials; a Run's captured outputs are not a secret channel)
- **Reconciles with:** **ADR-0112 follow-up #7**, which named this exact question and deferred it
  ("decide the form … before the build Workflow ships") — **this is that decision**; ADR-0112 D4
  (the binding condition: `required_providers` + a committed `.terraform.lock.hcl` — **now
  satisfied**), ADR-0112 D5 (`aws.subnetId` co-ownership — **now live-verified**), ADR-0112 D6 + ADR-0110
  D5 (the Crossplane→OpenTofu subnet demotion, **declared in two ADRs and delivered here**),
  ADR-0110 D3 (`provisions` per-kind build routing), ADR-0105 (capability resolve+inject — **D2
  below extends its seam**), ADR-0111 (ipam), ADR-0059 §6 + ADR-0058 §6 (the build's estate overlay),
  ADR-0120 D2/D3 (the per-singleton launch interface and the withdrawn `provisions` entry this
  restores), ADR-0123 D3 (declared inputs must be bound), ADR-0031 (Actions), ADR-0047 §7/§8 (the
  plan pin — **D3 states plainly what this form cannot have**), ADR-0017 (`stratt_entities`),
  ADR-0114 D4 (decommission — **D6 fixes a teardown that could not fail**).

## Context

The reference estate's network leg was **dead**, and had been for two ADRs.

`opentofu-network` advertised `provisions: {Subnet: opentofu-subnet-build}` against a Workflow **that
was never written**. `validateProvisions` only checked the entry was non-empty, so a declared
`Intent/Subnet` produced a build Finding naming a Workflow nobody could launch — discovered by an
operator at a gate. ADR-0120 D3 withdrew the entry rather than leave the promise standing, and wrote
down the condition for restoring it. So `app-subnet` and `dmz-subnet` were unbuildable, and
`app-tier`'s placement depends on `app-subnet`.

Underneath that sat a design question ADR-0112 discovered and deliberately deferred as follow-up #7:

> A Workflow Step is either an _actuation_ (`viewName + actuator`) or a targetless _Action_.
> Crossplane's `subnet-build` is a targetless Action; the opentofu Actuator's apply is
> _workspace-scoped_, so `opentofu-subnet-build` needs either a synthetic/anchor View for the
> actuation **or** a targetless `opentofu/apply` Action wrapper.

And underneath **that** sat a fact neither option had been checked against: **`InvokeRequest` had no
`resolved_capabilities` field, and `ExecuteAction` never resolved any.** `opentofu-network` declares
`requires: [statestore, ipam]`; its entire reason for existing is capability composition (ADR-0112
D1). The Action option was structurally blocked, and nothing said so — the port was never consulted.

Meanwhile the module itself had never been executed by anything. There was no `tofu` binary in the
dev container, so `deploy/tofu-modules/aws-network/` shipped with **no `.terraform.lock.hcl`** (ADR-0112
D4's own binding condition for release-readiness), **no `tofu validate`**, and no apply. Its README
said so honestly and the state persisted anyway, because nothing in `task ci` could have noticed.

## Decision

### D1 — A workspace-scoped builder's Step is a targetless **Action**, never an anchor View

`opentofu-subnet-build`'s build Step is `action: opentofu/apply`. The anchor-View option is rejected.

Three reasons, in ascending order of how much they matter.

**A `tofu apply` is not an actuation in any sense the core means.** It converges a _workspace_. It
reads no target set — `Apply` already folds its result under the workspace-root `item_key ""` — and
has no per-target status, no slicing, no Site routing and no `facetWriteScope` to govern. An anchor
View would hand all that machinery a set of Entities the build ignores.

**An anchor View's failure mode is silence.** A View is a selector; a selector that matches nothing
yields a Run with zero targets. For a fleet converge that is a benign no-op. For a _build_ it means
the operator approved a gate, the Run went green, and no infrastructure exists — the single outcome
a build must never have.

**Decisively: only the Action seam carries the estate overlay, and the correlation label has nowhere
else to travel.** A build's projection must carry `stratt.intent/singleton` or the Finding it was
launched from never resolves. On the actuation path a plugin's proposed Entities reach the graph
exactly as the plugin produced them (`executePlugin` → `EntityObservation{Kind, IdentityKeys,
Labels}`) — there is no overlay — so under an anchor View the label would have to come out of the
tofu module's `stratt_entities` output. **It structurally cannot:** `outputsToWire` refuses any
`stratt.*`-prefixed label from a module, and rightly so, since that prefix is the platform's. The
Action seam has carried `projectKind`/`projectLabels`/`identityScheme` since ADR-0058 §6, which is
why Crossplane's subnet builder is one too. This is not two viable options with a preference between
them; one of them cannot express the thing.

The general rule: **a provider whose unit of work is a workspace, a tenant, or an account — rather
than a set of Entities — builds through a targetless Action.** An actuation is for work that is
_per-target_, and the target set is what makes it one.

### D2 — Capability injection reaches the **Invoke** and **Destroy** seams

`InvokeRequest.resolved_capabilities` and `DestroyRequest.resolved_capabilities` are added (additive,
`proto:breaking` clean). `PluginAction` gains `Requires`, populated from the same declaration its
sibling Actuator reads it from, and `ExecuteAction` calls the same `resolveCapabilities` the Apply and
Plan paths call, failing closed identically.

**The asymmetry was an accident of sequencing, not a decision.** ADR-0105 landed capability
resolve-and-inject on the Actuator verbs; nothing carried it across. The effect is that `requires:`
on a declaration was a promise only **half** its dispatch surface kept: an Action served by the same
plugin, under the same declaration, silently got no handle at all. A build through it would have run
`tofu` with no S3 backend and no NetBox-allocated CIDR — and reported success. The failure would have
surfaced as "why did every build create its own VPC", far from the cause.

This is not opentofu-specific. **No Action could compose a capability**, so `awsec2/create-vm` could
not take an `ipam` handle either. The fix is a property of the seam, not of one provider.

Two boundaries are drawn explicitly:

- **A capability RESOLVER gets no handles of its own.** `resolveCapabilities` invokes the bound
  resolver Action directly with none. A resolver is the bottom of this chain; letting one `require`
  a capability makes resolution recursive with no cycle rule. A resolver needing configuration takes
  a CredentialRef, like any Action.
- **`Destroy` gets them too**, and not for symmetry's sake: without the statestore handle, tofu
  cannot read the state that says _what_ to destroy — see D6.

### D3 — This form has **no plan pin**, and that is stated rather than papered over

ADR-0047 §7/§8's approve-what-you-see — `tofu plan` → digest → a Gate binds the digest → apply
**exactly** that plan — lives on the Actuator seam. `plan: true` is an actuation field and actuations
require a `viewName`, so a workspace-scoped build **cannot produce a plan digest today**.

So `opentofu-subnet-build`'s Gate is **approve-THAT-it-happens**, not approve-what-you-see. For a
network build — the case the plan pin was designed for — that is a real gap, and the honest thing is
to name it in the Workflow, in this ADR, and in the follow-ups, rather than let a Gate imply a
guarantee it is not making. **§1.8 applies to our own limitations, not only to a tool's failures.**

Closing it means either a targetless Plan verb or an Action-side plan/apply pin pair. Both are real
designs; neither is this slice. Booked as follow-up #1.

### D4 — The estate overlay **refuses** a conflict; it never resolves one

`opentofu/apply` requires `projectKind` and takes optional `projectLabels`, and applies them to the
module's proposed Entities with these rules:

- module `kind` non-empty and **different** from `projectKind` ⇒ **refuse the build**;
- a label key in both with **different** values ⇒ **refuse the build**;
- otherwise the overlay is additive, and the module's own descriptive labels survive.

Merging by precedence would be exactly the implicit precedence §2.4 exists to forbid, and the
consequence is concrete rather than theoretical: `upsertEntityTx` **retypes** on the correlate
branch, so a silently-wrong kind propagates through the graph instead of sitting still.

Keeping `kind` in the module _and_ requiring agreement is deliberate. The module states what it
believes it built; the estate states what it is being built as; a disagreement is an authoring error
that is now caught at the build instead of discovered in the graph.

`projectKind` is **required**, not defaulted. A default would be a kind this plugin invented for
infrastructure it knows nothing about — the module could be building anything — and a build with no
kind lands an Entity no View selects and no reconcile ever closes.

### D5 — The toolchain is pinned, the module is locked, and both are in `task ci`

- `TOFU_VER` pinned to **1.12.5** (current line; N-1 is 1.11.x, and the module's
  `required_version = ">= 1.8.0"` keeps both working), sha256-verified from the release's own
  `SHA256SUMS`, installed to `.bin/` by `task tools:tofu` — the same shape as `helm`/`kubectl`.
- **`.terraform.lock.hcl` is generated and committed** (`hashicorp/aws` 5.100.0, hashes for
  `linux_amd64`/`linux_arm64`/`darwin_arm64`). This satisfies **ADR-0112 D4's binding condition**,
  which is what made the module release-blocked. Multi-platform on purpose: a lockfile carrying one
  platform's hashes fails `tofu init` everywhere else, and a build pod is the worst place to find out.
- **`task tofu:validate` runs inside `task ci`.** Bundled tofu modules are executable content and
  belong in the gate for the same reason the playbooks do. They were outside it only because no tofu
  existed here — which is precisely how a module shipped unvalidated for months.

The gate is **hermetic**: it pins `TF_DATA_DIR`/`TF_PLUGIN_CACHE_DIR` into gitignored `.bin/`, so its
result never depends on what ran before it.

### D6 — Three latent defects found by executing what had only been declared

Each was invisible to `task ci`, and each fails **silently or late**. They are recorded here because
the pattern — not the individual bugs — is the finding.

1. **`stratt.name` in the module's reserved output.** `outputsToWire` refuses `stratt.*`-prefixed
   labels; the module's `stratt_entities` carried one. The **first genuine apply would have failed at
   the projection**, after creating real infrastructure. Removed; the estate's keys now arrive
   through the D1 overlay, which is also what keeps one module reusable across every singleton.

2. **`tofu init` without `-reconfigure`.** The module directory is a long-lived mount shared by every
   Run a pod serves, and the backend config is **per-workspace**. tofu detects the change and refuses
   to init. So the **first build in a pod succeeded and every build after it failed** — visible only
   by running two, which nothing ever had. `-reconfigure` and explicitly _not_ `-migrate-state`: the
   latter would copy the previous workspace's state onto this workspace's key, so building
   `dmz-subnet` would inherit `app-subnet`'s state and then "converge" it by destroying
   `app-subnet`'s network.

3. **A `Destroy` verb that could not fail.** It ran `tofu destroy` in the process's CWD with no
   module, no env, no vars and no backend — reading its own doc comment's claim that "Destroy carries
   no `desired`", which was never true (`DestroyRequest.desired` has always existed). `tofu destroy`
   in a directory with no configuration **exits zero**, so the verb reported a successful teardown of
   infrastructure it had not touched, and ADR-0114's decommission Finding would have closed on it. It
   now takes the same path Apply does. _(The core does not yet invoke this verb anywhere — teardown
   runs through `decommissions:` Workflows — so the fix is ahead of its consumer, but a teardown that
   cannot fail must not sit in the tree waiting for one.)_

Separately, `TF_DATA_DIR` was `<module>/.terraform` — **one directory shared by every Run**. Two
concurrent builds would each re-init the backend under the other and one would apply against the
other's state; and it wrote into the mounted module, so the module that ran stopped being the module
that shipped. It is now per-workspace under a `DataRoot` outside the module tree, with a **shared**
provider cache (the isolation that matters is state; re-downloading a ~600MB provider tree per build
would be a large price for isolating something the lockfile pins to one hash).

### D7 — `provisions` is restored, and the binding is **environment-scoped**

`provisions: {Subnet: opentofu-subnet-build}` returns to `opentofu-network`, together with the
`identitySchemes: [aws.subnetId]` and `labelKeys: [source, stratt.intent/singleton]` grants the build
needs. The correlation-label grant is enforced by the guard added alongside ADR-0144 — ungranted,
`Host.toUpsert` **drops** the key rather than refusing, so the Entity lands, the Run goes green, and
the only symptom is the same build offered again forever.

`estate/capability-bindings/provisioning-subnet.yaml` selects `opentofu-network` for `Subnet` and is
scoped **`environments: [dev, prod]`**. Unscoped, it would be in scope in `vsphere-dc` too, where
`provisioning-vsphere.yaml` already binds `Subnet → vcenter` — two documents claiming
(provisioning, Subnet) in one scope is the cross-document collision §2.4 refuses. Naming the
environments states where OpenTofu is the substrate instead of leaning on a precedence rule that does
not and must not exist.

This lands **ADR-0110 D5's** demotion of the opaque-Claim Crossplane subnet builder in favour of the
charter-canonical network builder (§5.1/§5.2/§3), and **ADR-0112 D6's** first live explicit
capability-binding — both declared, neither previously delivered.

### D8 — Under this provider, an `Intent/Subnet`'s `params.cidr` is a **claim**, not an assignment

`opentofu-subnet-build` deliberately does not declare `params` in its launch interface. The CIDR
arrives as `var.stratt_ipam_cidr`, injected by the core from the resolved `ipam` handle — NetBox
allocates it. Crossplane's `subnet-build` binds `{{.launch.params.cidr}}`; this one cannot, and
declaring an input no Step binds is refused at load anyway (ADR-0123 D3).

Said plainly, because an operator will meet it: **the allocator decides the range.** The built
subnet's `net.cidr` label reports what it actually got (§1.2 — the graph holds what is). A declared
`cidr` under this provider expresses _that a range is wanted_, not _which one_. This is the intended
direction — hand-assigning addresses in Git is what the `ipam` capability exists to stop — but it is
a behaviour change relative to the Crossplane builder and belongs in the record, not in a surprise.

## Consequences

- **The network leg is buildable and live-proven.** `task dev:tofu:proof` runs a real `tofu` applying
  the shipped module against the real floci EC2 API with real S3 state, through the `opentofu/apply`
  Action a launched build invokes. It asserts the subnet exists **by asking the API independently**,
  that its CIDR is the one the core _injected_ (not one the module chose), that it reads back under
  `aws.subnetId` (ADR-0112 D5's co-ownership, now verified rather than asserted), that the projection
  carries `stratt.intent/singleton` from the launch, that state landed in the injected S3 backend, and
  — the falsification — that **re-applying yields the same subnet id** rather than leaking a second
  VPC. Teardown runs through the plugin's own `Destroy`, so a regression there shows up as the
  previous run's network still standing.
- **`requires:` now means the same thing on both dispatch surfaces**, for every plugin, not just this
  one.
- **A Gate on a build still does not show you the plan** (D3). This is the one thing this slice makes
  _available_ without making _complete_.
- **Crossplane's `subnet-build` stays declared and validated** but is no longer bound for `Subnet` in
  `dev`/`prod`. It remains the sole `Vlan` provider. Nothing is deleted; the binding moved.
- **floci's network surface is confirmed real, including the read path** — and it is genuinely
  **region-scoped**. That cost an hour here: independent readers querying `us-east-1` reported "the
  build created nothing" for a build that had worked perfectly in `eu-west-1`, and the near-miss was
  writing that up as a floci fidelity regression. The live test now pins one region across the module
  and every reader. HAR-1's network-write finding stands unamended.
- **A dev-stack blocker fixed on the way:** seaweedfs's healthcheck probed `GET /`, which its
  `-s3.config` identity makes a 403 — proof the server is answering, read as proof it was not. So the
  container reported unhealthy while serving correctly, and `docker compose up --wait`, which
  `task dev:up` uses, could never come up. Probes `/healthz` now.

## Follow-ups

1. **The plan pin for a workspace-scoped build** (D3) — a targetless Plan verb, or an Action-side
   plan/apply pin pair. Until then a build Gate is approve-that, not approve-what.
2. **Wire the `DESTROY` verb to a consumer**, or drop it from the manifest. It is advertised and the
   core calls it nowhere; ADR-0114's teardown goes through `decommissions:` Workflows.
3. **`actuators/opentofu.input` misdescribes its own params** — it requires a `mode` the plugin never
   reads (the verb decides) and documents `module` as "root module HCL" when the plugin resolves it as
   a directory name. Never caught because no Step in the repo uses the opentofu _Actuator_. Left
   untouched here to keep this slice's contract changes to the Action it adds.
4. **Remote module-source pinning** (ADR-0112 follow-up #2) still open; the module remains a
   first-party bundled asset pinned by image digest + the now-committed provider lockfile.
5. **The `in-vlan` edge Crossplane's builder projected has no counterpart here**, deliberately: the
   AWS module builds a VPC-scoped subnet and AWS has no first-class VLAN (the module carries the ipam
   VLAN id as a tag and says so). Projecting one to match would be asserting a topology the substrate
   does not have.

## Alternatives considered

- **Anchor View + actuation.** Rejected — D1. It cannot carry the correlation label at all, and its
  empty case is a silent successful no-op.
- **A fifth Step shape: a targetless _actuation_.** Genuinely attractive, because it keeps the Plan
  verb and with it approve-what-you-see (D3), and because `tofu` really is an Actuator. Rejected for
  blast radius and for authz: an actuation's chokepoint is the runner-on-View grant, and a targetless
  actuation has no View, so it would need a replacement gate designed from scratch — while every
  invariant that reads "an actuation has targets" grows a second branch. The Action seam already has
  its chokepoint (the CredentialRef use-check) and already carries the overlay. Worth revisiting _only_
  as the vehicle for follow-up #1, where the plan pin would justify the cost.
- **Smuggle the correlation label through the module as a tofu variable.** Rejected outright. It
  routes the estate's correlation concern through HCL and back, and a module that dropped it would
  leave the Finding unresolved with a green Run — a fresh instance of the exact defect closed
  alongside ADR-0144. The plugin's reserved-prefix refusal already forbids it, and that refusal is
  correct.
- **Keep Crossplane as the `Subnet` builder.** Rejected again (ADR-0110 D5, ADR-0112 D6): OpenTofu is
  the charter-recommended default, and the Crossplane builder provisions an opaque `ConfigMap`-shaped
  Claim in dev — it does not build a network.
