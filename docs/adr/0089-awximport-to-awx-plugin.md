# ADR 0089 — the AWX→CaC transform is plugin breadth: move `awximport` into the awx plugin

- **Status:** Accepted
- **Date:** 2026-07-20
- **Deciders:** steward (dstout)
- **Reviews:** charter-guardian — PASS-WITH-CHANGES: §1.4 breadth/spine cut sound, §2.5 locus preserved,
  awxfacade-stays correct, `import`-retirement clean. Must-fix folded: the §1.5 golden contract test is now
  emitter-generated + coverage-complete + staleness-guarded (§6). Reword folded: "zero AWX code" → "zero AWX
  outbound-client/transform code" (the inbound `awxfacade` stays by design). Verify folded into the build:
  confirm no core pkg besides the adopt registration imports `sdk/secretbroker` before dropping the dep.
  vocabulary-linter — PASS (`materialize`/`controller`/`awxsim` plugin packages clean; no banned term leaks
  into a core identifier; AWX nouns stay confined to the plugin wire boundary).
- **Charter sections:** **§1.4** (boring spine, pluggable everything — core owns the spine, *community
  owns breadth via plugin surfaces*; the AWX→Named-Kind mapping is breadth, not spine), **§1.5** (no
  external protocol load-bearing for the core; the transform's output is validated as a pinned Contract),
  **§2.5** (the credential locus is preserved — the AWX token still resolves only in a pod, now the awx
  plugin's, via its SecretBroker), **§1.2** (the plugin still writes no graph; adopt emits reviewable CaC),
  **§1.6** (adopt stays an API-first async Run under one Principal/authz/audit/cost).
- **Supersedes-in-part:** **ADR-0088** — its §1.4 premise ("the transform is core code, so the Action
  server is a *core-owned* image in the core module") is retired. Everything else ADR-0088 decided — the
  §2.5 pod-locus, use-without-read, the async Run via `RunAction`, the Resolve/Materialize split, the
  input/output Contracts, the API shape — **stands**; only the transform's *home* moves (core module → the
  awx plugin), which also lets us delete the core-owned `stratt-adopt` image and core's `secretbroker`
  dependency. Also finally **retires** the legacy one-shot `import` CLI verb that ADR-0086 superseded on
  paper but left wired.
- **Builds on:** ADR-0046 (dark-matter — every tool is a plugin over the sovereign port; the spine is
  content-blind), ADR-0086 (per-object adopt; the lean-catalog vs rich-deep-read model (b)), the awsec2
  plugin (the exact precedent: a SYNCER-class plugin that *also* advertises `INVOKE` + an Action).

## Context

`awximport` (the AWX→Named-Kind transform, its own AWX Controller REST client, and `awxsim`) lives in
`core/internal/`. It **predates the dark-matter arc** — it began life as the ADR-0025 *core importer*, and
when ADR-0046 re-centered every tool onto the sovereign port, it never migrated. ADR-0088 then moved the
credential-bearing *execution* into a pod but, to keep the change bounded, kept the transform in the core
module and shipped it as a *core-owned* image — accepting (F-1 in that review) that `strattd` still **links
a whole AWX REST client + transform** into the content-blind control-plane binary (`go list -deps
./cmd/strattd` shows `awximport`, `awximport/awx`, `awxfacade`), and that a **second** AWX client now exists
alongside the plugin's.

The steward's question surfaced the real smell: **`awxsim` is the only sim not living in its plugin**
(`chef/chefsim`, `msgraph/graphsim`, `puppet/puppetsim`, `salt/saltsim` all do it right), and the spine
carries tool-specific AWX code it shouldn't.

The resolving insight: **the AWX→Named-Kind *mapping* is tool-specific breadth, not spine.** The generic
adopt *capability* (take authority over an observed object → emit reviewable CaC → cutover) is spine and
stays in core. The AWX-specific knowledge (a `job_template` → a Workflow; a `survey_spec` → a Contract;
workflow nodes → Steps) is exactly the breadth §1.4 says the community owns via plugins. ADR-0088's
objection — "the content-blind port can't carry the rich AWX detail" — **dissolves once the transform runs
plugin-side**: the port carries the transform's *output* (`{files, report}` — tool-agnostic Named-Kind
bytes), and the rich AWX detail never crosses the port; it stays inside the plugin. Investigation confirms
the transform is cleanly extractable: its non-test files have **no** core-internal dependency (only the
shared `types` module, in `credentials.go`); every `desiredstate`/`graph`/`authz`/`awxfacade` coupling
lives **only in round-trip tests**.

## Decision

**Move `awximport` (transform + rich Controller client + `awxsim`) into the `awx` plugin, and make
`adopt/materialize` an Action the awx plugin provides. Core keeps only the tool-blind adopt orchestration.
`awxfacade` stays in core.**

### 1. The transform + rich deep-read client + sim move into `plugins/awx`
- The transform (`Bundle` + the survey/views/workflows/credentials/report/emit mappers) and the rich AWX
  Controller client (enumerate + single-object deep-read + its `Snapshot`/`JobTemplate` types) move into the
  awx plugin. `awxsim` moves with them — now consistent with every other tool's sim.
- **Two clients, both in the plugin, is principled — not duplication.** The Syncer's lean client reads the
  *catalog* (projection); the transform's rich client does the *deep-read* (model (b), ADR-0086). Different
  read fidelities for different jobs; both correctly owned by the plugin (the AWX owner). Fully converging
  them is a bounded in-plugin follow-up, not required here.
- The awx plugin becomes a SYNCER-class plugin that **also advertises `INVOKE`** with an `adopt/materialize`
  ActionDecl — byte-for-byte the awsec2 pattern (`Class: SYNCER, Verbs:[OBSERVE, INVOKE], Actions:[…]`). Its
  `Invoke` resolves the AWX CredentialRef via the SDK **SecretBroker** in its own pod (§2.5, unchanged),
  does the deep-read + transform, and returns the bundle on `InvokeResult.Outputs`. The plugin gains
  `types` + `sdk/secretbroker` + `yaml` deps — all plugin-importable; **nothing from `core/`** (module
  isolation preserved, ADR-0046).

### 2. Core keeps the tool-blind adopt orchestration
`core/internal/adopt` slims to the spine: `Resolve` (the graph read → native id, source, live executions),
`Catalog`, `Request`, `Resolved` (the coordinates handed to the Action), and the client-error sentinels.
`Materialize`, the `Reader` interface, `stampLineage`, `cutoverGuard`, and the `awximport` dependency all
**leave** (they are the transform — plugin-side now). The API handler (`AdoptObject` → `Resolve` →
`LaunchRun(Action:"adopt/materialize")`) and `GetAdoption` are **unchanged** — they never named a tool.
The cutover reconciler (ADR-0087) is untouched (already tool-blind via the manifest CutoverDescriptor).

### 3. Delete the core-owned Action plumbing
`core/internal/adoptplugin`, `core/cmd/stratt-adopt`, `deploy/docker/stratt-adopt.Dockerfile`,
`deploy/dev/adopt-plugin.yaml`, the `image:adopt-plugin` task, and **core's `secretbroker` require/replace**
all go away. `strattd` registers `adopt/materialize` using the **awx plugin's host** (inside the existing
`STRATT_AWX_PLUGIN_ADDR` block, `registerPluginAction` after the Syncer registration — the awsec2 shape); the
separate `STRATT_ADOPT_PLUGIN_ADDR` block is removed. **Result: `strattd` links zero AWX *outbound-client /
transform (breadth)* code** — `awximport` and `awximport/awx` leave its dependency graph (the §1.4 win,
verified by `go list -deps ./cmd/strattd`). The inbound `awxfacade` compat transport stays linked **by
design** (§4 — it is an API surface, not breadth); the slimmed tool-blind `adopt` package also remains.

