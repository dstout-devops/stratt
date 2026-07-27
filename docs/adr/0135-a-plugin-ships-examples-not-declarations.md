# ADR 0135 — A plugin ships examples and conformance, never declarations; and remediation binds to a capability, not a name

- **Status:** **Proposed** (2026-07-26, steward) — **DESIGN ONLY, nothing implemented.** Charter review
  by hand (this session's rules bar the subagent); §1.2/§1.4/§1.5/§2.4 answered inline. **No new dependency.**
- **Date:** 2026-07-26
- **Deciders:** steward
- **Charter sections:** §1.5 (sovereign contracts — a dependency targets a CLASS, never a named
  provider), §1.4 (boring spine, pluggable everything), §1.2 (projections, never a second truth),
  §2.4 (claim types, no implicit precedence), §1.8 (never hide diagnosis)
- **Reconciles with:** ADR-0033 (`packs/` — content as reviewable data that is not a Named Kind),
  ADR-0046 (the sovereign plugin port), ADR-0103 (Actuators as CaC declarations), ADR-0104 (capability
  classes — the vocabulary this extends), ADR-0105/0110/0111/0113 (capability resolution and binding),
  ADR-0116 (the demo library), ADR-0134 D2 (**the rule this generalises**: a declaration may never
  widen the authority of the team that owns it), and the
  [ownership study](../research/multi-team-ownership.md) item **(B)** — content as a published surface

## Context

The question that started this: _should plugins deliver plugin-specific Actions, Blueprints, and demos?_

The answer is three different answers, and the split is the point. Getting there required correcting
one thing this session had asserted confidently and wrongly.

### What ships today

`grep` over `plugins/*` returns code and a Dockerfile. **No plugin ships a single estate artifact.**
Actions already split cleanly: the plugin _implements_ `helm/deploy`; the **estate** declares it
(`estate/actuators/helm.yaml` → `actionNames: [helm/deploy]`) alongside `facetNamespaces`,
`environments`, and credential bindings. `packs/` (ADR-0033) is the one precedent for reusable
content-as-data, and it is explicitly _not_ a Named Kind.

### The asymmetry nobody had named

Stratt already has the capability machinery this project wants. `types/capability.go` says it plainly:
_"a dependency targets the class, never a named provider (§1.5), so the provider stays a swappable
transport"_ and _"Vendors named below are example provider #1s, never 'the' provider."_ Provisioning
proves it end-to-end — `estate/capability-bindings/provisioning-vsphere.yaml` swaps `Compute` from
awsec2 to vcenter in one environment, with no code change and no Intent edit.

**Remediation does not work that way.** A Blueprint route ends in `remediationWorkflow:
web-server-configure` — a Workflow _by name_, which names an Actuator by name, which (since ADR-0134)
names a content project by name. So:

| Path                              | Binds by                                                     | Swappable?                         |
| --------------------------------- | ------------------------------------------------------------ | ---------------------------------- |
| Intent `requires: [provisioning]` | capability **class** → operator binding → `provisions[kind]` | ✅ proven live, vcenter ↔ awsec2   |
| Blueprint route → remediation     | a hard-coded **Workflow name**                               | ❌ estate-specific by construction |

One half of the intent layer is provider-agnostic and the other half is not. That asymmetry — not
packaging — is what actually blocks a Blueprint from ever being shared.

### The correction

This session first argued _"a plugin can't ship a Blueprint because it binds to this estate's Views and
Intents."_ **The Views half is simply false**, and the type says so: `types/blueprint.go` mentions a
View exactly once, in a comment noting that route matching is _"intersected with the Assignment's View
membership."_ **A Blueprint names no View.** Views arrive via the Assignment.

So a Blueprint is _already_ mostly portable: `for:` is an Intent kind, `match:` is capability-scoped
Facet predicates, `observe:` is a Facet namespace and path. Every one of those is provider-agnostic.
**`remediationWorkflow` is the sole vendor coupling**, and the objection was much weaker than stated.
The error is recorded rather than edited away, because it is the reason the decision below is about a
missing seam rather than about packaging.

### A premise this ADR drifted from, written down so the next reader does not

**The AWX arc is a migration surface, not an integration.** Nine ADRs of `ansible.*` mirroring
(ADR-0086, ADR-0127–0133) make AWX the most deeply modelled external system in the repo, and that
depth reads like a partnership. It is not one. The charter is explicit: Stratt is _"the successor to
AWX/AAP"_; the exodus is _"importer maps templates→Step presets … `/api/v2` façade keeps existing
tooling alive **during cutover**,"_ with the import target _"frozen at 24.6.1 forever"_ (§6). AWX is
frozen upstream — no releases since 24.6.1 (July 2024) — which is the market condition the whole
thesis rests on.

The endgame is that **the ansible plugin does everything AWX did and AWX is switched off.** The two
existing surfaces serve that: the Syncer reads the estate in so it can be seen, `materialize/` imports
it out into Stratt CaC. Neither makes AWX a dependency.

This is written into an ADR about _plugin delivery_ because the drift is easy and this session made
it: the moment you go looking for a second provider of a capability, AWX is sitting right there,
already modelled, one Action from bindable. D4 is where that goes wrong, so it is answered there.

### Prior art

- **Crossplane Configuration packages** are the closest analogue: an OCI package of Compositions +
  XRDs with dependency resolution, so infrastructure abstractions ship across teams and clusters. The
  mapping onto Stratt is nearly 1:1 — **XRD ≈ capability class, Composition ≈ capability-binding,
  Claim ≈ Intent** — and the piece Stratt lacks is the _shippable unit_.
- **CSI / CNI** are the canonical statement of the discipline: a standardized contract the kubelet
  delegates to, so a platform team picks Calico or Cilium or Flannel without the orchestrator knowing.
- **The failure mode is documented too**, and it is the one to design against: Crossplane users report
  _"significant repetition in compositions with no way to avoid it,"_ needing a separate composition
  per scenario. Abstraction explosion is the tax on getting this wrong.

## Decision

### D1 — A plugin ships **examples** and **conformance**; an estate ships **declarations**

The seam is **authority**, and this is ADR-0134 D2's rule generalised past Ansible: _a declaration may
never widen the authority of the team that owns it._

An Actuator declaration carries `facetNamespaces` (a write ceiling), `environments`, and credential
bindings. **A plugin shipping its own Actuator declaration is a plugin granting itself facet write
scope** — the same defect as a tenant-writable Actuator, with the vendor in the tenant's chair. So the
current split is not an omission to fix; it is the boundary working.

What a plugin MAY ship:

- **Examples** — inert Blueprints, Intents, Workflows and Views, as data, in a `packs/`-shaped
  location. They are **copied into an estate and reviewed**, never reconciled in place. The copy step
  _is_ the review step, and review is where authority gets granted.
- **Conformance** — a suite proving the plugin honors the port: DryRun is a real no-op, facts correlate
  by the declared identity scheme, failures surface rather than fold (ADR-0051 MF1–MF7).

What a plugin MAY NOT ship: any declaration the desired-state engine reconciles.

### D2 — `remediates`: the capability-shaped counterpart to `provisions`

`types.Actuator` has `Provisions` (Intent kind → build Workflow) and `Decommissions` (Intent kind →
teardown Workflow). It has **no remediation counterpart**, which is exactly why remediation binds by
name. Add one:

```yaml
# estate/actuators/ansible-platform-baseline.yaml
provides: [configmgmt]
remediates:
  Application: web-server-configure # Intent kind → THIS provider's convergence Workflow
```

Symmetric with `provisions` in shape, resolution, and failure modes — it reuses `capability.Resolve`
unchanged, including its fail-closed behaviour: zero providers → PENDING Finding, ≥2 with no binding →
AMBIGUOUS compile error, never a silent tiebreak (§2.4).

**Keyed by Intent kind, and that is a narrowing worth stating** — it is the one place this design
could force rework, so it is argued rather than assumed. A Blueprint has MANY routes: co-management
(ADR-0083 §3) fans out by _adding_ them — a cert route, a config route, a package route — and today
each names its own Workflow. Under `remediates`, routes disambiguate by **capability**, not by facet:
a `certissuer` route resolves through OpenBao's `remediates[Application]` while a `configmgmt` route
resolves through Ansible's. That covers co-management exactly.

What it does **not** cover is two routes on the same Intent kind needing the same capability but
_different_ Workflows. Keying by facet namespace instead would, and it is rejected: a provider's
`remediates` is a statement about what it can CONVERGE (an Intent kind), while a facet is what a route
OBSERVES to detect drift — keying the provider's capability by the observer's concern would make every
provider re-declare itself per Facet. The escape hatch is the answer: that route keeps
`remediationWorkflow` (D3).

### D3 — A Blueprint route may name a **capability**, not a Workflow

```yaml
routes:
  - observe: { namespace: app.config, path: port, equals: "{{.spec.port}}" }
    claim: exclusive
    remediationCapability: configmgmt # ← resolved per environment, not baked in
```

> **LIMITATION found in implementation (2026-07-26): an EE-Job Actuator cannot be a capability
> provider.** Capability resolution counts only **verified** providers, and verification means
> fetching the plugin's Manifest over its **dial address**
> (`connectorregistry.verifyProvider`). An EE-Job Actuator has no dial address by construction —
> ansible is subprocess-only because of the GPLv3 boundary (§3) — so it is permanently
> `provUnverifiable`, and a route naming its capability compiles to _"no verified provider builds
> Intent/Application for capability `configmgmt`"_.
>
> This was shipped into `estate/blueprints/web-server.yaml` and broke three Assignments on every
> real floor. **Every unit test still passed**, because they resolve through a fake resolver; it
> surfaced only on booting `task dev:connector-e2e`. The estate is back to `remediationWorkflow`
> and the ansible Actuator no longer declares `provides`, since a provider nothing can verify is
> a phantom kept alive by the log line refusing it.
>
> So D3 stands **for gRPC-addressed providers** and is unusable by EE-Job ones until ADR-0104 D1's
> booked hardening makes them verifiable. That gap is the real blocker for D3's own motivation —
> the flagship configmgmt provider is exactly the kind of tool that runs as a subprocess.

`remediationWorkflow` is **kept, not deprecated** — the same call ADR-0134 D4 made for `params.play`.
A single-provider estate naming its Workflow directly is clearer than an indirection that resolves to
one answer, and pretending otherwise trades one awkwardness for another. The rule: **name the
capability when more than one provider could serve; name the Workflow when the estate has decided.**
The two are mutually exclusive on a route, refused at compile.

This is what makes an example Blueprint shippable: with the remediation leg capability-shaped, every
field in a Blueprint is provider-agnostic, and the estate supplies the binding.

**A REJECTED motivating case, recorded because it was drafted and is wrong.** An earlier version of
this section argued the payoff was an AWX bridge: bind `configmgmt → awx`, run remediation on AAP,
migrate content, then rebind to the native Actuator. It reads well and it contradicts the charter.
See D4 — Stratt is AWX's **successor**, and its exodus is _import + façade_, not proxy-execution.

The real justification does not need AWX at all, and is stronger for it: a Blueprint whose remediation
leg names a provider **cannot be shared**, which is D1's entire purpose. The indirection exists so an
estate this project never sees can bring a provider this project never shipped, and consume the same
Blueprint. That is the "pluggable everything" of §1.4 applied to the one edge of the intent layer that
still hard-codes a name.

### D4 — `configmgmt` as a capability class — and **AWX is not a provider of it**

ADR-0104's rule is that a new class ships with its first provider. `ansible` is provider #1, and for
now the only one: `plugins/{puppet,chef,salt}` are **Syncers** — no `Apply`, no `jobCommand`; they
observe and never converge.

**The tempting mistake, named so nobody repeats it.** AWX/AAP looks like the obvious provider #2: this
repo mirrors nine `ansible.*` namespaces, its job templates and credentials are Entities, ADR-0026
ships a façade of its API. It is a config-management execution engine by definition, and it is one
small Action — a launch — away from being bindable. An earlier draft of this ADR proposed exactly
that, and built a migration story on top of it.

**It is wrong, and the charter says why.** Stratt is _"the successor to AWX/AAP"_ (§ Positioning), and
the migration path is named: _"**AWX exodus:** importer maps templates→Step presets, inventories→Views,
workflows→Workflows; `/api/v2` façade keeps existing tooling alive **during cutover**. The import
target is **frozen at 24.6.1 forever**"_ (§6). Read that carefully — the exodus is **import + façade**:
existing tooling keeps making AWX-shaped calls, but **Stratt executes**. A launch Action inverts it,
leaving **AWX executing** with Stratt as a caller. That is not a bridge out; it is a supported
integration with the product being replaced, and every estate that binds to it acquires a dependency
this project intends to delete.

AWX is already integrated in exactly the two directions replacement needs, and neither is a provider:

- **Read in** — the Syncer mirrors the automation estate so it can be seen, audited, and reasoned about.
- **Import** — `materialize/` converts a job template into Stratt CaC. That is the exodus direction.

The endgame is that **the ansible plugin does everything AWX did**, and AWX is switched off. Nothing in
this ADR should make that harder, so the launch Action is **withdrawn, not booked**. The `configmgmt`
seam does not depend on it.

**Which leaves `configmgmt` justified on its own terms, and it is:**

1. **Blueprint shareability (D1's whole purpose).** A remediation leg that names a Workflow names an
   Actuator names a project — an example whose most important field every adopter must rewrite.
2. **Downstream pluggability.** The charter's thesis is that every tool is a plugin. An estate this
   project never sees may bring a puppet or chef Actuator; the class is what lets it consume a
   Blueprint unchanged. The class exists so **others can plug in**, not so we can swap.

Neither reason requires a second provider to exist in this repo, which is the honest position: the
indirection resolves to one answer today, and that is precisely why `remediationWorkflow` is kept (D3).

### D5 — Two kinds of proof, and they are not the same artifact

- **ADR-0116 demos stay scenario-level**: four turnkey floors proving end-to-end value. That shape
  does not scale to hundreds of plugins, and it should not try.
- **A plugin ships conformance**, which is nearer a test suite than a demo: does this plugin honor the
  port? It scales precisely because it asserts the _contract_ rather than a scenario.

A per-plugin scenario demo is the abstraction-explosion failure in another costume — one demo per
plugin per scenario, none of them exercising the spine.

## Charter alignment

- **§1.5.** D2/D3 move the last name-bound edge of the intent layer onto a capability class. A
  dependency targets the class; the provider stays a swappable transport.
- **§1.2.** D1 is the projection rule applied to distribution: desired state is the operator's, in
  their Git. A vendor-shipped reconciled declaration would be desired state with two authors.
- **§2.4.** Capability resolution keeps fail-closed semantics — PENDING or AMBIGUOUS, never a silent
  tiebreak. No precedence field is introduced.
- **§1.4.** Core learns no tool. `remediates` is a declared map, resolved by the existing pure resolver.
- **§2.** No new Named Kind. `remediates` is a field; `configmgmt` is a capability token.

## Consequences

- **A Blueprint becomes shareable** — every field provider-agnostic, the binding supplied by the estate.
  That is the unit "norms/patterns/examples" actually needs.
- **Examples are copied, not installed**, so adopting a plugin costs a review. Deliberate: the copy is
  where authority is granted, and a one-click install would be the Argo `.spec.project` circumvention
  wearing a vendor badge.
- **`configmgmt` has exactly one provider, and no second is planned in this repo.** D3's indirection
  therefore resolves to a single answer today — real, and the reason `remediationWorkflow` is kept. The
  class earns its place from shareability and downstream pluggability (D4), not from a swap we intend
  to perform.
- **AWX gains nothing here, deliberately.** No launch Action, no binding, no dependency an estate could
  acquire on a product this project is the successor to. The AWX surfaces stay what the charter's
  exodus makes them: read-in and import.
- **`remediates` is keyed by Intent kind, which is coarser than a route.** Co-management is covered
  (routes disambiguate by capability), but two same-capability routes on one Intent kind collapse to
  one Workflow. The escape hatch covers it; if that case turns out to be common, the keying is the
  thing to revisit, and it is called out here so the revisit is a decision rather than a surprise.
- **Abstraction explosion is the risk to watch** (the documented Crossplane tax). The mitigation is
  D3's "name the Workflow when the estate has decided" — an escape hatch that keeps the indirection
  optional rather than mandatory.
- **D1 must not foreclose (B).** "Copied, not installed" is a statement about AUTHORITY, not about
  transport. If the ownership study's publish gate later ships a signed, reviewed install path, it
  satisfies D1 the moment the install is an act of the ADOPTING estate rather than of the publisher.
  Nothing here should be read as ruling that out.
- **This does not decide the publish gate.** Ownership-study item **(B)** owns the project unit, its
  verbs, and publication; examples here are files in a repo, copied by hand. Whichever lands second
  reuses the other's machinery — and (B) should not be pre-empted by a packaging decision made here.

## Alternatives considered

- **Plugins ship live declarations, reconciled in place.** The adoption story people ask for, and
  rejected: an Actuator declaration carries a write ceiling, so this is a vendor granting itself facet
  scope (D1). It is also a second author of desired state (§1.2).
- **Capability-scoped Blueprints that route only to their own plugin's Actions.** The steelman this
  session flagged as untested. Rejected because it inverts the dependency: a Blueprint pinned to its
  own provider is the name-binding again with extra steps. D3 gets the same safety from the class.
- **Leave remediation name-bound; ship examples anyway.** Cheapest, and it ships an example whose
  single most important field an adopter must rewrite — an example that does not work.
- **A `remediation` capability class per Intent kind** (`certremediation`, `configremediation`).
  Rejected as ontology creep (§9): the kind is already a key _inside_ `remediates`.
- **Key `remediates` by Facet namespace instead of Intent kind.** It would let two same-capability
  routes remediate differently (see Consequences). Rejected: a provider's `remediates` states what it
  can CONVERGE; a Facet is what a route OBSERVES to detect drift. Keying the provider by the observer's
  concern makes every provider re-declare itself per Facet, and breaks the symmetry with `provisions`
  that lets `capability.Resolve` be reused unchanged.
- **Make AWX `configmgmt` provider #2 via a launch Action.** Drafted in an earlier version of this ADR
  and **withdrawn**, which is why it is recorded here rather than deleted. It is one small Action away,
  it would make the class demonstrably multi-provider, and it is the wrong direction: the charter's
  exodus is import + façade — _Stratt executes_ — while a launch Action leaves _AWX executing_ with
  Stratt as its caller. Every estate that bound to it would acquire a dependency on the product this
  project exists to succeed. The seam does not need it, so it is not built.
- **Fold this into ownership-study (B).** Rejected on the study's own instruction — _"each needs its
  own argument; none should be folded into another."_ (B) decides publication; this decides what may be
  published at all.

## Implementation — not started

In dependency order, and deliberately not begun: this is a decision to argue with first.

1. `CapConfigMgmt` in `types/capability.go` + the closed-set entry (D4).
2. `Remediates map[string]string` on `types.Actuator` + `actuatorFile` + `ValidateActuator` (the
   half-declaration rule: `remediates` requires `provides` to include the class).
3. `remediationCapability` on `BlueprintRoute`, mutually exclusive with `remediationWorkflow`, refused
   at compile with both set.
4. Compiler: resolve `remediationCapability` through the existing `capability.Resolve` at compile,
   stamping the resolved Workflow onto the Baseline exactly as `remediationWorkflow` does today.
5. The reference estate's `web-server` Blueprint moves to `remediationCapability: configmgmt`, which is
   the worked example the convention will be copied from.
6. `plugins/<name>/examples/` + a conformance suite shape, with one plugin doing it first.

### Traps

- **`remediates` is resolved at COMPILE, not at Run time.** A Baseline carries a concrete Workflow, so
  descent (§1.8) still shows one answer, and an estate that rebinds recompiles rather than silently
  changing what a Finding offers.
- **Do not let an example become a declaration by being in the wrong directory.** `ParseDir` walks
  named subdirectories; examples must live where it never looks (ADR-0134 proved this property for
  `estate/ansible/`).
- **`configmgmt` has one provider.** Do not write tests or docs implying a swap that cannot currently
  be demonstrated, and do not reach for AWX to manufacture one — see D4. The indirection has one answer
  today and that is fine; it is there so an estate outside this repo can supply the second.
- **A rebind must recompile.** Changing a capability-binding changes what a Finding offers as its
  remediation. If the compile cadence does not notice a binding change, an estate rebinds and the
  Baselines keep offering the old provider's Workflow — the silent-stale failure §1.8 exists to
  prevent. Verify this before believing step 4 is done.
- **`remediates` is a ceiling-free field, unlike its neighbours.** `facetNamespaces` bounds authority;
  `remediates` only routes. Do not let review treat naming a Workflow here as granting anything — the
  Workflow's own Steps carry their Actuator's ceiling, exactly as they do today.
