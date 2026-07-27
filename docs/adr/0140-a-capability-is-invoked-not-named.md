# ADR 0140 — A capability is invoked, not named: the mapping is declared, never minted

- **Status:** **Accepted** (D1/D2/D3/D4 implemented 2026-07-27; the estate move + boundary ratchet outstanding). Charter review by
  hand; §1.5/§1.8/§2.4 answered inline. **No new dependency, no new Named Kind.**
- **Date:** 2026-07-26
- **Deciders:** steward
- **Charter sections:** §1.5 (sovereign contracts — a plugin never mints a capability's meaning, and
  core never mints a plugin's), §1.4 (boring spine), §1.8 (never hide diagnosis), §2.4 (no implicit
  precedence)
- **Reconciles with:** **ADR-0104 D1 (depend on the class, never the provider — this is the same rule
  pointed the other way)**, ADR-0105/0112 (`requires:` → resolve Action → `CapabilityHandle`), ADR-0111
  (the `ipam` class contracts), **ADR-0083 §3 (routing is a per-capability MAP — the reconcile shape)**,
  ADR-0135 D3 / ADR-0110 / ADR-0114 (the declared per-kind maps), **ADR-0138 D2 (this closes its
  Action-shaped half)**, ADR-0139 (its Workflow-shaped half)

## Context

ADR-0104 D1 is unambiguous about the direction of the coupling:

> A declaration requires `keycustodian`, **never** `openbao-transit`. The capability class is the
> sovereign contract; the plugin that fulfils it is a swappable transport.

The rule is enforced downward — a consumer may not name a provider. It is **not** enforced upward. Here
is how the spine reaches a capability provider today:

```go
// connectorregistry.ResolveCapabilityAction
want := providers[0].pluginIdentity + "/" + capClass + "-resolve"
if !contains(providers[0].actionNames, want) {
    return "", fmt.Errorf("capability %q provider %q does not declare its resolve Action %q", …)
}
```

**Core string-concatenates a name inside the plugin's namespace** — `netbox` + `/` + `ipam` +
`-resolve` — and then requires the plugin to have declared exactly that. The comment calls it _"the
frozen `<plugin>/<op>` convention."_

That is not "core calls the Action directly." It is worse: **core dictates the provider's internal
naming.** A NetBox plugin whose Action is `netbox/allocate-prefix` cannot provide `ipam`, whatever its
Manifest says. The class was introduced so the provider would be swappable; the mechanism reaching it
constrains the provider's internals.

### It is the odd one out in its own family

| mapping                                           | how core learns it                |
| ------------------------------------------------- | --------------------------------- |
| `provisions: {Compute: compute-build}`            | **declared** by the provider      |
| `remediates: {Application: web-server-configure}` | **declared**                      |
| `decommissions: {…}`                              | **declared**                      |
| a class's resolve Action                          | **minted by core, from a string** |

Three declared, one guessed.

### The class is already the interface everywhere except the name

Core validates a capability call's result against the **class** contract (`ipam.output`), not the
Action's own declared output — ADR-0112 D2. So an Action's own contracts are already subordinate when
it is invoked as a capability. Every part of the seam is class-typed except the one thing core
hardcodes.

### Reconcile is the highest-traffic path, and it names providers everywhere

ADR-0083 §3 decided that **routing is a per-capability MAP, never a scalar** — "co-management is
reality": one box gets a config route **and** a certissuer cert route. So a single reconcile fans out
across _several_ capabilities by design.

In this estate it fans out across several capabilities and names a provider in every one of them:

| reconcile leg                                          | shape               | capability-typed?                                                        |
| ------------------------------------------------------ | ------------------- | ------------------------------------------------------------------------ |
| `cert-reconcile` → `actuator: cert-issuer`             | **Actuator** + View | no — and openbao _advertises_ `provides: [certissuer]`                   |
| `vsphere-subnet-build` → `action: netbox/ipam-resolve` | **Action**          | no — the Step's own comment says "the ipam capability's allocate Action" |
| a config route → `remediationWorkflow`                 | **Workflow**        | possible (ADR-0135 D3) but **zero live users**                           |

The certissuer row is the whole problem in one line: the provider declares the capability, the consumer
names the provider, and **nothing connects the two**. Reconcile — the loop this platform runs
continuously — is the least capability-typed path in the estate.

## Decision

### D1 — Core never mints a provider's Action name; the provider declares it

