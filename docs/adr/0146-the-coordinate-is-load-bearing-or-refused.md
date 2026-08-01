# ADR 0146 — A provider coordinate is load-bearing or refused: the region leg, without re-deciding where a region lives

- **Status:** **Proposed** (2026-07-29, steward) — implemented. Charter review by hand (this session's
  rules bar the subagent); §1.1/§1.2/§1.5/§1.8/§2.4 answered inline. **No new dependency, no new
  Named Kind, no new declaration kind, no schema change.** This ADR is mostly deletion of hazards
  and one new load-time check.
- **Date:** 2026-07-29
- **Deciders:** steward
- **Charter sections:** §1.5 (`params` is provider-shaped and opaque to core — D5 is the decision
  _not_ to erode that), §1.2 (a projected label must not assert an unobserved fact; the substrate is
  the authority), §1.8 (declaration > compile > launch — a failure discovered after an approval gate
  is the shape being closed), §2.4 (no implicit precedence: a core-injected value must not silently
  overwrite a declared one), §1.1 (type the seam, not the world)
- **Reconciles with:** **ADR-0142 D3/D4** — the four-way Cell/Site/Environment/coordinate table and
  the resolved "an Environment carries no inheritable facts". **This ADR does not revisit either**;
  D1 below records why the planned work was already decided and what remained. ADR-0118 D1
  (`rejectEnvKeyedValues` — flat values per environment), ADR-0057 (`environments` membership),
  ADR-0110/0113 (environment-scoped capability bindings), ADR-0111/0112 D3 (ipam injection —
  D3 below closes its precedence hazard), ADR-0120 D2 + ADR-0123 D2/D3 (the launch interface and the
  two checks D4 completes), ADR-0145 (the network builder this region now reaches),
  ADR-0058 (the provisioning seam).

## Context

The planned next step was "an Environment carries the region coordinate and scoped Intents inherit
it". **It had already been decided against**, by ADR-0142 D4, on a §1.2 argument: a coordinate must
be observed or caused, never computed. That ADR also concluded, explicitly, that the region
coordinate "dissolves too" — a flat `params.region` per Intent is already the compliant shape, and
"define a region from code" is satisfied by composition: an `environments/` declaration,
environment-scoped capability-bindings, and flat params.

So the honest question was not _where should a region live_ but **does the coordinate the estate
already declares actually do anything?** It did not.

- **`awsec2/create-vm` required a `region` param that selected nothing.** The EC2 client is built
  once from the pod's own `Config.Region`; the param never reached it. It travelled on to become the
  `aws.region` **label** on the build's projection — so an Intent declaring `us-east-1` against a
  provider serving `eu-west-1` created the instance in `eu-west-1` and told the graph it was in
  `us-east-1`. A projected label asserting a fact nobody observed, and a false one (§1.2).
- **The network builder never saw a region at all.** `opentofu-subnet-build` (ADR-0145) forwarded no
  region, so a subnet was built in whatever the module defaulted to while the Compute Intents placing
  hosts in it named something else, and nothing compared them.
- **`app-tier` could not be built.** It declared `params: {tier: app}` while `compute-build` binds
  `{{.launch.params.region}}`, `instanceType` and `ami`. `params` is opaque to core (§1.5) so nothing
  typed it, and `template.Substitute` fails **closed** on a missing field — at launch, after an
  operator approves the gate.
- **`validateEnvironmentRefs` did not check Actuators**, the one kind where the omission bites
  hardest (see D6).

## Decision

### D1 — Nothing about _where a region lives_ is redecided here

ADR-0142 D3's four-way table (Cell · Site · Environment · provider coordinate) and D4's "an
Environment carries no inheritable facts" stand unamended. This ADR completes the composition they
prescribe rather than proposing an alternative to it.

Recorded because the pull is strong and will recur: inheritance looks like the obvious de-duplication
of `region: us-east-1` repeated across four Intents. It is the shape ADR-0118 D1 forbids with the
indirection moved one file over — the same document meaning different things per scope — and D4 found
a stronger objection underneath. **Repetition here is the compliant shape, not a missing
abstraction.** The estate comments now say so at each occurrence, so the next reader meets the
reasoning rather than the smell.