### 4. `awxfacade` stays in core
The AWX-`/api/v2` compat façade is an **inbound** surface (external AWX tooling points *at* Stratt during a
cutover, §5.6) — it does not fit the **outbound** plugin port (Syncers/Actuators/Actions integrate Stratt →
tool). It is a translation layer mounted on the API server, like the OpenAPI handlers, and its own doc
frames it as "a compat transport, never load-bearing" (§1.5). It stays. A `facades/` grouping earns its keep
only at N≥2 (a second compat surface); with N=1 it is premature.

### 5. Retire the legacy `import` verb
ADR-0086 superseded the one-shot `stratt import awx` (Decision #1) but left `core/cmd/stratt/import.go`
wired. Remove it and its dispatch; the rich client's `Enumerate` moves to the plugin and is retained there
for a possible future bulk-adopt (`log`-ged if ever bounded). `adopt` (per-object, strangler-fig) is the
sole path.

### 6. Round-trip validation across the new module boundary (§1.5)
The transform's shape tests move to the plugin (asserted against `awxsim`). The round-trip guarantee — *the
emitted CaC actually parses back through the core `desiredstate` reader* — cannot live in the plugin (it may
not import core). It becomes a **core-side golden contract test** that must be adequate, not a weaker static
fixture (charter-guardian §1.5 must-fix). It has three properties:
1. **Emitter-generated, never hand-authored.** A plugin-side test runs the *real* transform against
   `awxsim` and writes the golden bundle fixture; the fixture is the plugin's actual emission, not a
   hand-copied approximation.
2. **Coverage-complete.** The fixture exercises every emit shape — Views, Workflows, gate-vs-actuation
   Steps, CredentialRefs, survey→Contract, and the residual report — so the contract test sees the full
   surface, not a happy-path subset.
3. **Staleness-guarded.** A CI step regenerates the fixture and fails on any `git diff` (the
   `generate:check` pattern), so a plugin emit change that core's `desiredstate` can no longer consume
   fails **loudly** at the boundary — never silently absorbed (§1.5 "schema drift is blocking").
The core-side test then parses the committed fixture via `desiredstate`, proving the plugin↔core CaC
contract holds across the module boundary. With these three properties this is a *stronger* guarantee than
the old in-package round-trip — an explicit, reviewable §1.5 contracts-as-data artifact.

## Charter alignment
- **§1.4:** the spine names no tool; `adopt/materialize` dispatches through the tool-blind `RunAction` path
  exactly like every plugin Action; the AWX mapping is plugin breadth; `strattd` links no AWX code. `awxsim`
  sits with its plugin like all the others.
- **§2.5:** the credential locus is *preserved* — the AWX token resolves only in the awx plugin's pod via
  its SecretBroker (the ADR-0088 win carries over intact; core drops its `secretbroker` dep).
- **§1.2:** the plugin writes no graph; adopt emits reviewable CaC captured as Run outputs (Run provenance).
- **§1.5:** the transform output is a pinned Contract; the golden contract test guards the CaC boundary.
- **§1.6:** adopt stays API-first, agent-native, an async descendable Run.

## Alternatives considered
- **Keep the transform core-owned (status quo, ADR-0088).** Rejected — leaves AWX-specific breadth compiled
  into the content-blind spine; keeps the second AWX client in core and `awxsim` out of place; the whole
  point of the dark-matter arc is that this does not happen.
- **Extract to a shared module both core and the plugin import.** Rejected — the awx plugin *is* the owner
  of AWX breadth; a neutral shared module re-centralizes what should be plugin-local and dilutes ownership.
- **Fully converge the two AWX clients now.** Deferred — the lean-catalog and rich-deep-read clients are the
  principled model-(b) split (ADR-0086); merging them risks the Syncer's `normalize` path for no §1.4 gain
  (both already live in the plugin after this ADR). A bounded in-plugin follow-up.
- **Move `awxfacade` too (into a plugin or a `facades/` group).** Rejected — it is an inbound compat API,
  not an outbound plugin-port integration; grouping is premature at N=1.

## Slice roadmap
1. **This ADR + the move:** transform + rich client + `awxsim` into `plugins/awx`; `adopt/materialize` as an
   awx-plugin Action (manifest `INVOKE` + `Invoke` + SecretBroker); slim `core/internal/adopt` to the
   tool-blind spine; delete the core-owned adopt image/plumbing + core's `secretbroker` dep; retire
   `import`; the golden contract test; `strattd` + Dockerfile rewiring.
2. **Follow-ups (own slices):** converge the plugin's two AWX clients; a bounded `bulk-adopt` over the
   catalog if demand appears (never a silent full-estate one-shot).
