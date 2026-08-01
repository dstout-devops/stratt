# ADR 0116 — The demo library: reproducible, narrated, turnkey teaching scenarios

- **Status:** **Proposed** (2026-07-24, steward) — vocabulary-linter **CLEAN after fix** (lowercased "Gate" →
  "gate Step" — a gate is a Step kind, not a §2 Named Kind); charter-guardian **PASS after fixes** (corrected the
  ADR-0084 citation — it is the reachability-Facet seam, not "the managed-web demo"; the embedded-demo precedent
  is the `estate/{assignments,views,hosts}/managed-web.yaml` artifacts narrated in their own file comments, NOT
  `estate/README.md`; D3 now makes `fidelity:` a DECLARED field the runner surfaces so the honesty guarantee
  doesn't rot; the first demo's spine-not-intent-layer teaching depth is made a deliberate, documented ladder
  choice).
- **Date:** 2026-07-24
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.8 (the abstraction must never hide diagnosis — the one-click descent IS the
  "how Stratt works" story a demo teaches; hiding _mechanism_ is fine, hiding _failure_ is not, so demos
  are honest about real-vs-simulated fidelity) · §1.1 (a demo's estate is normal typed CaC — no new
  configuration language) · §1.6 (one Principal / authz / audit model across UI/CLI/CI/agents — a demo
  drives the same surfaces a real operator does) · §7.4 (OSPO/IP clearance is cleared, so public-facing
  demo + narrative material may exist). **Reuses [ADR-0102](0102-tiered-genesis-bootstrap.md)** (the
  `dev:genesis` on-ramp + its gated self-deploy loop — a demo sits _downstream_ of genesis), **ADR-0084**
  (the typed managed-node reachability Facet that _makes the `managed-web` convergence real_ — the
  embedded-demo precedent itself is the `estate/{assignments,views,hosts}/managed-web.yaml` artifacts,
  label-scoped and narrated in their own file comments),
  **[ADR-0055](0055-estate-composition.md)/[ADR-0056](0056-estate-as-code.md)/[ADR-0057](0057-environment-scoped-reconciliation.md)**
  (estate composition / CaC / environment scoping — the demo's estate is these, not a fork), and
  **[ADR-0091](0091-ui-is-a-first-party-bundled-pure-api-client.md)/[ADR-0003](0003-ux-design-principles.md)**
  (the first-party UI is the canonical descent surface).

## Context

The vSphere connector (ADR-0113/0114/0115) rounds out a substantial platform: typed graph, capability
framework, provisioning + lifecycle + read breadth across substrates. The next initiative is **demos** —
reframed as the start of a **demo library** that becomes getting-started / onboarding / long-term training
material. The eventual goal is a full multi-substrate "enterprise estate" demo, but single-substrate demos
(k8s-only / ec2-only / vSphere-only) and a simple deployment demo are **first-class**, and we **start small
to establish the pattern**.

A prior-art scan established the shape: a demo pattern already ships (`managed-web` — a demo that lives _in_
`estate/`, label-scoped, narrated in its own `estate/{assignments,views,hosts}/managed-web.yaml` file
comments; ADR-0084 supplies the reachability Facet that makes its convergence real); a large amount of runnable scaffolding
exists (`dev:genesis`, `dev:stack:up`, the per-substrate seeders, and crucially the `stratt-dev-assert` +
`genesis-selfdeploy.sh` _launch → poll gate → approve → poll run → assert_ loop); and there is **no `demos/`
tree yet** (clean greenfield). It also confirmed the literal "grand capstone" has un-built prerequisites
(per-instance/region fan-out ADR-0058; no K8s `Compute` provider; awsec2 region/AZ projection; multi-
substrate simultaneous reconcile) — which this ADR defers to _later demos that each teach and close one gap_,
not the first increment.

## Decision

### D1 — Demos are a top-level `demos/` library of self-contained scenarios

Each demo is a self-contained directory `demos/<name>/` holding: `README.md` (the narrated walkthrough), a
`demo.yaml` manifest (title/summary + the declared `fidelity`, D3), an `estate/` fragment (the scenario's
CaC), any demo assets (e.g. a chart), and its turnkey runner. `demos/README.md` is the library index
(registered in `docs/README.md`).

**How a demo's estate is delivered — CaC through the declarations mount, NOT `stratt apply`** (corrected
against the first live runs; this ADR was still Proposed): a demo stages its `estate/` into the
inline-declarations path (a `demo:<name>:stage` task, mirroring `dev:stage-genesis`) so the floor boots with
the demo's estate as its desired state and the reconcile controller enforces it. Two structural facts forced
this and are worth recording, because they constrain every future demo:

