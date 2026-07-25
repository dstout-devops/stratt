# ADR 0120 — Provisioning joins the parameter plane: a Finding carries its own launch spec

- **Status:** **Proposed** (2026-07-25, steward) — charter-guardian **CHANGES REQUIRED → resolved**;
  vocabulary-linter **CLEAN**. Five violations accepted and three of this ADR's own factual claims
  withdrawn or corrected. All recorded in **Review record** below, because two of them changed a decision
  rather than a sentence.
  **vocabulary-linter:** `launchWorkflow` / `launchParams` / `launchKind` and the generated param names all
  clean — no banned term, no Named-Kind collision. It was asked specifically whether `launchKind` overloads
  `Kind` (`Intent.Kind`, `Trigger.Kind`, `TargetRef.Kind`, `projectKind`) and whether
  `types.Finding.LaunchParams` collides with the existing `orchestrate.LaunchParams`; both cleared —
  `Kind` is an established discriminator-field pattern, and the two `LaunchParams` are the same concept at
  different layers (typed transport vs persisted JSON), which §2 treats as consistency rather than
  collision. `remove` over `withdraw` is what made the chain read one-concept-one-name.
- **Date:** 2026-07-25
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams), §1.2 (projections, never a second truth), §1.4 (boring
  spine, content-blind), §1.5 (sovereign contracts), §1.6 (one capability, every surface), §1.8 (never
  hide diagnosis), §2 (frozen vocabulary), §2.4 (no implicit precedence), §4.3 (blast-radius gating),
  §5 Flow 1 (a build is gated, never auto-run)

## Context

ADR-0118 built the parameter plane, and its follow-ups extended it to the withdrawal path. One place it
still does not reach, and it is the place with a **live functional defect**:

`estate/intents/web-fleet.yaml` declares `count: 2`, which the provisioning reconcile expands to
`web-01` and `web-02` (`provision.InstanceName`) and surfaces as two `provision/web-fleet` Findings
(`controller.go:275-284`). Both route to `estate/workflows/compute-build.yaml`, which declares **no
`inputs`** and hardcodes:

```yaml
name: web-01
projectLabels:
  stratt.intent/instance: web-01
```

`ResolveLaunchInputs` refuses any supplied param for an inputs-less Workflow, so **no gated, declared
path builds `web-02`.** Two routes to it exist and both are bad: an estate author hand-parameterizes the
Workflow and hand-types the correlation label, or someone runs the `awsec2/create-vm` Action ad hoc via
`startAction`, which validates against the Action's input Contract and launches with **no Workflow gate
at all** — bypassing the `approve` Step that §5 Flow 1 exists to enforce. The defect is therefore not
"an instance cannot be built" but "the gated path cannot build it, so the ungated one is the only one
that works", which is worse.

The second Finding never resolves either way, because the reconcile matches on the
`stratt.intent/instance` label and nothing projects `web-02`'s.

This is a **known** gap, recorded three times and never closed:

- `estate/workflows/compute-build.yaml:12` — _"per-instance parameterization (web-01 vs web-02 at
  launch) … is the build-execution slice that follows this one"_
- `estate/workflows/subnet-provision.yaml:5` — _"the per-instance parameterization the compute-build
  note flagged"_
- ADR-0114:124 — the decommission path defers the same thing, _"mirroring provisioning's own deferred
  per-instance parameterization"_

The values are not missing. The reconcile computes them and already puts them on the Finding —
`instance`, `ordinal`, `projectKind`, `labels`, `params`, `placement`, and the resolved provider's
`buildWorkflow` (`controller.go:275-284`, `provisioning_resolve.go:158`). They sit in a **freeform detail
blob** with no typed channel to a Workflow: the shape the withdrawal path had before ADR-0118's
follow-up, and what §1.8 calls hiding mechanism — present, displayed, unusable.

**And the target shape already ships.** `estate/workflows/subnet-provision.yaml` did exactly this for a
singleton, correlation label included (the file has since been deleted — see the follow-up: it was a
duplicate of the advertised `subnet-build`, whose input shape could never match what the reconcile sends,
so nothing could ever route to it):