The class→mechanism mapping is a **plugin-internal fact**, and the plugin is the only thing that knows
it. It is advertised, verified, and carried opaquely:

- **Authority** — _may_ this provider serve `ipam`? Governed CaC, unchanged: `provides: [ipam]` on the
  Connector/Actuator declaration. The operator grants; §1.5's "the Manifest is advertisement, the grant
  is truth" holds exactly as it does today.
- **Implementation** — _which_ Action implements it? Advertised in the **Manifest**
  (`ActionDecl.implements: <class>`) and checked at registration against the grant, the same way
  `Manifest.capabilities` is already checked against `provides`.

  > **CORRECTED IN IMPLEMENTATION (2026-07-27).** This bullet originally continued: _"A provider whose
  > Manifest advertises no implementation for a class it claims is a **phantom** and does not count."_
  > That rule contradicts **D3 of this same ADR** and could not be built. D3 says the three Step
  > shapes have three resolutions — and a class reached through a per-kind Workflow map has **no
  > resolve Action at all**. `provisioning` is exactly that shape, so the phantom rule would have
  > rejected every provisioning provider in the estate (awsec2, crossplane, opentofu, vcenter) and
  > taken the whole build path down with them. The drafting error was assuming every class is
  > resolve-Action-shaped, which D3 itself refutes two sections later.
  >
  > **What shipped instead:** a missing implementation is refused where it is **used** —
  > `ResolveCapabilityAction`'s third failure (D5) — never where it is merely absent. Verification
  > records the advertised implementations for the **granted** classes and stays silent about
  > classes routed some other way. An advertisement for an **ungranted** class is dropped, not
  > honored (§1.5): a plugin must not be able to admit itself to a class by claiming to implement
  > it. Two Actions claiming the same class **does** fail verification — that one is unresolvable
  > without a tiebreak, and a tiebreak is what §2.4 forbids.

Core then carries whatever token the plugin advertised, opaquely — exactly as it already carries
`compute-build` without knowing what is inside it. **The `<plugin>/<class>-resolve` convention is
deleted, not documented.**

**Why not full class-dispatch over the port** (core sends `ipam`, the plugin translates): it would
overload `InvokeRequest.action` to carry sometimes a class and sometimes a name, and a field whose
meaning depends on context is the ambiguity §2.4 exists to refuse. The plugin naming its own
implementation achieves the same decoupling with no new wire semantics.

### D2 — The unifying rule: every class→mechanism mapping is DECLARED

> **No mapping from a capability class to the thing that implements it may be derived by core.
> Every one is declared by the provider — per-kind maps in CaC, the class implementation in the
> Manifest.**

`provisions`/`remediates`/`decommissions` and `implements` stay **distinct** rather than collapsing
into one field, because they answer different questions: the per-kind maps answer _"which Workflow
builds/converges/tears down this INTENT KIND"_ and are keyed by kind; `implements` answers _"which
Action IS this class"_ and is keyed by nothing. Merging them would need a synthetic kind for the
keyless case, which is a fiction invented to make one field fit two questions.

What unifies them is not their shape but their direction: **the provider tells core, core never
computes.**

### D3 — Three Step shapes, three resolutions, one rule

ADR-0138 D2's gap has three shapes; ADR-0139 closed one. This closes the other two:

| Step shape                | resolves through                                     | mechanism             |
| ------------------------- | ---------------------------------------------------- | --------------------- |
| `workflow:`               | `provisions`/`remediates`/`decommissions` (per kind) | ADR-0139, nested Step |
| `action:`                 | `implements` (per class)                             | **D1, here**          |
| `actuator:` + `viewName:` | `implements` (per class)                             | **D4, here**          |

### D4 — An Actuator may be selected by capability, and reconcile is why

A reconcile leg that converges an Entity is `actuator:` + `viewName:` + `facetWriteScope:` —
`cert-reconcile` is exactly that shape. Making it capability-typed means a Trigger or Baseline may name
`actuatorCapability: certissuer` and resolve to the bound provider's Actuator.

This is the shape that matters most in practice, because it is the one reconcile uses, and reconcile is
the loop that never stops. It also inherits the hardest constraint: **`facetWriteScope` is the Step's
half of the write ceiling (ADR-0054, grant ∩ scope), and the grant belongs to the resolved Actuator.**
So a capability-typed actuation must be checked against **every candidate provider's** grant at
declaration time, not the winner's — the rule ADR-0135 D3 and ADR-0139 D4 already established, applied
to authority rather than to inputs. A scope that fits one provider's ceiling and exceeds another's is a
write that silently stops happening on a rebind.