1. **Actuators and CredentialRefs are CaC-only** (§2.2/§2.3, ADR-0103) — Git review authorizes plugin
   registration, so the imperative apply door cannot register them (`DeclareCredentialRefAs(…, api)` is
   refused).
2. **`stratt apply` is the same authority as the reconcile controller**, not a supplement — it declares
   `DeclaredBy=cac` and prunes whatever is absent, so applying a fragment "on top of" a running floor is
   reverted within a reconcile tick.

Each demo estate is therefore **whole and self-contained** (its own authz grant, CredentialRef, Workflow,
Views), and still uses only the same typed primitives as the reference estate (ADR-0055/0056/0057) — never a
new configuration language (§1.1).

**Reconciling the `managed-web` in-`estate/` precedent:** a _single embedded_ demo (`managed-web`) was fine as
label-scoped content inside the one reference estate. But a _growing library_ of standalone teaching
scenarios — each with its own narrative, runner, and reset — wants its own tree, so a reader can open one
demo and see the whole thing without untangling it from the reference estate. This is not a fork of the
estate model: **each demo's `estate/` uses the same typed primitives (Views/Intents/Workflows/CredentialRefs/
environments, ADR-0055/0056/0057) — never a new configuration language (§1.1).** The `demos/` tree is
_where teaching scenarios live_; the reference `estate/` remains _what a real operator runs_. (`managed-web`
stays where it is; it is not migrated.)

### D2 — Every demo is BOTH turnkey and narrated

- **Turnkey:** a `demo:<name>:run` task — one command that stands up the floor with the demo estate staged
  as its desired state (D1), launches the gated Workflow, **auto-approves** the approval gate Step, and
  **asserts** the outcome, reusing the shipped `genesis-selfdeploy.sh` launch/approve/assert loop. A paired
  `demo:<name>:down` tears the demo's footprint back down, so every demo has a build-up ⇄ tear-down
  lifecycle and re-runs from a known state. This makes every demo reproducible, CI-able, and
  **non-rotting** — a demo that stops working fails its own runner. (Auto-approval automates the human but
  goes through the real `POST /gates/{id}/decision` → authz → Temporal signal path; the gate is a durable
  Temporal signal wait, never a scripted sleep.)
- **Narrated:** a `README.md` that walks a human through the same steps by hand, with plain-language
  explanations of each Named Kind and the descent, and "open the UI to watch it happen."

The turnkey run keeps the demo _honest_; the narrative _teaches_. This is the base the future
interactive/UI-driven demo evolution builds on — not a bespoke narration engine.

### D3 — Honest real-vs-simulated fidelity (ADR-0084 pattern, §1.8)

Each demo states its fidelity plainly: **kind/helm and floci/EC2 are real** (real workloads; real
Ansible-over-SSH convergence, ADR-0084/0093); **vcsim/vSphere is build/lifecycle-API real, guest-OS
simulated** (ADR-0007/0113 D5 — no bootable guest, so no SSH converge). A demo **never presents simulated
fidelity as real** — hiding _mechanism_ is the product, hiding _failure/limits_ kills trust (§1.8).