```yaml
stratt.intent/singleton: "Intent/Subnet/{{.launch.subnetName}}"
```

So this ADR applies a proven in-repo pattern to the fleet path rather than inventing one. What it must
decide is where the launch spec lives, since a provision Finding has no Baseline to hold it.

### The finding that changes the design

Fixing this with a third pair of fields would be wrong, structurally.

Three kinds of Finding route to a Workflow, and they differ in **where the launch spec can live**:

| Finding   | Baseline row                                                                     | Spec lives      | Is the copy authoritative?             |
| --------- | -------------------------------------------------------------------------------- | --------------- | -------------------------------------- |
| drift     | **live** — the compiled Baseline                                                 | on the Baseline | n/a — read live                        |
| orphan    | **pruned** by the same Apply that wrote the Finding                              | on the Finding  | **yes** — the only surviving record    |
| provision | **never existed** — `provision/<intent>` is a synthetic grouping name, not a row | on the Finding  | **no** — derived, recomputed each pass |

Two of the three cannot use a Baseline, for different and permanent reasons: an orphan's Baseline must be
pruned (a Baseline whose Assignment is withdrawn must stop being observed), and a provision Finding is
raised _before anything exists to compile a Baseline from_ — writing one would make the graph a home for
the not-yet-built, which ADR-0058 M1 and §1.2 both refuse.

So "a Finding may carry its own launch spec" is the general shape, and the ADR-0118 follow-up that
introduced `Finding.removeWorkflow/removeParams` named it after its first caller. Adding
`buildWorkflow/buildParams` beside it would make that permanent and guarantee a third pair when
ADR-0114's decommission path lands.

The right-hand column is the part the first draft missed, and it is load-bearing — see D2.

## Decision

### D1 — One launch spec on a Finding: `launchWorkflow` + `launchParams` + `launchKind`

`Finding.removeWorkflow` / `removeParams` are **renamed** to `launchWorkflow` / `launchParams`, plus
`launchKind` naming the act: **`remediate` | `remove` | `build`**.

`launch` is already the frozen term for invoking a Workflow with typed inputs (`{{.launch.X}}`,
`LaunchParams`, `Workflow.inputs`, `ResolveLaunchInputs`). `remove` — **not** `withdraw` — because the
chain already reads `onRemove: remove` → `Blueprint.removeWorkflow` → `Baseline.RemoveParams`, and
`types/finding.go` states the rule this would have broken: _"one concept, one name down the whole chain
(§2)."_ A frozen vocabulary does not get a synonym for an act it already names.

`launchKind` is a closed, core-owned set because these are **spine acts** — converging live state,
retiring abandoned state, creating declared state. A plugin never adds one; that is the §1.4 line. The
spine already carries closed enums of exactly this character (`FindingPending|Open|Resolved`, claim
`exclusive|additive`, `onRemove: retain|revert|remove`), so this is not the whole-Entity typing §1.1
forbids. It stays at three, and a fourth act must argue membership rather than add a field.

**`launchKind` is the single branch point, and `Framework` stops being one.** `Finding.Framework`
already carries `provision` and `orphan`, and `resolveFindingLaunch` currently branches on
`f.Framework == "orphan"`. Shipping both without saying which is authoritative gives two fields that can
disagree, resolved by whichever branch runs first. So: `launchKind` decides what launches; `Framework`
reverts to what §2.4 calls it, the compliance tag ("one kind, framework-tagged"); the
`Framework == "orphan"` branch is retired in the same change.

**The rename mechanics, stated rather than waved at.** Migration 00042 is unreleased — `origin/main`
stops at 00040 and this branch has no upstream — so 00042 is **edited in place** rather than renamed by a
new migration. A `RENAME COLUMN` in a fresh migration would be the honest choice against a released
schema, and would need ADR-0078's `-- expand/contract-ok:` marker plus a roll window where old replicas
still write the old name. Neither applies to a column no deployment has ever had. The cost is local: a
dev database that already applied 00042 keeps the old column names, because goose will not re-run an
applied version — `task dev:down && task dev:up` is the fix, and it is named here so nobody debugs it
twice.

### D2 — The provision Finding carries the per-instance launch spec, and it is REDERIVED every pass

