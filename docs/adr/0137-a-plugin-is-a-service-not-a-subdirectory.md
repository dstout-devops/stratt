# ADR 0137 — A plugin is a service, not a subdirectory: plane independence and the no-core-change rule

- **Status:** **Proposed** (2026-07-26, steward) — **DESIGN ONLY, nothing implemented.** Charter review
  by hand; §1.4/§1.5/§2.2 answered inline. **No new dependency.**
- **Date:** 2026-07-26
- **Deciders:** steward
- **Charter sections:** §1.4 (boring spine, pluggable everything — "core owns the spine; community owns
  breadth"), §1.5 (sovereign contracts; pinned + hash-verified plugin schemas), §2.2 (the derivation
  ladder), §7.2 (contributor demographics — the cost of a first contribution)
- **Reconciles with:** ADR-0046 (the sovereign plugin port — **this is that decision's packaging
  half**), ADR-0022 (DB-pinned rung-2/3 Contracts — the mechanism D5 extends), ADR-0033 (`packs/`),
  ADR-0103 (Actuators as CaC), ADR-0104 (core-owned capability vocabulary), ADR-0116 (demos),
  **ADR-0135 D1/D5 (partly superseded — see D3)**, ADR-0136 (superseded vs driven)

## Context

[`docs/overview.md`](../overview.md) already makes the promise this ADR enforces:

> The core is content-blind; every tool is a plugin. … **Adding a tool never touches the core.**

**Today that is false**, and the gap is not subtle:

| Where a plugin's parts actually live |                                                                                                                                                                          |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Implementation                       | `plugins/<name>/` ✅                                                                                                                                                     |
| **Input Contracts**                  | `contracts/actuators/*.schema.json` — **`find plugins -name "*.schema.json"` returns nothing.** `ansible.input.v7`, the ansible plugin's own wire promise, lives in core |
| Actuator / Connector declaration     | `estate/actuators/<name>.yaml`                                                                                                                                           |
| Workflows, Blueprints                | `estate/workflows/`, `estate/blueprints/`                                                                                                                                |
| Demo                                 | `demos/<scenario>/estate/…`, split by scenario, with no per-plugin home                                                                                                  |
| A harness to develop against         | **does not exist** — `sdk/` has proto bindings, `mcp`, `secretbroker`, and no mock host                                                                                  |

So a plugin is a subdirectory whose configuration is scattered across three trees. At twenty plugins
that is untidy. At the hundreds this platform is aimed at, it is the end of the road: every plugin
author edits core, every plugin's blast radius is the whole repo, and no plugin can be tested alone.

### The planes

`overview.md` names three planes — **graph** (what is), **execution** (how things change), **intent**
(what teams want) — stitched by the diagnosis spine. The **plugin** surface is the fourth, and the port
that carries it is already sovereign (ADR-0046).

**Each plane must be independently mockable and testable.** That is the property this ADR exists to
protect; the packaging rules below are means to it, not ends. A plane you cannot stand up alone is a
plane you cannot reason about alone, and four planes that can only be tested together are one plane
wearing four names.

### Where the project already agrees

`task plugins:standalone` builds every plugin with `GOWORK=off`, and its comment is this ADR in
miniature: _"Inside the workspace, go.work satisfies imports from sibling modules, so an INCOMPLETE
go.sum stays invisible here and fails only at docker build — far from the change that caused it."_ It
has already caught ansible, vcenter, awsec2 _"and (silently) ten more."_ Monorepo convenience hiding
real breakage is a lesson this repo has already paid for. This generalises it past `go.sum`.

## Decision

### D1 — Everything specific to a plugin lives inside the plugin

```
plugins/<name>/
  cmd/ *.go go.mod            ← the service (already true)
  contracts/                  ← its input/output Contracts (D5)
  estate/                     ← its PROPOSED declarations: actuator, workflows, blueprints, views
  conformance/                ← proof it honours the port
  demo/                       ← its single-plugin demo, if it has one
  Dockerfile
```

A plugin is a **contained service**, packaged as it will run. Nothing about it lives in a tree it does
not own.

### D2 — The acid test, and it is CI-enforced

> **Developing a plugin changes nothing outside `plugins/<name>/`.**

