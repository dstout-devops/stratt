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

`remediationWorkflow` is **kept, not deprecated** — the same call ADR-0134 D4 made for `params.play`.
A single-provider estate naming its Workflow directly is clearer than an indirection that resolves to
one answer, and pretending otherwise trades one awkwardness for another. The rule: **name the
capability when more than one provider could serve; name the Workflow when the estate has decided.**
The two are mutually exclusive on a route, refused at compile.

This is what makes an example Blueprint shippable: with the remediation leg capability-shaped, every
field in a Blueprint is provider-agnostic, and the estate supplies the binding.

**The motivating case is migration, not aesthetics.** This is the path the project actually walks, and
it is the strongest argument for the indirection:

1. A Blueprint routes remediation to `configmgmt`.
2. The estate binds `configmgmt → awx`. Remediation runs as an AWX job template, **on AAP, where the
   automation already lives** — nothing has moved yet.
3. Content migrates into Stratt projects, one at a time (ADR-0134).
4. The estate rebinds `configmgmt → ansible-platform-baseline`. **One binding changes. No Blueprint is
   edited, no Intent, no Assignment.**

A name-bound route makes step 4 a rewrite of every Blueprint that mentions the old Workflow. A
capability-bound one makes it a one-line diff in a file whose whole job is to say which provider
serves this environment. That is the same shape as the vSphere binding this repo already ships, and
migration is precisely the moment an estate has two providers for one capability — the condition the
class exists to express.

### D4 — `configmgmt` as a capability class, with its swappability stated honestly

ADR-0104's rule is that a new class ships with its first provider. `ansible` is provider #1.

**And provider #2 is already here — it just cannot be launched.** An earlier draft of this ADR wrote
the gap as "`plugins/{puppet,chef,salt}` are Syncers only, so we need another config tool." That is
true and it is the wrong gap. The obvious second `configmgmt` provider is **AWX/AAP**, which this repo
already integrates deeply: `plugins/ansible-automation` mirrors nine `ansible.*` namespaces, its job
templates, credentials and execution environments are Entities in the graph, and ADR-0026 ships a
façade of its API. It is a config-management execution engine by definition.

What it lacks is **one Action**. The plugin is strictly read-only: a Syncer plus `materialize/`, which
runs the _migration_ direction (AWX job template → Stratt CaC). `grep` for a launch path finds only
comments about AWX's own schedules. So the honest statement is not "there is no second provider" but:

> The second provider is integrated, trusted, and already projected. `configmgmt` becomes a real
> two-provider class the day AWX gains a launch Action — and that Action is small, because everything
> it needs to address a job template is already in the graph.

**Launching does not breach the mirror's boundary**, and the boundary's own wording is the argument:
strattd states the rule as _"§1.2 — AAP stays SoR and keeps executing; the mirror never converts an
already-observed object to Stratt-managed."_ Launching a job template **invokes** an object; it does
not **convert** one. AAP remains the system of record, keeps executing, and keeps owning the
definition — which is not merely permitted by that sentence but assumed by it. The rule that must
survive is the no-conversion rule, and a launch Action does not touch it.

**That Action is NOT decided here.** It needs its own argument — idempotency, how a launched job's
events reach the Run stream, whether a Stratt-launched job is distinguishable in AWX's own audit —
and folding it in would repeat the mistake the ownership study warns about. This ADR books it as
`configmgmt`'s second provider and stops there.

So the honest reading: this buys **structure now, with a named and reachable second provider** rather
than a hypothetical one. Until that Action lands, D3's indirection resolves to a single answer, which
is exactly why `remediationWorkflow` is kept.

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
- **Migration stops being a rewrite.** Binding `configmgmt → awx`, migrating content, then rebinding to
  the native Actuator is a one-line diff instead of an edit to every Blueprint naming the old Workflow.
  This is the consequence that pays for the whole decision.
- **`configmgmt` has one LAUNCHABLE provider today**, so D3's indirection resolves to a single answer
  until AWX gains a launch Action. Real, and the reason `remediationWorkflow` is kept.
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
- **Decide AWX's launch Action here.** Tempting, since it is what makes `configmgmt` real. Rejected:
  idempotency, event forwarding into the Run stream, and whether a Stratt-launched job is
  distinguishable in AWX's own audit are each their own argument — exactly the folding the ownership
  study warns against. Booked, not decided.
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
- **`configmgmt` has one LAUNCHABLE provider.** Do not write tests or docs implying a swap that cannot
  currently be demonstrated. The second provider (AWX) is named and reachable, but until its launch
  Action lands the indirection has one answer.
- **A rebind must recompile.** Changing a capability-binding changes what a Finding offers as its
  remediation. If the compile cadence does not notice a binding change, an estate rebinds and the
  Baselines keep offering the old provider's Workflow — the silent-stale failure §1.8 exists to
  prevent. Verify this before believing step 4 is done.
- **`remediates` is a ceiling-free field, unlike its neighbours.** `facetNamespaces` bounds authority;
  `remediates` only routes. Do not let review treat naming a Workflow here as granting anything — the
  Workflow's own Steps carry their Actuator's ceiling, exactly as they do today.