`launchWorkflow` = the resolved provider's build Workflow (`capability.Result.Workflow`, already
computed). `launchKind: build`. `launchParams`:

| Param         | Shape             | Source                                                                                            |
| ------------- | ----------------- | ------------------------------------------------------------------------------------------------- |
| `instance`    | string            | `Instance.Name` (`namePrefix` + zero-padded ordinal)                                              |
| `ordinal`     | integer           | `Instance.Ordinal`                                                                                |
| `projectKind` | string            | `ComputeSpec.ProjectKind`                                                                         |
| `labels`      | object            | `ComputeSpec.Labels`, plus the `stratt.intent/instance` correlation label derived from `instance` |
| `placement`   | object, optional  | `ComputeSpec.Placement` when declared                                                             |
| `params`      | **opaque object** | `ComputeSpec.Params`, passed through WHOLE                                                        |

**`params` is passed through as one opaque object, never flattened into sibling inputs.** The first draft
flattened it and that was a §1.1/§1.5 violation. `contracts/intents/compute.v3.schema.json` declares
`params` as `additionalProperties: true` and says why: _"Opaque build params passed to the resolved
provider, validated against ITS input Contract downstream (§1.5). Provider-shaped by design (EC2's
region/ami ≠ GCE's zone/image); Intent/Compute never types these."_ A Workflow's `inputs` schema is
**closed** — `CompileInputSchema` requires `additionalProperties: false`. Flattening therefore forces
every build Workflow to enumerate `region`, `instanceType`, `ami`…, so adding one param to an Intent
breaks the launch until an estate Workflow is edited: the provider coupling ADR-0110 deliberately removed,
reintroduced one layer down. One opaque input keeps the provider's Action Contract as the only typing
site.

Passing it whole also removes a §2.4 collision class rather than needing a rule for it: `params` is open,
so `params: {instance: …}` is a legal Intent today, and flattened it would collide with a core-generated
key with no exclusive-claim rule to resolve it — precisely the implicit precedence the anti-GPO axiom
forbids. Namespaced, the collision cannot be expressed.

**These params are DERIVED, and that makes them the opposite of the orphan's.** The orphan Finding's spec
is the only surviving record, so it is written once and never touched. A provision Finding's spec is
recomputed from Git every reconcile — `WriteProvisionFinding`'s own comment says _"recomputed every
reconcile"_ — so the upsert **must** refresh it:

```sql
ON CONFLICT (baseline, target) WHERE status <> 'resolved'
DO UPDATE SET diff = excluded.diff, last_observed = now(),
              launch_workflow = excluded.launch_workflow,
              launch_params   = excluded.launch_params
```

Today's `DO UPDATE` refreshes only `diff` and `last_observed`. Adding the columns without extending it
would serve an already-open Finding the params from its **first** reconcile, so a Git edit to `labels` or
`placement`, or a `CapabilityBinding` change that swaps the build Workflow, would launch yesterday's
desired state from a Finding that looks current. That is the second truth §1.2 forbids, and it is a
one-line omission — which is why the invariant is written into the decision and not left to the
implementation: **an orphan's launch spec is immutable; a provision Finding's is a projection.**

The correlation label is the load-bearing param: the next reconcile matches on it to decide the instance
is built, so a build that projects the wrong one produces a host nobody asked for _and_ a Finding that
never resolves — today's behaviour for every instance after the first. Note it is currently derived
rather than carried: the compute branch of `controller.go` does not emit `correlationLabel` (only the
singleton branch does), so D2 adds it.

**Who writes the Entity does not change.** The label travels launch params → Step params → the Action's
`projectLabels` → a Run-provenance projection. ADR-0058 M1 already specifies that kind, labels and the
correlation label ride the build Action's output projection and never a reconcile-side write. The writer
remains the Run (§1.2).

### D3 — Validated at declaration and at the reconcile, reusing the shipped chokepoint

A route's `remediationParams` are cross-checked against the Workflow's declared inputs at compile, and
their keys at declaration (ADR-0118 D3). Provisioning has no compile — it is a sibling reconcile
(ADR-0058) — so the equivalent is:

- **At declaration:** the same functions, not a second dialect. `checkTriggerLaunchInputs` and
  `checkBlueprintParamNames` already perform declaration-time launch-input checks through
  `contract.InputNames` / `RequiredNames`, under the comment _"moved to the earliest point it can run
  (§1.8: declaration > compile > launch)"_. This adds a third caller, not a third mechanism.
- **At the reconcile:** the same `contract.ResolveLaunchInputs` chokepoint, so a provider swap that
  changes the build Workflow's inputs surfaces as an unresolved-provisioning Finding rather than a failed
  launch.

Two consequences to name, because they are work rather than surprises:

1. The declaration check is per-**(Intent, resolved build Workflow)** pair, not per-Workflow: whether
   `placement` is supplied depends on whether that Intent declares it. So `placement` must be **optional**
   in every build Workflow's closed schema.
2. The review expected `estate/workflows/app-tier-build.yaml` to fail this check, since it declares
   `required: [targetSubnet]` which D2's table does not supply. **It does not fail, and the reason is
   worth recording: that Workflow is unreachable.** No provider's `provisions` map names it, and
   `app-tier` is an `Intent/Compute`, so its build resolves through `provisions[Compute]` — to
   `compute-build` or `vsphere-vm-build`, never to `app-tier-build`. Nothing else references it either.
   It is dead estate, and the same smell ADR-0083 D4's sufficiency gate catches for Facet schemas and
   routes with no consumer. Booked below rather than deleted here, because deleting a shipped
   declaration is a separate decision from plumbing values.

   What the check DOES fail is `vsphere-vm-build`, which declared no `inputs` at all — so the
   vsphere-dc environment's Compute builder could not be told which instance to build. It is fixed in
   the same change, which is the check earning its keep on its first run.

### D4 — `Placement` reaches the build unchanged, and stays per-fleet for now

`Placement` (`subnet` / `dmz` / `availabilityZone`, ADR-0059 D5) rides `launchParams` as a nested object,
so a build can honour declared placement instead of hardcoding `region: us-east-1`. Its fields stay
**distinct per topology kind** — ADR-0059 D3 rejected a generic `zone` string so the build never
disambiguates the edge type by resolving the target's kind — and passing the struct through as an object
reopens nothing.

What this ADR does **not** do is make placement vary _within_ a fleet. `ComputeSpec` holds exactly one
`*Placement` for all N instances, so "2 in us-east-1a, 3 in us-east-1b" stays inexpressible. That needs
**keyed** instance identity (`web-use1a-01` rather than positional `web-01`), which changes
`InstanceName`, `instanceOrdinal` and `Excess` — the teardown selection ADR-0114 D4 relies on to decide
deterministically which instance dies. Booked as its own ADR, for the reason ADR-0119 was split out of
ADR-0118: **this one plumbs values, that one changes identity**, and identity owes a migration story for
every fleet already carrying positional names.

D2 is that ADR's prerequisite, and the reason it is sequenced second: per-zone placement is pointless
while the zone cannot reach the build Workflow at all.

### D5 — The existing remediation door serves all three, and names the act

`GET`/`POST /findings/{id}/remediation` already serves drift and withdrawal, and
`resolveFindingLaunch` already checks the Finding's own spec before attempting a Baseline read — so a
provision Finding needs **no new endpoint**, only `launchKind` where the `Framework == "orphan"` test is.

`FindingRemediation.withdrawal` (a boolean) becomes `kind`, carrying `launchKind`. A boolean can
distinguish two acts; three need a name. One door because every case answers "resolve this Finding";
discriminated because creating an instance, converging one and retiring one are not interchangeable, and
an operator about to approve a gate is entitled to know which they are approving (§1.8).

**Still gated, never automatic** (§5 Flow 1). Nothing here launches a build; it makes the gated launch
possible _with the right values_, which is what removes the incentive to reach for the ungated
`startAction` path described in Context.

## Consequences

- A `count: N` fleet actually provisions N instances through the **gated** path. This is a bug fix, and
  the reason this ADR precedes keyed spread.
- **Five** reference/demo build Workflows lose their hardcoded instance identity and declare `inputs`:
  `estate/workflows/{compute-build,app-tier-build,vsphere-vm-build}.yaml` and
  `demos/{ec2-only,vsphere-only}/estate/workflows/*`. The other two hardcoded correlation labels
  (`subnet-build.yaml`, `vlan-build.yaml`) are **singletons**, whose fix is a follow-up below — counted
  separately rather than inflating the number.
- One launch-spec mechanism on Finding instead of an accreting family of per-act pairs.
- `Finding.launchKind` is a new core-owned enum, and `Framework` loses a job it had accreted.
- A dev database that already applied 00042 must be recreated (`task dev:down && task dev:up`).
- Placement still cannot vary within a fleet — now the only thing between the estate and "define
  replicas, per availability zone".

## Alternatives considered

- **`buildWorkflow`/`buildParams` beside `removeWorkflow`/`removeParams`.** Rejected: two pairs today,
  three when ADR-0114's decommission lands, on a Kind whose vocabulary is frozen. A discriminator is
  smaller than the third pair.
- **A real Baseline row for provisioning** so the Baseline mechanism works. Rejected: a Baseline is a
  compiled expectation evaluated against the estate, and there is nothing to evaluate before the instance
  exists (ADR-0058 M1, §1.2).
- **Flatten `ComputeSpec.Params` into sibling inputs.** Rejected on review — see D2. It re-types opaque
  provider params at a second, closed site and reintroduces ADR-0110's provider coupling.
- **Have the build Workflow read the Finding's `diff`.** Rejected for the reason the withdrawal door was:
  `diff` is a display surface, redacted and size-capped, and a launch depending on it breaks silently the
  first time anything caps it.
- **Keyed identity here.** Rejected as scope: it changes instance identity and teardown selection, and
  owes its own migration story and charter review.

## Follow-ups

- **Keyed placement-aware spread** (per-AZ replicas) — the next ADR, with D2 as its prerequisite.
- ~~**Two unreachable build Workflows**~~ — **resolved, and resolving them found a worse defect than
  the unreachability.** Reading the unrouted `subnet-provision` against the advertised `subnet-build`
  showed that D2's singleton parameterization had stopped one level short: `subnet-build` bound
  `{{.launch.name}}` and `{{.launch.params.cidr}}` at the TOP level while its nested
  `spec.forProvider.manifest` — the manifest provider-kubernetes actually applies — still said
  `name: subnet-app-subnet` and `cidr: 10.30.0.0/24` **literally**. Both declared Intent/Subnet
  resolve to this one Workflow, so building `dmz-subnet` would have applied a ConfigMap under
  **app-subnet's name carrying app-subnet's CIDR** — not "dmz-subnet is unbuildable" but "dmz-subnet
  silently overwrites app-subnet". `vlan-build` had the same shape latently (`vlan-net-vlan`,
  `vid: "100"`), harmless only while exactly one Intent/Vlan exists. Both are now fully bound,
  `vid` included, from the Intent's opaque params.

  **The claim in 90946f7 that the singleton builders were "all parameterized" was therefore wrong.**
  The first instinct was to call this unguardable — core cannot know which of a provider's opaque
  literals are per-instance, since §1.5 makes the manifest body deliberately opaque. That was wrong
  too, and in a useful way: core does not need to know which literals are per-instance, because it
  already knows every declaration's identity. **D3 now refuses a builder containing any literal that
  belongs to a declaration of the kind it builds** — the Intent's name (substring, since names get
  composed into `subnet-<name>`) or any of its declared `params` values (whole-value, since a
  substring test on `"100"` would collide with unrelated literals) — at any depth, inside opaque
  blobs included. No rendering is needed: anything correctly parameterized is a `{{.launch.*}}` token,
  so a bare occurrence of a declaration's identity is wrong by construction. Both real defects were
  falsified back in and the check named each literal, the declaration it belonged to, and the binding
  that should replace it.

  It stays quiet where it should: the scope is Intents **of the kind this Workflow builds**, so
  `subnet-build`'s `toValue: net-vlan` — an Intent/Vlan name in a Subnet builder — is never compared.
  That literal is a genuine cross-kind topology coupling, and it is placement's to answer, not this
  check's.

  `subnet-provision.yaml` is **deleted**: `subnet-build` now does everything it did and is the
  advertised builder, while `subnet-provision`'s inputs (`subnetName`/`cidr`) could never match what
  the reconcile sends, so no `provisions` map could ever legally name it. Keeping both would leave two
  Workflows that build a subnet with only one reachable — and the unreachable one still projected a
  valid `stratt.intent/singleton` label, so hand-launching it would have **resolved a provisioning
  Finding without going through the bound provider at all**, quietly making the CapabilityBinding
  non-authoritative about who builds a kind. That is the same hole as the `startAction` follow-up
  below, reached from a different direction.