### D5 — Fail closed, and say which failure it was

Zero verified providers, or two with no binding, refuses — never a silent tiebreak (§2.4). The
diagnostics stay distinct because the fixes are: _"no verified provider for `certissuer`"_ sends the
reader to the provider; _"2 verified providers, add a capability-binding"_ sends them to the estate.
And after ADR-0138 D5, _"provider declares `certissuer` but advertises no implementation for it"_ sends
them to the plugin — a third failure that today is silently a missing string.

## Charter alignment

- **§1.5.** ADR-0104 D1 said a plugin never mints a capability's meaning. This is the mirror: **core
  never mints a plugin's implementation.** The sovereign contract is the class in both directions.
- **§1.4.** The spine stops carrying a naming convention for a namespace it does not own. A convention
  is knowledge, and knowledge about tools is what the spine is supposed not to have.
- **§1.8.** D5 keeps three distinct failures distinct. Today the "no implementation advertised" case
  surfaces as a name that did not match, which points at the wrong thing.
- **§2.4.** Nothing acquires a precedence rule: the binding names the provider or the resolution fails.

## Consequences

- **A provider names its own Actions**, and the `<plugin>/<class>-resolve` convention disappears.
  Existing providers keep working by advertising the name they already use.
- **Reconcile becomes capability-typed** — the point of the exercise. `cert-reconcile` stops naming
  `cert-issuer` and asks for `certissuer`, which is what ADR-0083 §3's per-capability route map assumed
  all along.
- **ADR-0138 D2 closes** across all three Step shapes, with ADR-0139.
- **`vsphere-subnet-build` becomes movable, and it turns out to be vcenter's already.** vcenter's own
  declaration reads `provisions: {Compute: vsphere-vm-build, Subnet: vsphere-subnet-build}` — so this
  Workflow IS vcenter's advertised Subnet builder. Only ONE of its two legs is the problem: naming
  `vcenter/create-portgroup` is a plugin naming its own provider, which D1 permits and ADR-0138 D1
  always did; naming `netbox/ipam-resolve` is the cross-plugin coupling, and D3 row 2 is what replaces
  it. Capability-type that one leg and the Workflow goes home.

  **A smell this exposes:** ADR-0137 step 4 moved `vsphere-vm-build` and `vsphere-vm-teardown` into
  `plugins/vcenter/` but left `vsphere-subnet-build` in `estate/`, so vcenter's `provisions` map now
  points into two different trees. Nothing catches that — `plugins:boundary` check 3 reads
  `actuator:`/`action:` references, not `provisions:` values. A provider whose advertised mechanisms
  are split across trees is a gap in the ratchet, and closing it is cheap: the same ownership test,
  applied to the per-kind maps.

- **`linux-onboard` still needs ADR-0138 D5** for its ansible leg — capability routing to an EE-Job
  provider remains impossible until dial-less verification lands. **That is the hard prerequisite for
  reconcile too**, since `configmgmt`'s first provider is a subprocess by charter.
- **A Manifest field is a port change**, so this is one of ADR-0137 D2's two sanctioned reasons to touch
  core while changing a plugin.

## Alternatives considered

- **Keep the convention, document it better.** Rejected: it constrains the provider's internals, which
  is the coupling the class exists to remove. A documented convention is still core knowing a tool.
- **Full class-dispatch over the port.** Rejected in D1: overloads `InvokeRequest.action` with two
  meanings.
- **Declare the mapping in the estate (CaC) instead of the Manifest.** Tempting for symmetry with
  `provisions`. Rejected: which Action implements a class is not an operator's decision, and putting it
  in CaC would ask an operator to know a plugin's internals to admit it. Authority is the operator's;
  implementation is the plugin's.
- **Collapse `implements` into `provisions`.** Rejected in D2: it needs an invented kind for a keyless
  mapping.
- **Do reconcile first, Actions later.** Rejected: reconcile's legs are Actuator-shaped AND
  Action-shaped, so the reconcile case needs both. Splitting them would ship half a reconcile.

## Implementation — D1/D2 shipped; D3 row 2, D4, D5's Step forms outstanding

