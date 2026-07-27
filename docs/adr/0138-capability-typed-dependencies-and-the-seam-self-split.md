# ADR 0138 — A module depends on capabilities: the seam/self split, and the Step-level gap

- **Status:** **Accepted** — D1–D5 implemented (2026-07-27). Charter review
  by hand; §1.1/§1.5/§2.4 answered inline. **No new dependency, no new Named Kind.**
- **Date:** 2026-07-26
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams, not the world), §1.4 (boring spine, pluggable everything),
  §1.5 (sovereign contracts; a plugin never mints a capability's meaning), §2.4 (no implicit precedence)
- **Reconciles with:** **ADR-0104 (capability dependencies — D1 is the rule this ADR applies
  uniformly)**, ADR-0105 (`requires:` → `CapabilityHandle`), ADR-0110 (Intent `requires:` →
  `Provisions` → build Action), ADR-0114 (`Decommissions`), **ADR-0047 §4 (rung-1 is core-shipped —
  narrowed here, see D3)**, ADR-0135 D3 (`Remediates`), **ADR-0137 D4/D5/D7 (corrected here)**

## Context

Three things happened during the ADR-0137 migration that look unrelated and are the same problem.

1. **`linux-onboard` was ruled a "composition" and moved to `estate/`** because it spans `awsec2` →
   `ansible`. A CI check was written to enforce "a plugin's declarations reference only that plugin."
2. **ADR-0135 D3's capability-routed remediation could not reach its own flagship provider** — ansible
   is an EE-Job Actuator with no dial address, so it is permanently unverifiable.
3. **ADR-0137 D5 (a plugin owns its Contracts) collided with ADR-0047 §4** (rung-1 is _hand-written,
   core-shipped_), and the collision was reported as unresolvable without a decision.

The common error is treating **inter-module dependency** as something to be minimised or routed around,
when the project already decided it is first-class. ADR-0104 D1, in its own words:

> A declaration requires `keycustodian`, **never** `openbao-transit`. The capability class is the
> sovereign contract; the plugin that fulfils it is a swappable transport. … This is §1.5 made
> structural, and it is the whole **anti-Jenkins move**: coupling to a contract cannot version-rot the
> way coupling to a named provider does.

A plugin **should** depend on capabilities it cannot supply. Ansible converges hosts; it cannot create
them. A certificate plugin needs a CA. An HSM-backed one needs `keycustodian`. Those are permanent
properties of the domain, and the vocabulary already names them — `provisioning`'s own definition reads
_"provision machines **other plugins target**."_

## Decision

### D1 — The rule is about NAMES, not about dependency, and it is symmetric

> **Any declaration — core's or a plugin's — may depend on capability CLASSES freely. No declaration
> may name a PROVIDER it does not own.**

ADR-0137 D4 stated half of this ("a core composition may depend only on capabilities, never on a
plugin's name") and the migration mistakenly enforced a different, stricter rule on the other half
("a plugin's declarations reference only that plugin"). That rule is wrong: it forbids exactly the
dependency the capability system exists to express, and it pushes every honest cross-capability
lifecycle into core, which is how a "boring spine" grows a tool-shaped bulge.

So `linux-onboard`'s defect was never that it spans two plugins. It is that it **names**
`awsec2/create-vm`.

### D2 — The gap: capability resolution exists at every layer except the Step

Capability resolution is shipped in three places, all class-typed, none naming a provider:

| layer           | declares                            | resolves through | to                 | ADR  |
| --------------- | ----------------------------------- | ---------------- | ------------------ | ---- |
| Intent          | `spec.requires: [provisioning]`     | `Provisions`     | a build Action     | 0110 |
| Actuator        | `requires: [statestore]`            | resolve Action   | `CapabilityHandle` | 0105 |
| Blueprint route | `remediationCapability: configmgmt` | `Remediates`     | a Workflow         | 0135 |

There is **no Step-level equivalent.** A Workflow Step takes a concrete `actuator:` or `action:`. So a
Workflow whose legs cross capabilities — provision, then converge; issue a cert, then install it — has
no way to say _"whoever provides `provisioning`"_, and must name someone.

**That gap, not any principle, is why `linux-onboard` lives in `estate/`.** It should be recorded as a
missing mechanism, never as evidence that cross-capability lifecycles belong to core.

The shape follows the three above rather than inventing anything: a Step names a capability class, the
compiler resolves it once against verified providers plus the estate's capability-bindings, and the
compiled artifact carries a **concrete** Action — so one-click descent still shows one answer (§1.8) and
a rebind recompiles. Exactly ADR-0135 D3's shape, one layer down.

### D3 — Contracts split by SEAM vs SELF, which is what ADR-0047 §4 was really protecting

ADR-0047 §4 forbids a plugin to _"introduce, mutate, satisfy, or shadow a hand-written, **core-shipped**,
`ContractRef`-pinned rung-1 Facet/Contract."_ Read it against its four verbs rather than its noun:

- **A seam contract** describes seams BETWEEN modules — what one module may rely on another to mean.
  `capabilities/` (the class handle shapes), `facets/` (graph vocabulary a Blueprint route observes),
  `intents/` (the payload shape of core Named Kinds). A plugin that could change these **can** shadow
  and mutate what others depend on. §4 is fully engaged, and ADR-0104 D1 says the same thing from the
  other side: _"a plugin never mints a capability's meaning."_ **These stay core-shipped, permanently —
  not as a migration debt but as the definition of a seam.**
- **A self contract** describes only the plugin itself: its own `params` shape.
  `actuators/<plugin>.input.vN`, `actions/<plugin>/<verb>.input|output`. Nothing else reads it; it
  constrains one Actuator's Steps. Such a plugin cannot shadow, cannot satisfy another's contract, and
  "mutating" it changes only what its own Steps may say — with core still pinning it and blocking
  drift. **§4's threat model is not engaged.**

The census is not a coincidence: **22 of 152** documents are self contracts (`actuators/` 13 +
`actions/` 9); the other 130 are seams. ADR-0137 D5 is therefore **narrowed, not withdrawn**: a plugin
may own its **self** contracts; seam contracts are core's and always were.

> **TWO CORRECTIONS FROM IMPLEMENTATION (2026-07-27).**
>
> **The census above is wrong by ~4×.** "22" counts 13 actuator FILES plus 9 action DIRECTORIES —
> but those directories hold 78 files. The real self-contract set is **91 documents**, not 22. The
> decision stands; it was simply sized against a number that mixed files and folders.
>
> **Not every self-shaped contract is a self contract.** Three families are named NEUTRALLY, and a
> neutral name means more than one plugin may implement the surface — which makes the document a
> seam by D3's own definition. `cert-issuer` is the clearest: `main.go` says outright that the
> Actuator name is neutral "so a step-ca plugin could implement it", so `actuators/cert-issuer.input`
> and `actions/cert-issuer/*` describe a swap point, not one vendor's params. Moving them into
> `plugins/openbao/` would weld a neutral contract to a single vendor — precisely backwards. The
> same holds for `actions/adopt/*` and the retired `actuators/webhook.input`.
>
> **83 documents moved; 8 stayed.** The shipped set went 152 → 69 (seams + the three neutral
> families); 77 pinned entries are now estate-resident (83 files, with ansible's seven version
> siblings collapsing to one name).

### D4 — Residence follows ownership; verification does not move

For self contracts, core stops embedding and instead pins at registration with blocking drift — the
mechanism ADR-0022 already ships for rung-2/3 and that `contract.ValidateDocument` already evaluates.
§1.5 is upheld: pinned, hash-verified, drift blocking.

**The residual risk, stated rather than buried:** a self contract becomes estate-resident instead of
binary-resident, so an operator could relax their own plugin's param schema. That is not an escalation
— the same operator already writes the Actuator declaration, including its `facetNamespaces` write
ceiling, which is a strictly more powerful knob — but it IS a real change and it is the reason this is
scoped to self contracts only.

### D5 — A capability provider must be verifiable without a dial address

ADR-0135 D3 shipped a route that its own flagship provider could never satisfy: verification fetches a
Manifest over a dial address, and an EE-Job Actuator has none by construction (§3, the GPLv3 boundary).
Every unit test passed because they resolve through a fake.

A capability system whose verification step structurally excludes subprocess tools cannot express
`configmgmt` — the class whose first provider is, by charter, a subprocess. So verification needs a
second admissible form for dial-less providers (ADR-0104 D1's booked hardening), or capability routing
remains available only to gRPC plugins and must say so loudly at declaration time rather than at
compile time on a live floor (§1.8).

> **IMPLEMENTED (2026-07-27) — the second admissible form.** A dial-less provider is verified against
> its **declared mechanisms** instead of a Manifest it cannot have. There is nothing to fetch and there
> never will be, so the question becomes whether the claim is backed by anything: a declaration
> carrying a `provisions`/`remediates`/`decommissions` entry or an `actionNames` list has committed to
> something, and a bare `provides:` with nothing behind it stays refused — at load, in the diff.
>
> **It is not self-certifying.** The mechanism it checks lives in a different part of the tree from
> the claim, and the estate loader independently requires a named Workflow to EXIST. It is
> nonetheless weaker than asking a running binary, so the verdict records its **basis**
> (`manifest` | `declaration`) and the API exposes it — two verdicts that both read `verified=true`
> are not equally strong, and a surface that blurs them hides diagnosis (§1.8).
>
> **It is honestly weaker in a second way**, and this bounds the decision: an **Action-shaped** class
> needs a Manifest-advertised `implements` (ADR-0140 D1), which a subprocess has no way to supply. An
> attested provider of such a class verifies and then fails **closed** at resolution, naming the
> missing advertisement. So attestation admits the **Workflow-shaped** classes — which is exactly what
> `configmgmt` and `provisioning` are, and exactly the set the old rule locked out.
>
> The earlier load-time gate that demanded a dial ADDRESS is replaced, not weakened: it now demands a
> MECHANISM, which is what it was really enforcing. `ansible-platform-baseline` declares
> `provides: [configmgmt]` again and `estate/blueprints/web-server.yaml` is capability-routed again.
> **Verified on a live floor** (`task dev:connector-e2e`), which is the only thing that caught the
> original defect: the registry logs `provider attested from its declaration (dial-less)` and all four
> Assignments compile with `unrouted=0`.

## Charter alignment

- **§1.5.** This is the discipline stated once and applied everywhere: depend on the contract, never the
  transport. D3 sharpens rather than weakens it by naming which contracts are the sovereign seam.
- **§1.4.** D2 keeps the spine boring: without a Step-level class, every cross-capability lifecycle
  accretes in core, and core slowly learns what tools do.
- **§1.1.** The seam/self split IS "type the seams, not the world" applied to the contract tree itself.
- **§2.4.** Resolution stays fail-closed with no implicit precedence; a class with two verified
  providers and no binding is AMBIGUOUS, never a silent winner.

## Consequences

- **A plugin can express what it needs.** Ansible requires `provisioning`; a cert plugin requires
  `certissuer`; an HSM-backed one requires `keycustodian` — none naming a provider.
- **`linux-onboard` and `vsphere-subnet-build` become movable** once D2 lands, and are its tests. Until
  then they stay in `estate/` because the mechanism is missing — never because a cross-capability
  lifecycle belongs to core.
- **Everything NOT blocked on D2 has moved.** A plugin's own projection Views and Baselines go with the
  plugin: the nine `awx-*` Views and six `awx-*` Baselines to `plugins/ansible-automation/estate/`
  (they select and assert over kinds only that Syncer projects, and no Assignment binds them), and the
  salt Emitter + Trigger to `plugins/salt/estate/`. This is the case ADR-0137 step 4 explicitly left
  open — "a plugin that ships a View of the kinds ITS Syncer projects is a different case" — and the
  test is the same one as everywhere: a View that is a GROUP Assignments bind to stays; a View that is
  a plugin's own projection moves.
- **Two rules replace one wrong one:** capability dependency is encouraged; provider naming is refused.
  `plugins:boundary` check 3 already tests the right thing and now says the right thing.
- **The `contracts/**` gate exception narrows permanently**, not temporarily: `facets/`, `intents/`,
  `capabilities/`, `policy/`, `outputs/` are never a routine plugin edit.
- **ADR-0137 D5's blocker dissolves for 22 documents and hardens for 130.**

## Alternatives considered

- **Keep "a plugin's declarations reference only itself."** Rejected: it forbids the dependency the
  capability system exists for, and pushes tool-shaped lifecycles into the spine.
- **Let a Step name a provider when "the estate has decided."** This is what happens today by default,
  and it is why `linux-onboard` cannot move. ADR-0135 D3's rule is the right one — name the capability
  when more than one provider could serve, name the concrete thing when the estate has decided — but
  today the Step layer offers only the second half, so the choice is not actually available.
- **Move all 152 contracts to plugins.** Rejected by D3: a plugin owning `facets/app.config` could
  change the shape of state a Blueprint observes. That is precisely ADR-0047 §4's threat.
- **Leave all 152 in core (withdraw D5).** Tempting, and safe, but it keeps `contracts/` a core tree
  every plugin author edits — the ADR-0137 D2 violation that started this — for no protection, since a
  self contract protects nothing but itself.
- **Give EE-Job providers a synthetic dial address.** Rejected: verification would then be checking a
  fiction, which is worse than declining to verify and saying so.

## Implementation — complete

1. **D1's rule** — already enforced correctly by `plugins:boundary` check 3; its message and the
   `estate/README.md` rationale were corrected alongside this ADR.
2. **D5's honesty half first** — refuse `remediationCapability` at DECLARATION time when no admissible
   provider shape exists, so §1.8 does not depend on reading a running floor's logs.
3. ~~**D2, the Step-level class**~~ — **Shipped, and it needed TWO ADRs**, exactly as this item
   predicted. The nested-Workflow primitive became **ADR-0139** (steps 1–4: parent link, concrete
   form, class form, `linux-onboard` converted) and the Action- and Actuator-shaped halves became
   **ADR-0140** (D1–D4). The shape below is what was built, essentially unaltered — resolve in an
   Activity, fail closed on PENDING/AMBIGUOUS, check params against EVERY candidate. The original
   analysis:

   **larger than this ADR first assumed**. A provider's per-kind maps (`Provisions`/`Remediates`/`Decommissions`) resolve to a
   **Workflow**, not an Action — `awsec2` advertises `Compute: compute-build`, and it is
   `compute-build` that contains `action: awsec2/create-vm`. So a capability-typed Step must invoke a
   **nested Workflow**, and `types.Step` has no such form: it offers `gate`, `policy`, `action`, or
   actuation, and the orchestrator's dispatch switch has exactly those four arms. **D2 therefore
   depends on a primitive that does not exist** — nested Workflow Steps, with their own child
   lifecycle, output binding, failure propagation and descent. That is a substantial orchestration
   feature and deserves its own ADR rather than being smuggled in under this one.

   Shape, when it is built: the Step names a class plus the Intent kind to resolve through; an Activity
   resolves it once before the child starts, so everything downstream — params validation, dispatch,
   §1.8 descent — sees a concrete Workflow exactly as today; fail closed on PENDING/AMBIGUOUS, and
   check params against EVERY candidate provider at declaration time, not just the winner.
4. ~~**D3/D4 relocation of the self contracts**~~ — **Shipped.** 83 documents moved into the plugin
   trees that own them; the shipped set went 152 → 69, and `TestPinsAreStable`'s count moved for the
   structural reason this item names. See the correction block under D3: the census was wrong by ~4×,
   and three neutrally-named families are seams rather than self contracts.

   It also unlocked what it was always for: **port invariant #5 now has something to check.** A plugin
   embeds the documents it ships and pins each ContractRef to their digest, and core refuses a
   provider whose pin disagrees with the document core validates Steps against. That was impossible
   while the documents lived in the core binary — a plugin cannot hash what it does not have.

### Traps

- **"A plugin depends on another plugin" is not the defect; naming one is.** The check tests names. If
  it is ever rewritten to test dependency, it will forbid the design.
- **Do not let a "self" contract quietly become a seam.** The moment anything other than its own plugin
  reads a contract, it is a seam and belongs to core. The test is who READS it, not who wrote it.
- **D5 is the recurring shape, not a one-off:** a seam whose tests all resolve through a fake will pass
  while being structurally unsatisfiable. Where a mechanism depends on runtime state (verification,
  bindings), at least one test must exercise the real store.