- **`estate/workflows/app-tier-build.yaml` is unreachable — and it is the ONLY consumer of declared
  placement, which makes this bigger than dead estate.** `placement` is declared as an input by all
  seven build Workflows and bound by **none** of them; `BuildLaunchParams` faithfully derives it and
  every advertised builder ignores it. `app-tier` declares `placement.subnet: app-subnet` and is the
  one Intent in the estate that does — so **its declared placement currently reaches nothing**, and
  the one Workflow that would project the `placed-in` edge is the one nothing routes to.

  It cannot simply be folded into the shared `compute-build`, and the reason is structural:
  `BuildLaunchParams` omits `placement` entirely when the Intent declares none, the substituter fails
  closed on an unknown field, and ADR-0083 D5 rules out conditionals — so `{{.launch.placement.subnet}}`
  in a shared builder would break every unplaced Compute Intent (`web-fleet` today). Exactly the trap
  the `{{.launch.params.cpus}}` note above records.

  There is a second, sharper problem underneath: `additionalProperties: false` **forces** every builder
  to declare each param the reconcile might send, so an accepted-but-unbound input is structurally
  required and indistinguishable from a consumed one. A build Workflow can validate `placement`,
  accept it, and silently drop it, and nothing at declaration, compile, launch or dispatch says so —
  a §1.8 gap in the parameter plane itself, not in any one Workflow. `app-tier-build` is therefore
  **kept, not deleted**: it is the only worked example of the projection this needs, and deleting it
  would erase the evidence. All of this is handed to the keyed-placement ADR, which owns placement.