1. ~~**`ActionDecl.implements`** in the port + SDK, advertised by one provider and verified at
   registration.~~ **DONE (2026-07-27).** Field 6 on `ActionDecl`; advertised by **two** providers,
   netbox/`ipam` and awss3/`statestore` — both of the classes that are actually resolve-Action-shaped
   today, so the derived path has no remaining users. The granted class→Action map is persisted on
   `graph.capability_provider` (migration 00043) by the leader-only verification pass, **not dialed for
   at resolve time**: routing a Run by whether this replica could reach the plugin in this instant is
   precedence-by-liveness (§2.4), the same hazard `verified` was introduced to remove.
2. ~~**`ResolveCapabilityAction` reads the advertisement** instead of concatenating.~~ **DONE.** The
   `<pluginIdentity>/<class>-resolve` concatenation is **deleted**, with no fallback (see Traps). Three
   distinct failures, because their fixes are: no verified provider → the provider; ≥2 with no binding →
   the estate; verified but advertising no implementation → the plugin. A fourth was found while
   building and kept separate: **advertised but not in `actionNames`** — the plugin named its
   implementation and the estate never admitted it as dispatchable, so there is no dispatch-table entry
   to route to. That is a grant gap, not a plugin gap, and it says so.

   Proven by a test the old mechanism could not have passed: a provider whose implementation is
   `netbox/allocate-prefix` resolves `ipam`. Under the convention that name was unreachable by
   construction — which was the whole complaint.