What this ADR changes is that the repeated value is now **load-bearing**: it selects the substrate,
or the build is refused.

### D2 — A declared coordinate must select the substrate, or the build is refused

`awsec2/create-vm` refuses when `params.region` differs from the region the provider serves, before
`RunInstances` is called.

**Refused rather than honoured by building a per-region client**, which looks like the more capable
fix and is the worse one. This plugin's Syncer enumerates `Config.Region` and nothing else, so an
instance built outside it would be created, projected once by the build, and then never seen again by
the source that owns its Facets. Building where you cannot observe is a bigger hole than the wrong
label it would close.

**Multi-region is composition, not a parameter**: one provider declaration per region, each scoped to
an environment, each dialing a pod configured for that region. That is exactly the mechanism ADR-0142
D4 pointed at, and D2 is what makes the coordinate participate in it instead of decorating it.

The check lives **in the plugin**, not at admission, and that is a §1.1 boundary rather than a
compromise. A `region` field on the Manifest would put a substrate-specific concept into a
content-blind spine. The provider's region is the plugin's own configuration, and the only component
that can legitimately compare it to a provider-shaped param is the content expert holding both.

**No second region is declared in the reference estate**, deliberately. There is no second AWS
provider pod to verify one against, so the declaration would sit PENDING forever — the
declared-but-never-runs shape this session has closed four times. The composition is documented; it
is not pretended.

### D3 — The coordinate reaches the network builder, and a core-injected var is never silently overwritten

`opentofu-subnet-build` forwards `{{.launch.params.region}}` into the module, so a subnet is built in
the region its Intent names. `params.cidr` is still deliberately **not** forwarded (ADR-0145 D8: the
allocator decides the range) — region and CIDR are different kinds of value, and only one of them is
a claim.

Separately, `prepare()` merged the ipam handle's CIDR straight over any declared `stratt_ipam_cidr`.
Two answers to "which range", resolved by a rule nobody wrote down. The reserved `stratt_` var prefix
is now **refused** with a diagnostic naming where the value actually comes from — §2.4's
exclusive-fails rule, not a precedence order.

### D4 — An Intent must be able to supply every param its builder binds, checked at load

`checkAdvertisedWorkflow` gains the third of three checks that together make "this Intent can be
built" a load-time fact:

1. the reconcile's generated launch keys can fill the builder's declared `inputs` (ADR-0123 D3);
2. every declared input is bound by some Step (ADR-0123 D3);
3. **every key the builder binds from inside the opaque `params` exists in the Intent** (new).

The gap was structural, not accidental. `params` is provider-shaped and opaque (§1.5), so no Contract
covers it, while the substituter fails closed — which puts the failure at launch, _after_ the
approval. The check found `app-tier` unbuildable, and the class it closes has now produced four
separate defects in this estate (a declared `placement` reaching no provider; an advertised builder
that did not exist; a `provisions` entry with no Workflow; this).

**Every candidate builder is checked, not the bound one**, for the reason the sibling check already
gives: which provider wins is runtime state Git cannot see. A builder that keeps provider-shaped
values literal — vcenter's, precisely so it stays compatible with Intents shaped for another
substrate — constrains nothing here, which is the right outcome for that design and confirms the rule
is not over-tight.

### D5 — Core does **not** compare coordinates across Intents

A tempting fifth check: a host placed in a subnet must declare the same region as that subnet.
**Rejected.** `params.region` is a provider coordinate, opaque to core by ADR-0142 D3's own table.
A core check comparing them would make `region` a core concept in the one place the charter says it
is not — the first crack in "type the seams, not the world", and the next request would be
`availabilityZone`, then `vpcId`.

The coherence is enforced where it belongs: **the substrate is the authority (§1.2)**, and it refuses
the mismatch itself. Verified rather than assumed — a `RunInstances` against a subnet in another
region returns `InvalidSubnetID.NotFound`. Core's contribution is that both coordinates now reach
their providers at all, which is D2 and D3.