- **Fleet-shape provider params still live in the build Workflow.** `vsphere-vm-build` keeps literal
  `cpus`/`memoryMB` because `Intent/Compute.params` is opaque and provider-shaped: the reference
  estate's Compute Intents carry EC2's `region`/`instanceType`/`ami`, so binding
  `{{.launch.params.cpus}}` would fail CLOSED against exactly the Intents that exist (the substituter
  refuses an unknown field, and there are no conditionals — ADR-0083 D5). The honest fix is an Intent
  per substrate boundary (ADR-0113 D2 already makes environment that boundary), not a template that
  guesses. Recorded because it is the one remaining place a build Workflow holds configuration.
- ~~**The singleton path**~~ — **done**, via `provision.SingletonLaunchParams`. Its params differ for a
  real reason: no ordinal, and a per-kind `(intentKind, name)` correlation key so a Subnet named
  "web-dmz" can never collide with a Compute instance of that name (§2). `subnet-build`, `vlan-build`
  and `vsphere-subnet-build` are all parameterized, and D3's check applies the singleton set to them.

  **Extending it uncovered three more defects, and the singleton cascade was dead end-to-end:**

  1. **`opentofu-network` advertised `Subnet: opentofu-subnet-build`, a Workflow that was never
     written.** `validateProvisions` only checks the entry is non-empty, and the reconcile copies the
     name onto the Finding — so with `estate/capability-bindings/provisioning.yaml` explicitly
     selecting that provider for Subnet, **`app-subnet` and `dmz-subnet` could not be built at all**,
     and `app-tier`'s placement depends on `app-subnet`. D3 now refuses a dangling `provisions` target
     at declaration: a provider must not advertise a capability it has no Workflow for.
  2. **`vsphere-subnet-build` projected `stratt.intent/subnet`** — a key nothing reads;
     `ProvisionedSingletons` matches only `stratt.intent/singleton`. A successful build therefore
     produced a portgroup that never resolved its own Finding, so the reconcile re-surfaced the same
     gated build forever. Both failure modes look like success, which is why it survived. D3 now
     refuses any build Workflow that hardcodes a `stratt.intent/*` label instead of forwarding
     `{{.launch.labels}}` — guarding the class, not the instance.
  3. **`subnet-build` and `vlan-build` hardcoded one singleton's name and key**, so only the named one
     was ever buildable — the fleet defect in singleton clothing.

  **Consequence recorded rather than buried: ADR-0112 D1/D6 was DECLARED BUT NEVER DELIVERED.** D1
  declares the `opentofu-network` Actuator with `provisions: {Subnet: opentofu-subnet-build}` and D6
  makes its binding "the first live explicit capability-binding" — but `opentofu-subnet-build` was
  never written. ADR-0112 knew it would be awkward and said so: an Actuator apply is workspace-scoped,
  so the Workflow "needs either a synthetic/anchor View for the actuation" or a redesign. That
  unresolved note is where it stopped. The advertisement and its binding are removed, so Subnet
  resolves to Crossplane's `subnet-build`, which exists. `provides: [provisioning]` stays on the
  Actuator: OpenTofu genuinely provides the class for the enablement gate; what it lacked is a per-kind
  builder.

  **A correction to this ADR's own first telling, since it is a document of record.** An earlier
  revision attributed the undelivered builder to ADR-0110 D5. That is wrong. D5 demotes Crossplane to
  _one bindable provider_ and names **awsec2** (`create-subnet`) as Subnet's end state; OpenTofu
  appears nowhere in it. ADR-0112 then **superseded** that half, explicitly rejecting "provision via
  awsec2-native `create-subnet` (my earlier plan)" as unrealistic, and substituted OpenTofu. So the
  live estate carried the third plan's advertisement with none of the three plans' Workflows: D5's
  awsec2 builder unbuilt because 0112 replaced it, 0112's OpenTofu builder unbuilt because it stopped
  at a design note, and Crossplane — the provider both ADRs were demoting — quietly still doing the
  work while a binding pointed away from it. The removal restores what actually runs.

  **And the deeper point, which the three-plan sequence above makes hard to avoid: "which tool builds
  Subnet" is not an ADR-shaped decision at all.** Three ADRs picked three different winners for one
  slot, and the estate ended up running none of them. Which plugin performs which operation is a
  LANDSCAPE
  choice (§1.5) — per provider, per Intent kind, per environment — and the seam for it already ships:
  `CapabilityBinding.Entries` selects a provider per `(capability, intentKind)` and is environment-scoped
  (ADR-0110 D3, ADR-0113 D2, ADR-0057). `capability.Resolve` auto-binds the sole verified builder and
  makes >1-with-no-binding a §2.4 error, so the operator declares the choice exactly when there is a
  choice to make. A repo-level "X over Y" verdict pre-empts that, and pre-empted it wrongly here: it
  named a winner that could not build anything, and because the estate's binding then pinned the loser
  out, the kind became unbuildable rather than falling back — correctly so, since §2.4 forbids an
  implicit cross-provider fallback.
  So the two removals are unconditionally right (a provider must not advertise a builder it lacks, which
  D3 now enforces), and re-adding them is an OPERATOR's act in a binding once `opentofu-subnet-build`
  exists — not a fourth ADR picking a fourth winner. What is genuinely additive, and only noted here:
  `capability.Resolve`
  is reached solely from `desiredstate/provisioning_resolve.go`, so this per-operation selection exists
  for `provisioning` alone — `certissuer`, `keycustodian`, `statestore` and `ipam` have no equivalent
  per-operation binding surface yet. Widening it is additive to this ADR and belongs with whichever class
  first ships a second provider.

