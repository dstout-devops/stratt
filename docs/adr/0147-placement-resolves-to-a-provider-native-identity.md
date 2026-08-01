# ADR 0147 — A declared placement resolves to a provider-native identity, or the build is not offered

- **Status:** **Proposed** (2026-07-29, steward) — implemented and **live-proven, both directions**.
  Charter review by hand (this session's rules bar the subagent); §1.1/§1.2/§1.5/§1.8/§2.4 answered
  inline. **No new dependency, no new Named Kind, no schema change, no migration.**
- **Date:** 2026-07-29
- **Deciders:** steward
- **Charter sections:** §1.5 (core stays content-blind — it never learns what an AWS subnet id is),
  §1.2 (the graph holds what is built; an unbuilt target is simply absent, and that absence is the
  signal), §1.8 (declaration > compile > launch — the failure being moved off the far side of an
  approval gate), §2.4 (two addressable identities for one placement is refused, never tie-broken),
  §1.1 (the launch interface stays provider-agnostic; the provider-native value sits beside it)
- **Reconciles with:** ADR-0059 D3/D5 (placement's CaC home is the Intent; distinct fields per
  topology kind), ADR-0123 D2 (placement emitted COMPLETE, empty where undeclared — `subnetRef`
  follows that rule and D1 explains why it must), ADR-0120 D2 (the typed per-unit launch spec this
  extends), ADR-0110 D4 (the RESOLVED/PENDING Finding shape D3 reuses for ordering), ADR-0058 (the
  provisioning seam), ADR-0146 (which made the region coordinate load-bearing and **booked this as
  PRV-2**), ADR-0145 (the network builder whose output this consumes), ADR-0047 §1 (`identitySchemes`
  as a provider's declared identity vocabulary — D2 reuses it rather than adding a field),
  ADR-0137 D3 (`contractsOnly` demo admission — why the shadowing copies exist at all).

## Context

`placement.subnet` holds an **Intent/Subnet name**, because a name is the only thing Git can hold.
`compute-build` bound it straight into the `subnetId` param of `awsec2/create-vm`, which puts it in
`RunInstancesInput.SubnetId`, where AWS requires `subnet-0abc…`. The Workflow's own comment claimed
this was "where the two meet — provider-agnostic name in, provider-native id out". **No translation
existed.** Measured against floci: `--subnet-id app-subnet` → `InvalidSubnetID.NotFound`.

This is the next link past PRV-1. That fix got the _key_ right (`subnetId`, not `subnet`) and left
the _value_ wrong, so "a Linux host **on that network**" — the join the whole network leg exists for
— had never worked. Nothing caught it because the value is a non-empty string: every load-time check
and every Contract passes it, and only the substrate can tell that `app-subnet` is not a subnet id.

## Decision

### D1 — The reconcile resolves the declared NAME to a provider-native ref, and emits both

`placement` gains `subnetRef` beside `subnet`. The reconcile reads the built subnet's identity keys
and resolves the ref before the build Finding is written; builders bind `{{.launch.placement.subnetRef}}`.

**Both, not one.** They are different things and both have consumers: `subnet` is what Git declared
and what placement-drift compares against (ADR-0059 S5); `subnetRef` is what a provider's Action can
actually address. Replacing the name with the id would push a provider-native value into a
provider-agnostic launch interface — the §1.1 seam ADR-0123's comment is careful about.

**Present-and-empty**, following ADR-0123 D2 for the reason that ADR records: template substitution
has no conditionals, so a key that vanished for an unplaced Intent would make
`{{.launch.placement.subnetRef}}` unsafe in a builder shared by placed and unplaced Intents — which
is precisely how `placement` came to be declared by every builder and bound by none.

**The read stays in the controller; `BuildLaunchParams` stays pure.** That function is documented as
"the whole per-instance decision" with no substrate, and resolving a ref needs a graph read. The ref
rides on `Instance`, exactly as `Zone` does and for the same reason.

### D2 — Which identity? The intersection of what the target carries and what the provider declares

`ResolveSubnetRef` intersects the built subnet's identity keys with the resolved provider's declared
`identitySchemes`:

- **exactly one** → that value is the ref;
- **none** → a hard error naming both sides. A provider that cannot address the subnet it is being
  asked to build into cannot be made to by guessing — this is the coherent-on-paper case of a
  Crossplane-built subnet and an awsec2-resolved Compute build;
- **more than one** → **refused**. Two addressable ids for one placement is two answers, and a rule
  for choosing between them is the implicit precedence §2.4 exists to forbid.

**Core stays content-blind (§1.5).** It never learns what `aws.subnetId` means. `identitySchemes` is
already the provider's declared identity vocabulary (ADR-0047 §1) — reusing it means **no new
declaration field**, and it is the same list the write-back governor already gates on, so a provider
cannot claim to address an identity it may not correlate by.

This also makes the co-owned-subnet case work rather than break it: a subnet carrying both
`aws.subnetId` (the awsec2 Syncer, ADR-0112 D5) and `netbox.prefix.id` (the IPAM Syncer) resolves
cleanly, because only one of those is in awsec2's vocabulary.

### D3 — An unresolvable placement means the build is **not offered**, with a reason

When the target is declared but not yet built, the Finding is written with **no launch spec** and a
`placementUnresolved` reason — exactly the shape an unresolved _provider_ already produces (ADR-0110
D4's PENDING), and for the same reason.

The two alternatives are both worse:

- **emit the launch with an empty ref** → the build succeeds and the host lands _unplaced_, on the
  default network, silently. The estate says it is in `app-subnet`; the substrate says otherwise;
  nothing compares them until placement-drift notices, after the fact;
- **emit it and let it fail** → the failure lands after an operator approved the gate, which is the
  §1.8 shape this whole sequence of ADRs keeps closing.

So build ordering is expressed as **observable non-readiness**, not as an ordering engine. "Build
app-subnet first" is a sentence an operator can act on. Stratt does not acquire a dependency graph
between gated builds here, and deliberately: the reconcile is level-triggered, so the dependent build
becomes launchable on the next pass with no orchestration at all.

### D4 — A placement target no `Intent/Subnet` declares is refused at load

Now checkable, because D1 settles what the field _refers to_: resolution goes through the
`Intent/Subnet/<name>` correlation key, so the referent is an Intent/Subnet name. Before D1 the
referent was genuinely ambiguous — the schema description said both "subnet Entity name" and "(an
Intent/Subnet name)" — and a check would have baked in a guess.

The failure it closes is the ordinary typo: `placement.subnet: ap-subnet` is a valid string that
reaches the reconcile, never resolves, and surfaces forever as "build `ap-subnet` first" — advice
nobody can take, pointing at a subnet that does not exist.

**Consequence, stated:** placement into **pre-existing estate** — a subnet nobody provisioned,
observed by a Syncer — is not expressible. That is a real limitation, not an oversight. Supporting it
means a second referent for one field resolved by a different mechanism, which is the ambiguity §2.4
refuses; it needs its own decision. Booked.

### D5 — The divergent demo copies are converged, and a guard now refuses re-divergence

PRV-1's remediation asked for exactly this and it was not written — so PRV-2 hid in the same gap: the
demo's `compute-build` still bound `placement.subnet` after the shipped one moved on, and the demo
would have kept passing against a value the real estate had stopped sending.

`TestDemoWorkflowsDoNotShadowWithDifferentParams` compares **bindings and declared input names** (not
prose — the copies may explain themselves differently) between any demo Workflow and a same-named one
in its plugin's shipped estate. On its first run it found a **second** instance: vcenter's demo copy
of `vsphere-vm-build` declared `ordinal`, `placement` and `params` that no Step binds — inputs
accepted and silently dropped (ADR-0123 D3), invisible because that check only runs for Intents and
the demo declares none.

Shadowing copies remain legal — a demo admits its plugin `contractsOnly` (ADR-0137 D3) because it
wants the seam, not the provisioning story — but they may no longer _differ_.

## Consequences

- **The network→host join works, and is proven in both directions.** `task dev:placement:proof`
  builds an instance into a real subnet against the real EC2 API and asks the API _which subnet it
  landed in_ — the build's own report is not evidence. Then it builds again with the raw Intent name,
  the exact value the estate used to send, and asserts the substrate **rejects** it. Without that
  second half the first would pass just as well against a backend that ignored placement entirely,
  which is the class of false green this repo keeps finding.
- **`app-tier` is now buildable end to end** — it was the reference estate's only placed host and had
  never been buildable, for two different reasons (ADR-0146 D4, then this).
- **A build can now be legitimately un-offered.** That is new: a provisioning Finding with no launch
  spec previously meant only "no provider". Operators will meet it as "build the subnet first".
- **The `identitySchemes` list is now load-bearing in a second way.** It already gated write-back
  correlation; it now also decides whether a provider can be placed into a given target. A provider
  that under-declares it will fail to resolve placement — visibly, with both sides named.

## Follow-ups

1. **Placement into pre-existing (observed, undeclared) estate** — D4's stated limitation. Needs a
   decision about a second referent, not a quiet relaxation of the load check.
2. **`dmz` and `availabilityZone` get no ref**, deliberately: `availabilityZone` is already a
   substrate-native string (ADR-0123 D1 makes it identity-forming), and **no builder consumes `dmz`
   at all**. Adding unconsumed `Ref` fields would be the accepted-and-dropped shape D5 just deleted
   two instances of. Add one when a builder binds one.
3. **The resolution is Compute-and-singleton symmetric but only Compute has a live consumer.**
   `SingletonLaunchParams` emits `subnetRef` too, so a subnet-in-a-subnet or a DMZ-placed singleton
   resolves the same way; nothing binds it yet.

## Alternatives considered

- **Resolve inside `BuildLaunchParams`.** Rejected: it is pure by design and the resolution needs a
  graph read. Threading a store into it would make the per-instance decision untestable without a
  substrate, which is exactly what its purity buys.
- **Have the plugin resolve the name.** Rejected outright — plugins have no graph read path (§1.2:
  they propose typed values, the core governs), and giving one a lookup would be a far larger hole
  than the bug it closes.
- **A first Step in the build Workflow that looks the subnet up via an Action.** Same objection: the
  Action would need graph access. It also puts a resolution every builder needs into every builder.
- **Emit the ref and let an unresolved one be empty.** Rejected — D3. Silent unplaced builds are the
  worst available outcome, because the graph and the substrate then disagree with nothing comparing
  them.
- **A dependency graph between gated builds, ordering subnet before host.** Rejected as
  over-machinery for a level-triggered reconcile: non-readiness plus the next pass gets the same
  result, and the operator keeps the decision (§5 Flow 1).
- **A new `placementScheme:` field on the provider declaration.** Rejected — `identitySchemes`
  already says what a provider can address, and a second list would be free to disagree with the
  first.