3. ~~**`action:` Step form** (D3 row 2) — `actionCapability:`, resolved at launch, recorded on the
   Run.~~ **DONE (2026-07-27).** `Step.actionCapability`, mutually exclusive with `action:` (naming
   both would give the Step two answers and a rule to pick is §2.4's implicit precedence). Resolution
   is an **activity**, not workflow-side code — a Temporal workflow function must stay deterministic,
   and a rebind between run and replay would otherwise rewrite history. The Run records the class
   **alongside** the resolved Action: recording only the resolved name would make a capability-routed
   Run indistinguishable from one that named the provider directly, which is the coupling this
   removes.

   Params are validated against the **class** Contract at load and at launch, never the bound
   provider's — check them against the provider and the declaration's validity changes when the
   binding does, which is the opposite of the point.

   `vsphere-subnet-build`'s allocate leg is converted and is the mechanism's first live user. Its
   old comment — _"the ipam capability's allocate Action"_ — was the tell: the author was thinking
   in capabilities and the mechanism could only express a provider. **Its params needed no change**,
   because they were already class-shaped; `actions/netbox/ipam-resolve.input` exists only to mirror
   `capabilities/ipam.input` and is bound to it by a co-fidelity test, so the mirror stops being
   load-bearing on this path.

   **A residue this exposed, and the reason item 5 has NOT been done.** An Action Step must carry a
   `CredentialRef` — it is the Action's only authz gate until the run-grant lands — and a credential
   is provider-specific by nature. So the Step no longer names NetBox's _Action_ but still names
   NetBox's _credential_, and a swap to Nautobot would still edit this declaration. Moving the
   Workflow into `plugins/vcenter/` now would put a netbox reference inside vcenter's tree — the
   exact coupling `plugins:boundary` exists to catch, in a field check 3 does not read. The move is
   gated on that, not on the Action-shaped half, which is closed.

   Note the `requires:`-path asymmetry: `resolveCapabilities` invokes a resolve Action with **no**
   credentials at all (the plugin uses its own configured auth), so capability-typed credentials are
   a Step-form problem specifically. Whether a class should be able to declare its credential
   requirement — or whether a capability Step should inherit the provider's own auth the way the
   `requires:` path does — is an open question this ADR does not answer.

4. **`actuator:` form + reconcile** (D4) — `actuatorCapability:` on Trigger/Baseline, with
   `facetWriteScope` checked against every candidate's grant at declaration.

   > **PREREQUISITE CORRECTED (2026-07-27), and it was not the one this ADR named.** Consequences
   > below says D4 is gated on ADR-0138 D5 because `certissuer`'s provider is "boot-registered
   > today". D5 shipped; D4 was still unbuildable, for a different and more basic reason: **the
   > declared `certissuer` provider and the Actuator serving it were two different objects pointing
   > at the same pod.** `openbao` declared `provides: [certissuer]` and carried **no
   > `facetNamespaces` at all**; `cert-issuer` — the Actuator `cert-reconcile` actually dispatches
   > to — was registered in `main.go` with a grant hardcoded in Go.
   >
   > So capability-typing the reconcile would have resolved to `openbao` and put
   > `facetWriteScope: [cert.identity, cert.expiry]` **wholly outside** the bound grant: the
   > reconcile converges OpenBao and the graph is never updated. That is this ADR's own D4 trap —
   > _"`facetWriteScope` is authority, not configuration"_ — reached not by a later rebind but by
   > the **first** bind, which is worse than the case the trap describes.
   >
   > **`cert-issuer` is now a CaC Actuator declaration** (ADR-0103, following ansible/script), and
   > `certissuer` moved onto it: the provider IS the mechanism, which is D2's rule applied to the
   > Actuator-shaped row. Both declarations advertising the class would be two providers of one
   > class and resolution would fail closed as ambiguous (§2.4) — they are the same pod, but core
   > cannot know that and must not guess. The boot block is deleted, not kept as a fallback: two
   > registration paths for one name collide at §2.4 and make "which grant is live?" unanswerable
   > from Git. The declaration is also strictly **narrower** than the block it replaces — the boot
   > grant carried the cert Syncer's five namespaces because one Go value served both roles.
   >
   > **DONE (2026-07-27), once that was cleared.** `actuatorCapability:` on Trigger and Baseline,
   > mutually exclusive with `actuator:`. Resolution is `ResolveCapabilityActuator` — **Actuators
   > only**, since this form binds something DISPATCHABLE and a Connector is not, and it reads **no
   > advertisement**. That asymmetry with D1 is the design, not an omission: an Action-shaped class
   > needs `implements` because the plugin owns the Action's name inside its own namespace, whereas
   > here the provider **declaration IS the Actuator** — its name is the operator's, granted in CaC,
   > and core derives nothing. It is also why a dial-less provider (ADR-0138 D5) can serve this form
   > and not the Action-shaped one.
   >
   > **The `facetWriteScope` rule is enforced at LOAD against every candidate**, per this ADR's trap.
   > It has to be: a namespace outside the resolved provider's grant is not an error at run time —
   > grant ∩ scope simply drops it at the one governor, so the reconcile converges the backend and
   > the graph quietly stops being updated. That reports as **nothing at all**.
   >
   > **Params too, and for a reason worth recording.** An Actuator-shaped class has no class-level
   > Contract the way an Action-shaped one does (ADR-0111/0112), and inventing one no shipping
   > Contract demands would violate §1.1. So params are validated against **every candidate's own**
   > input Contract — same guarantee (valid whichever provider binds), no invented schema. If a
   > second, differently-shaped provider ever appears, that failure is the signal the class needs a
   > real class-level Contract.
   >
   > `cert-reconcile-web` now reads `actuatorCapability: certissuer`. **Verification is partial and
   > says so:** the load-time half is proven on a live floor (`dev:connector-e2e` boots the full
   > reference estate, so the candidate check runs against the real `cert-issuer` declaration), but
   > the **fire-time resolution is unit-tested only** — that Trigger is `environments: [prod]` and no
   > floor in this repo runs the prod slice with an OpenBao pod.
5. **`vsphere-subnet-build` moves** to the vcenter plugin — the test that the Action-shaped half closed,
   and the fix for a `provisions` map currently split across two trees.
6. **Extend `plugins:boundary` check 3 to the per-kind maps** (`provisions`/`remediates`/
   `decommissions`), so a provider advertising a Workflow that lives outside its own tree is caught the
   way a cross-plugin `action:` already is.

### Traps

- **Do not keep the convention as a fallback.** "Use the advertisement, else the old name" is a silent
  precedence rule (§2.4), and it would keep every provider that never migrated invisibly working until
  the day it did not.
- **`facetWriteScope` is authority, not configuration.** A capability-typed actuation whose scope was
  only checked against the bound provider hands a rebind the power to silently narrow write-back —
  which reports as nothing at all, not as an error.
- **Reconcile fans out across capabilities of DIFFERENT shapes.** A design that handles only the
  Workflow-shaped remediation leg has not made reconcile capability-typed; it has made one of its
  routes so.
- **ADR-0138 D5 gates the flagship.** `configmgmt` and `certissuer` are both served by providers that
  are subprocess or boot-registered today. Building this without the verification half produces a
  mechanism whose most important consumers cannot use it — the ADR-0135 D3 failure, repeated at a
  larger scale.