Exactly two exceptions, and both are core decisions by definition:

1. **A new capability class** — §1.5 is explicit that a plugin never mints a capability's meaning.
2. **A port/proto change** — the sovereign contract itself.

Anything else touching core while adding or changing a plugin is a **defect in the boundary**, not a
routine diff. This is a gate, mechanically checkable the way `plugins:standalone` is, and it must be
one — a rule enforced by intention is the thing this ADR is replacing.

### D3 — Locality is not authority (restating ADR-0135 D1)

ADR-0135 D1 answered a **locality** question with an **authority** rule, and got the locality half
wrong: it put examples in a vague "`packs/`-shaped location," which does not solve the scatter at all.
The authority half stands and is untouched — an Actuator declaration carries `facetNamespaces`, a write
ceiling, so **a plugin must never grant itself authority.**

Both hold at once, because they are different axes:

- **Locality** — the file lives with the plugin. Its author owns it, versions it, ships it.
- **Authority** — the **adopting estate** decides what is admitted and with what ceiling. A plugin's
  declaration is a **proposal with defaults**, not an installation. Helm's chart-defaults /
  operator-overrides shape, and the review is real because the admission is a diff.

ADR-0135 D1's "copied, not installed" survives as the authority rule. Its _location_ guidance is
superseded by D1 above.

### D4 — What core keeps, and the rule that keeps it honest

Core owns: the Named Kinds, the graph, orchestration, authz, audit, the **capability vocabulary**
(closed set, ADR-0104), the port and its protos — and **cross-plugin compositions**: the default
Workflows and Blueprints for things that span tools, like _stand up a VM → issue a certificate →
install and configure it_.

**A core composition may depend only on capabilities, never on a plugin's name.** That single rule is
what stops "default workflows" from becoming the back door through which plugin specifics re-enter the
core. It is also newly possible: ADR-0135 D2/D3 shipped `remediates` + `remediationCapability`, so a
composition can say `provisioning`, `certissuer`, `configmgmt` and let each plugin supply its own
mechanism. Without that seam this decision could not be implemented; with it, the connective tissue
already exists.

### D5 — A plugin owns its Contracts; core pins them

§1.5 requires plugin schemas pinned and hash-verified with drift blocking. That is a statement about
**verification**, not about **residence** — core must know the hash, not host the file.

So Contracts move into the plugin, and core verifies at registration rather than embedding. The
mechanism is not new: `contract.ValidateDocument` already evaluates _"a schema document that is not
embedded — e.g. a DB-pinned rung-2/3 Contract (ADR-0022)"_. This extends that path to rung-1
hand-written schemas.

**This is the riskiest step in the ADR and is sequenced last.** `contracts.FS` is embedded and
`TestPinsAreStable` asserts an exact document count; moving 150+ documents out from under a load-bearing
integrity mechanism deserves its own slice, its own tests, and its own scrutiny.

### D6 — Mock every plane, starting with the one facing plugins

Each plane ships a mock the others can develop against. **`mock-stratt` comes first** — the
plugin-facing host that answers the port — because it is what turns D2 from an aspiration into
something a contributor can run.

Its value is the same as `plugins:standalone`'s: not that it proves the plugin works, but that it
proves the **boundary is real**. A plugin that needs the whole control plane to start has a dependency
nobody wrote down.

### D7 — What `estate/` and `demos/` become

- **`estate/`** stops being the home of plugin declarations and becomes what it should always have
  been: **a worked composition** — capability-bindings, cross-plugin Blueprints, Assignments. An
  example of assembling plugins, not a place plugins live.
- **`demos/`** keeps **cross-plugin scenarios only.** `app-cert` spans openbao + ansible + a managed
  node and stays. `vsphere-only` is a vcenter plugin demo and moves to the plugin. The split is not
  uniform and must be made per demo, because getting it wrong recreates the scatter in a new tree.

## Charter alignment

- **§1.4.** This is the discipline stated as a layout: core owns the spine, the community owns breadth,
  and the filesystem now says so.
- **§1.5.** Sovereign contracts are strengthened — a Contract lives with the tool it describes and is
  pinned by the core that consumes it. Verification is unchanged; residence moves.