- **ADR-0114's decommission path** should carry `launchKind: decommission` through the same field rather
  than growing its own. That is the first real test of whether D1's discriminator holds, and if it needs
  a fourth member the "stays at three" claim was wrong.
- ~~**The ungated `startAction` route**~~ — **closed at both doors.** `POST /runs {action}` is the one
  path to real infrastructure with no Workflow and therefore no approval Step; its only authz is the
  CredentialRef `use`-check. That is right for the operations it was built for, but an Action carrying
  a `stratt.intent/*` label is not one of them: the label is how the reconcile decides a declared unit
  EXISTS, so projecting it there creates infrastructure with no approval on the path **and resolves the
  provisioning Finding that would otherwise have demanded one** — the gate is not merely skipped, it is
  retired by the thing that skipped it. It also sidesteps the CapabilityBinding, which is the estate's
  authority on which provider builds a kind (the same hole `subnet-provision` opened from the
  declaration side).

  Two guards, because one door cannot see the other. **At declaration**, D3 now requires every
  advertised builder to carry a human approval gate Step — all five already did, which is the reason to
  pin it: the invariant held by convention, and convention is what this check keeps finding broken. **At
  launch**, `startAction` refuses any `stratt.intent/*` key at any depth in an Action's params. The walk
  is depth-first over decoded JSON rather than a check on a known field, because the param that carries
  projected labels is named by each plugin's own Contract — core must not need to know the spelling to
  police its own namespace, and §2 makes that namespace core's.

  Worth recording how close this came to being another inert mechanism: the pure function was tested
  and the CALL was not. Stubbing the call out changed nothing observable, because no test exercised the
  handler. It now has one, running against the real `awsec2/create-vm` Contract with a nil Store and
  Temporal, so a guard that stops returning early fails loudly instead of passing quietly.

  What is deliberately NOT closed: an operator may still declare an ungated Workflow and launch it by
  name. That is a general property of Workflows, not a provisioning hole, and narrowing it belongs with
  whatever decides which Workflows are privileged — not here.