**The fidelity claim is a DECLARED field, not just prose (so it can't rot).** Each demo declares its
fidelity (e.g. a `fidelity: real` / `fidelity: build-api-real-guest-simulated` line in the demo's manifest),
and **the turnkey runner (D2) reads and prints it** as part of its output. This makes the single most
trust-load-bearing claim as non-rotting as the outcome assertion — resolving the tension charter-guardian
flagged (D2 rejects rot-prone docs, so the honesty guarantee must not itself live only in the doc layer).

### D4 — The shipped descent is the teaching surface (§1.8)

Demos narrate the shipped **Intent → Workflow → Run → task-event** descent via the first-party UI
(ADR-0091/0003 — always in the Apache-2.0 product, never gated), with the CLI, `/api/v1`, and MCP as
equally-authorized (§1.6) secondary paths. A demo does not invent a narration mechanism; it drives the same
surfaces an operator, CI job, or agent uses.

## Charter alignment

Upholds §1.8 (the descent is the teaching surface; honest real-vs-sim — D3/D4), §1.1 (a demo's estate is
typed CaC, no new config language — D1), §1.6 (demos drive the one Principal/authz/audit model), §7.4
(public-facing demo material is cleared). It touches **no core Go, no data model, no Contracts, no proto** —
a demo is estate CaC + assets + a runner + docs. The consequential decision is structural (a new top-level
`demos/` tree + the demo model + the real-vs-sim honesty rule) — hence the ADR and the charter-guardian bar.

## Consequences

- **The teaching ladder is deliberate (charter-guardian flag 4).** The first demo ("k8s: deploy an app" —
  `helm/deploy` a workload behind an approval gate) teaches the **orchestration spine + the descent's lower
  rungs** (Workflow → Run → task-event) and the "declare in CaC → gated → real result" arc — the truest
  _simple deployment_ on-ramp. It deliberately does NOT yet reach the **Intent → Blueprint → Baseline** layer
  that is the platform's thesis (§6 power gradient) — that fuller descent (which the shipped `managed-web`
  convergence already exercises) is the **explicit next rung**, not an oversight. The library climbs the
  ladder: spine first, then the intent/desired-state layer, then multi-substrate.
- **Positive.** A reproducible, non-rotting, teaching-oriented demo library with a clear pattern to extend;
  the first rung of getting-started/onboarding/training. Reuses proven scaffolding (genesis, helm/deploy,
  `stratt-dev-assert`) rather than inventing a demo engine. Honest about fidelity from day one. Sets the
  numbering/structure for the substrate demos (ec2-only, vsphere-only) and the eventual enterprise capstone.
- **Negative / trade-offs.** A second place estate-shaped content lives (`demos/` alongside `estate/`) — the
  D1 boundary (teaching scenarios vs the operator's reference estate) must stay clear or the two blur. A
  demo's live-green acceptance needs a real dev cluster (kind), so CI/live verification is heavier than a
  unit test (mitigated: the turnkey runner IS that verification; the reproducible+asserting runner is the
  deliverable even where a sandbox can't stand up kind).
- **Demos are a bug-finding instrument, not just documentation** (observed across the first three). Running
  a scenario end to end on a real cluster exercised shipped seams no unit test covered, and each demo
  surfaced real defects that were fixed as part of landing it: the desired-state wire API could not carry a
  targetless `action` Step or a `gateOnly` CredentialRef (a §1.6 asymmetry vs the Git door); the genesis
  floor declared **no** helm Actuator, so the shipped self-deploy dogfood could never register `helm/deploy`;
  a failed plugin Action's real cause was masked by a downstream "got null" and no Run surfaced any error
  (§1.8); `plugins/{vcenter,awsec2}/go.sum` were incomplete for their standalone (`GOWORK=off`) image
  builds; and floci's compose healthcheck probed with `wget`, which its image lacks (false-unhealthy, also
  breaking `dev:stack:up`). This is a positive consequence worth planning for: **budget demo work as
  integration testing with a teaching artifact as the output.**
- **Follow-ups.** Shipped: k8s-deploy (`real`), vsphere-only (`build-real`), ec2-only (`real`). Next rung
  before the capstone: an **app-install demo** — install an application that requires a **certificate**
  (e.g. a TLS web server), so the demo teaches certificate issuance/renewal alongside install. Then the
  **enterprise-estate capstone**, which additionally requires the deferred prerequisites, each its own
  demo-that-closes-a-gap: per-instance/region build fan-out (ADR-0058), a K8s `Compute` provider (or the
  workloads reframe), awsec2 region/AZ projection (ADR-0115 #1), and multi-substrate simultaneous reconcile
  via environment scoping (ADR-0113 D2). ~~Also deferred: the **real SSH converge** act (provision →
  configure over SSH into a floci instance, the ADR-0084 pattern) — floci gives real SSH-able instances,
  but the EE Job (in kind) reaching them across the host/kind network boundary is unsolved.~~
  **WITHDRAWN (2026-07-27): the premise was false.** floci instances are not SSH-able at all — no AMI
  ships sshd, user-data is never executed (HAR-1 in [enterprise-readiness.md](../enterprise-readiness.md),
  guarded by `plugins/awsec2/floci_fidelity_live_test.go`). The host/kind network boundary was therefore
  never the blocker; there was nothing listening to reach. floci's **network** model is fully real and
  remains the provisioning-leg substrate; a provision→configure demo needs a Compute provider whose
  machines boot.

## Alternatives considered

- **Keep demos inside `estate/` (extend the `managed-web` in-estate pattern).** Rejected for the _library_
  (D1): a single embedded demo is fine, but a growing series of standalone teaching scenarios is clearer as
  self-contained `demos/<name>/` dirs; scattering many demo-labeled fragments through the one reference
  estate would blur "what an operator runs" with "what teaches." (The existing `managed-web` demo stays put.)
- **A narrated getting-started doc with no turnkey runner.** Rejected (D2): docs rot; without an asserting
  runner a demo silently breaks. Every demo carries both.
- **A bespoke demo-runner engine / DSL.** Rejected (D2, §1.1): reuse the shipped `stratt-dev-assert` +
  genesis loop; a demo is CaC + a thin task, not a new framework.
- **Start with the full multi-substrate capstone.** Rejected (Context): it has real un-built prerequisites
  and would be a fragile first impression; start small, make each rung teach and close one gap.