- **§7.2.** The cost of a first plugin contribution drops from "understand this repo" to "implement
  this port against a mock." That is a contributor-demographics decision as much as an architectural one.
- **§2.** No new Named Kind. Nothing here renames anything.

## Consequences

- **A plugin becomes reviewable, ownable, and testable as a unit** — and eventually extractable to its
  own repo. This ADR deliberately does **not** decide that; locality first, and repo-splitting is a
  later decision that this ordering keeps cheap.
- **Adding a plugin stops touching core**, which makes `overview.md`'s existing claim true.
- **The migration is wide and mechanical**, and must go one plugin at a time. `ansible` first — it is
  the most recently worked and the only one with a content project (ADR-0134) to move with it.
- **Contract relocation is the risky part** (D5) and is last for that reason.
- **`go.work` stays** for local development ergonomics; `plugins:standalone` remains the truth. Keeping
  both is not a contradiction — one is convenience, the other is the gate.
- **Two trees will be wrong for a while.** During migration a plugin's parts exist in both places, and
  the only defence is finishing each plugin before starting the next.

## Alternatives considered

- **Keep the monorepo layout; add conventions and documentation.** Rejected on the project's own
  evidence: `plugins:standalone` exists because convention did not hold even for `go.sum`.
- **Split every plugin into its own repository now.** Rejected as premature and hard to reverse.
  Locality inside one repo gets the isolation benefit while the boundary is still being learned; the
  split becomes trivial afterwards, and impossible-to-evaluate before.
- **Let a plugin ship declarations that auto-reconcile.** The frictionless install story, and rejected
  for ADR-0135 D1's untouched reason: a declaration carries a write ceiling, so this is a vendor
  granting itself authority.
- **Move Contracts but leave estate artifacts central.** Half the change, none of the benefit: a plugin
  author would still edit `estate/` for every declaration.
- **Fold this into ADR-0135.** Rejected. 0135 decides what a plugin may _ship_; this decides where a
  plugin _lives_ and what independence means. 0135's authority rule is a dependency of this, not the
  same argument.

## Implementation — step 1 shipped

Ordered so each step is provable before the next depends on it.

1. ~~**`mock-stratt`** — the plugin-facing host (D6). First, because it makes every later step
   testable.~~ **Shipped** as `sdk/mockstratt`: both transports (EE-Job subprocess + gRPC), a faithful
   Apply governor, and a tool-blind conformance suite. Three things are worth recording because they
   were not obvious when this ADR was written:
   - **It lives in `sdk/`, not a new module.** Every plugin already requires `sdk` with a `replace`,
     so a plugin gains the harness with no new wiring — which is itself a D2 test the packaging had
     to pass.
   - **Reimplementing the governor risks drift, so drift is now a test.**
     `core/internal/pluginhost.TestMockStrattGoverns­IdenticallyToCore` drives both governors over the
     same encoded frames and compares verdicts AND refusals. It lives in core because core may import
     sdk and never the reverse — the dependency direction is the point. Verified to fail on induced
     drift, not merely to pass.
   - **The fidelity earns its keep immediately.** `plugins/ansible/conformance_test.go` builds the real
     `cmd/stratt-ansible` and drives it through the harness with a stand-in `ansible-runner`, covering
     ADR-0134's read-only mount, the vacuous-run refusal, and the confused-deputy gate — with no
     cluster, no Postgres, no Temporal and no ansible installed.