## Review record

| Finding                                                                                                                                         | Disposition        | Resolution                                                                                                                                                  |
| ----------------------------------------------------------------------------------------------------------------------------------------------- | ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **V1 (§1.1/§1.5)** — flattening `ComputeSpec.Params` re-types opaque provider params at a closed second site, reintroducing ADR-0110's coupling | accepted, verified | D2 passes `params` through as one opaque object                                                                                                             |
| **V2 (§2.4)** — flattening collides with core-generated keys (`params` is open) with no exclusive-claim rule                                    | accepted           | Dissolved by V1's fix — namespaced, the collision cannot be expressed                                                                                       |
| **V3 (§1.2)** — `WriteProvisionFinding`'s `DO UPDATE` refreshes only `diff`, so an open Finding would serve first-pass params                   | accepted, verified | D2 states the derived-vs-immutable asymmetry and the required `DO UPDATE`                                                                                   |
| **V4 (§2)** — `launchKind: withdraw` is a third word for an act the chain already calls `remove`                                                | accepted           | `remove`                                                                                                                                                    |
| **V5 (§2.4)** — `launchKind` overlaps `Finding.Framework`, which `resolveFindingLaunch` already branches on                                     | accepted, verified | D1 makes `launchKind` the single branch point; `Framework` reverts to the compliance tag                                                                    |
| **C1** — "`web-02` can never be built" is false                                                                                                 | withdrawn          | Context now says no _gated, declared_ path builds it, and names the ungated `startAction` route it drives people to — a stronger claim, and a new follow-up |
| **C2** — "seven Workflows" is five plus two singletons this ADR defers                                                                          | corrected          | Five, with the singletons counted separately                                                                                                                |
| **C4** — the compute branch does not emit `correlationLabel`                                                                                    | corrected          | D2 says it is derived and that this ADR adds it                                                                                                             |
| D1's closed enum is defensible, not ontology creep (spine record, plugin cannot extend)                                                         | flagged, accepted  | Kept, with the "argue membership" rule and V5's single-branch-point fix                                                                                     |
| D3 is not a second dialect — `checkTriggerLaunchInputs`/`checkBlueprintParamNames` already do this                                              | flagged            | D3 reuses them; the per-(Intent, Workflow) and optional-`placement` consequences are named                                                                  |
| D4's ADR-0059 D3 claim verified against `provision.Placement`                                                                                   | flagged, accurate  | Unchanged                                                                                                                                                   |
| §1.2 — routing the correlation label through launch params does not change the writer (ADR-0058 M1)                                             | flagged, upheld    | Stated in D2                                                                                                                                                |
| Permanent non-goals — clean                                                                                                                     | flagged            | —                                                                                                                                                           |