### D6 — The environment-reference guard covers every kind that carries the filter

`Actuator` was missing from `validateEnvironmentRefs`. It is the kind where the omission costs most:
`assembleProvisioningProviders` filters build providers by `InScope(a.ScopedEnvironments(), env)`, so
an Actuator scoped to a typo'd environment provides nothing in _every_ environment, and the only
symptom is a build Finding resolving to no provider — reported as "no verified provider", which sends
the reader to the plugin registry rather than to the typo one file away.

ADR-0142 D2 built that check over a hand-written list, arguing it should stay explicit "so that adding
a kind with `environments` and forgetting it here is a visible omission in review". The reasoning is
sound; the outcome was not — `Actuator` was already missing when the sentence was written. The list
stays explicit, and a test now **derives** the expected set by reflection over `Declarations`, so the
next kind carrying `environments` cannot be forgotten quietly. A test carrying its own second list
would have failed in exactly the same way as the first.

## Consequences

- The four `region:` declarations in the reference estate now decide something. Change one and a
  build is refused rather than silently landing elsewhere.
- **`app-tier` is buildable for the first time** — as far as its own declaration goes. See the
  follow-up: the next link is broken.
- One provider pod serves one region. That is a real constraint, stated rather than discovered:
  multi-region needs one provider declaration per region, environment-scoped.
- The estate's repeated `region: us-east-1` is now annotated at each site with why it is repeated,
  so the inheritance question is answered where it will be asked.

## Follow-ups

1. **PRV-2 — `placement.subnet` carries an Intent NAME into a param that requires a provider-native
   ID.** `compute-build` binds `subnetId: "{{.launch.placement.subnet}}"`, and the Intent declares
   `placement.subnet: app-subnet`. Verified against the real API: `RunInstances` with
   `--subnet-id app-subnet` returns **`InvalidSubnetID.NotFound`**. The Workflow's own comment claims
   this is "where the two meet" — provider-agnostic name in, provider-native id out — and no
   translation happens. So "a Linux host **on that network**" is still broken, one link past where
   PRV-1 left it.

   Not fixed here because it is a **design decision, not an omission**: resolving the name means the
   reconcile reading the graph for the subnet Entity correlated to `Intent/Subnet/app-subnet` and its
   `aws.subnetId` identity key — and `BuildLaunchParams` is documented pure. Worse, it raises
   **build ordering**: what should happen when the placement target is declared but not yet built? A
   launch that silently proceeds unplaced is the failure mode to avoid, so the answer is probably a
   blocked/observable Finding rather than an unplaceable launch — which is a decision about
   dependencies between gated builds, and deserves its own ADR rather than a hurried parameter.

2. **A load-time check that `placement.subnet` names something resolvable** belongs with PRV-2, not
   before it: whether the target may be an observed-but-undeclared subnet (pre-existing estate nobody
   provisioned) is part of that decision, and guessing now would bake in the wrong assumption.

## Alternatives considered

- **An Environment carrying the region, inherited by scoped Intents** (the planned approach).
  Already rejected by ADR-0142 D4 — see D1. Re-deriving it would have shipped an inheritance
  mechanism against a decision made two ADRs earlier for a stronger reason than the one that would
  have been re-argued.
- **A per-region EC2 client so one pod serves every region.** Rejected — D2. It closes a label bug by
  opening an observability hole: the Syncer looks in one region.
- **A `region` field on the plugin Manifest, verified at admission.** Earlier in the pipeline, which
  is normally the right direction (§1.8), and rejected on §1.1/§1.4: it puts a substrate-specific
  concept into the content-blind port. The nearest legitimate version is a provider declaring an
  opaque, provider-owned attribute set — a real design, and not needed to make this coordinate
  load-bearing.
- **A core check that a host's region matches its subnet's.** Rejected — D5. The substrate already
  enforces it, and core reading `params.region` costs more than the check is worth.