2. ~~**`plugins/ansible/` takes ownership**: its `estate/` declarations, its conformance suite, its
   demo. One plugin end to end, as the worked example the rest are copied from.~~ **Shipped.** Its 4
   Actuators, 6 Workflows, 2 Triggers and 3 content projects now live in `plugins/ansible/estate/`. Two
   things this step had to settle that the ADR left open:
   - **How an estate admits a plugin.** `<root>/plugins.yaml` names the plugin estates this estate
     admits; `ParseDir` treats their directories as ADDITIONAL search paths for the kinds it already
     reads, so everything merges into one flat set validated in one pass. That keeps a cross-tree
     reference — a Blueprint here routing to a Workflow the plugin ships — an ordinary reference and
     not a special case, and it makes D3's authority rule mechanical rather than aspirational: an
     unadmitted plugin estate sitting right beside the estate contributes nothing.
   - **D2 and D3 are about different acts.** D2 says developing a plugin changes nothing outside
     `plugins/<name>/`; D3 requires admission to be a reviewable diff. Both hold, because
     **developing ≠ deploying**: the plugin builds, tests and conformance-checks with no estate at all,
     and `plugins.yaml` is touched once, on purpose, by whoever deploys it.

   Two details worth recording because they are easy to get wrong:
   - **`contentDir` resolves against the estate that SHIPPED the Actuator**, not the one that admitted
     it — otherwise the plugin's content tree is unreachable the moment it moves. Guarded by
     `TestPluginContentResolvesAgainstItsOwnEstate`, which plants a decoy at the same relative path
     under the admitting estate.
   - **Duplicate names must stay a hard error across estates.** `parseKind`'s seen-set spans every
     root, so two admitted plugins declaring the same Workflow name names both files rather than
     picking a winner by load order (§2.4).

   **`demos/app-cert` did NOT move**: it spans openbao + ansible + a managed node, so by D7 it is a
   cross-plugin scenario and stays. Ansible has no single-plugin demo to relocate.

   Deployment is `task dev:stage-estate`, which now VENDORS each admitted plugin estate into the staged
   tree under `plugins/<name>/estate/` and rewrites `plugins.yaml` to local paths — the `install` half,
   the same materialize-into-operator-Git move `stratt pack install` makes (ADR-0033). Vendored as a
   subtree rather than flattened for two reasons: two plugins may each ship a `content/` root and
   flattening would collide them silently, and the deployed tree still shows which declarations came
   from which plugin (§1.8).

3. ~~**The D2 gate** — CI proves a plugin-only diff touches nothing outside `plugins/<name>/`, with the
   two documented exceptions.~~ **Shipped**, as **two** gates, because D2 has two halves and only one of
   them is diff-shaped:
   - **`task plugins:boundary`** — structural, and the primary. D2 asks to be "mechanically checkable
     the way `plugins:standalone` is", and note what that gate actually does: it proves a property of
     the TREE, not of a diff. So does this. The strongest mechanical statement of "developing a plugin
     changes nothing outside it" is that there is nothing outside it to change. It checks the **import
     direction** (no plugin module may depend on `core/`) and **estate ownership**.
   - **`task plugins:boundary:diff`** — the literal diff rule, and it earns its place by catching what
     the structural gate cannot: a change that edits a plugin AND teaches core about it, the
     `if ansible {}` §1.4 forbids. That coupling leaves no trace in the plugin's module graph, so only
     the shape of the change reveals it.

   **The ownership check is a RATCHET, not a cliff.** It fails only for plugins that have already taken
   ownership. A gate that went red for all 11 unmigrated plugins on day one would be switched off within
   a week, and a switched-off gate is worse than none. What it buys: migration is one-way, and the
   remaining debt is printed on every run rather than living in someone's head — so step 4's progress is
   legible.

   **The diff gate's escape hatch is a statement, not a switch.** A change that genuinely alters the
   boundary — building the admission mechanism, moving a Contract — is legitimate and must be possible;
   what must not be possible is doing it silently. A `Boundary-Change:` commit trailer satisfies the
   gate and puts the reason in front of the reviewer. That is D3's logic applied to D2: the exception is
   fine, the exception being invisible is not. (Commit `81f1789`, step 2, carries the first one.)

   `plugins:standalone` was upgraded from `go build` to `go vet`: build ignores `_test.go`, so a
   test-only dependency missing from `go.sum` was invisible to it — and "a plugin can be **tested**
   alone" is the property D6 exists for, not merely "it compiles alone".

   One violation was **removed rather than allowlisted**: six near-identical `image:<name>-plugin` tasks
   meant adding a plugin required a core edit for pure boilerplate. They are now one
   `task image:plugin PLUGIN=<name>`. Deleting the cause beats permanently excusing the symptom — and a
   gate whose allowlist keeps growing is a gate on its way to being ignored.

   Known temporary exception, listed in the gate so it stays visible: `contracts/**`, until D5/step 6
   relocates plugin Contracts.

4. ~~**Remaining plugins**, one at a time.~~ **Shipped.** All eleven migrated; `estate/actuators/`
   and `estate/connectors/` are empty and `plugins.yaml` admits twelve. Step 4 began by re-examining
   step 2 rather than piling onto it, and that paid twice:
   - **`linux-onboard` had been moved wrongly.** It spans `awsec2` → `ansible`, so it is a
     composition (D4/D7). The Traps section says this for demos — "do not move a demo into a plugin
     because it MENTIONS that plugin" — and the same test applies to every declaration. Returned to
     `estate/`, and `plugins:boundary` grew a third check (every `actuator:`/`action:` inside a plugin
     estate must resolve to that plugin) written and confirmed to fail on the live defect first.
   - **ADR-0135 D3 turned out to be unusable by the tool it was written for.** Capability resolution
     counts only *verified* providers, verification needs a dial address, and an EE-Job Actuator has
     none by construction (§3, GPLv3). See the LIMITATION block in ADR-0135 D3.

   **Views did not move** despite D1 listing them: in this estate they are the groups Assignments bind
   to, which makes them composition. A plugin shipping a View over the kinds its own Syncer projects
   is a different case, left for when one exists.

   **Demos cannot verify this**, which is worth knowing: each stages only its own estate and none
   carries a `plugins.yaml`. `task dev:connector-e2e` stages the full estate and is the in-cluster
   proof — it booted strattd in kind against all twelve vendored plugin estates and reconciled them.
5. ~~**`estate/` and `demos/` reduced** to compositions and cross-plugin scenarios (D7).~~ **Shipped.**
   `estate/` needed nothing further — step 4 emptied `actuators/` and `connectors/`, leaving exactly the
   worked composition D7 describes (Views, Intents, Blueprints, Assignments, capability-bindings, and
   the three cross-plugin Workflows).

   `demos/` split on the same test used everywhere else — **does it span more than one plugin?**
   `k8s-deploy` → `plugins/helm/demo/`, `vsphere-only` → `plugins/vcenter/demo/`, `ec2-only` →
   `plugins/awsec2/demo/`. Only `app-cert` stays, spanning ansible + openbao + declared. `plugins:boundary`
   grew a fourth check so a single-plugin demo cannot land in `demos/` again — without it the split
   re-scatters one file at a time, since `demos/` is where demos "go".

   The move's real risk was **losing test coverage silently**: four guards in
   `core/internal/desiredstate` globbed `demos/*/estate`, and a demo that stops being checked because it
   changed directory is worse than one never checked — the census assertion still passes and the coverage
   looks intact. They now share a `demoEstates` helper that finds both homes.

   Two smaller things: `plugins/helm/demo/run.sh` computed a repo root by directory depth
   (`${HERE}/../..`), which the move silently invalidated — it was dead code and was deleted rather than
   re-derived. And the demo **task blocks stay in the root Taskfile**; see the gap below.

   Verified by running the relocated `k8s-deploy` demo end to end on kind from its new home.

   **REMAINING D2 GAP, booked not hidden:** each demo still has bespoke task blocks in the root
   `Taskfile.yml` (13 of them), so adding a plugin with a demo still edits core. `demo.yaml` already
   exists as a declared manifest and is the obvious place for a demo to declare its floor — which plugin
   images, which values files, which extra services — so a generic `task demo:run DEMO=<path>` could
   retire them. Not attempted here: the four floors differ substantially (vcsim, floci, a node container,
   three different values stacks), and guessing that shape while migrating is how the scatter gets
   recreated in a new tree.
6. **Contracts relocate, core pins at registration** (D5) — last, and with its own tests.

### Traps

- **A "default workflow" in core that names a plugin is the whole decision undone.** D4's rule is the
  one to enforce in review; capability-routing (ADR-0135) is what makes obeying it possible.
- **Do not move a demo into a plugin because it _mentions_ that plugin.** The test is whether it spans
  more than one (D7), and `app-cert` is the case that will be got wrong.
- **`TestPinsAreStable` is a deliberate tripwire.** When Contracts move, its count changes for a
  structural reason — bump it because the mechanism changed, never to make a test pass.
- **The acid test is the point; the directory layout is not.** If a future change satisfies the layout
  and still requires a core edit to add a plugin, the layout is wrong and D2 is right.
