# Stratt demos

A growing library of **self-contained, narrated, turnkey** scenarios that teach Stratt by _running_
it. Each demo is a real slice of the platform you can reproduce in one command — the basis for
getting-started material, onboarding, and (long term) interactive training. Framework and rationale:
**[ADR-0116](../docs/adr/0116-demo-library.md)**.

## How a demo is built

Every `demos/<name>/` is:

- **`demo.yaml`** — the manifest: title, summary, and a declared **`fidelity`** (`real` /
  `build-real` / `simulated`) that the runner prints up front, so the honesty claim can't rot away
  from the code (ADR-0116 D3; charter §1.8 — hide mechanism, never fidelity).
- **`estate/`** — the scenario's Config-as-Code (Views / Workflows / Actuators / …). Normal estate
  CaC — no second config language (charter §1.1). It is **staged into the declarations mount** and
  enforced by the reconcile controller, not pushed through `stratt apply`: Actuators, Connectors and
  CredentialRefs are **CaC-only** (§2.2/§2.3 — Git review is what authorizes registering a plugin, so
  the imperative door cannot), and every shipped demo needs at least one of those. A demo estate
  _replaces_ that mount rather than layering onto the reference estate, which is why each one must be
  self-contained (ADR-0116 D1).
- **`run.sh`** + a **`demo:<name>:run`** Taskfile task — the turnkey path: bring-up → apply → launch
  the gated Workflow → approve → **assert** the outcome. Reuses the proven `genesis-selfdeploy`
  launch/approve/assert loop.

  **These demos DO silently rot, and saying otherwise here was itself a stale claim** (corrected
  2026-08-01; it read "Reproducible and CI-able, so it can't silently rot"). `task ci` runs no demo —
  every one of them is proven by a task a human remembers to invoke. The vsphere demo broke for weeks
  when ADR-0103 moved vcenter's Actions and nobody noticed, and that row is still open as **E2E-1** in
  [enterprise-readiness](../docs/enterprise-readiness.md). What closes it is a scheduled `e2e:live`
  job whose failure is a build failure. Until then, treat a demo's green as evidence of the day it
  last ran — which is why each records the date and the command. A matching
  **`demo:<name>:down`** tears the demo's footprint back down (full nuke: `task dev:kind:down`), so
  every demo has a clean build-up ⇄ tear-down lifecycle and re-runs from a known state.
- **`README.md`** — the narrated walkthrough: the turnkey one-liner _and_ the by-hand path, with
  "open the UI to watch the descent" (charter §1.8).

Demos are teaching scenarios in their own tree; the single embedded `managed-web` convergence demo in
`estate/` (ADR-0084) remains the reference estate's in-place example. Demos _use_ the reference-estate
patterns — they don't fork them.

## Where a demo lives

A demo that exercises **one** plugin lives **with that plugin**, in `plugins/<name>/demo/`
([ADR-0137](../docs/adr/0137-a-plugin-is-a-service-not-a-subdirectory.md) D7). This directory keeps the
scenarios that span **several** — the only ones no single plugin can own.

The test is "does it span more than one plugin?", never "does it mention one." `task plugins:boundary`
enforces it, so a new single-plugin demo cannot quietly land here and re-scatter the tree one file at a
time. Every demo runs the same way wherever it lives: `task demo:<name>:run`.

## The library

| Demo                                                                              | Home                                    | Substrate                | Fidelity              | Teaches                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| --------------------------------------------------------------------------------- | --------------------------------------- | ------------------------ | --------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **[k8s: deploy an app](../plugins/helm/demo/README.md)**                          | `plugins/helm/`                         | Kubernetes (kind)        | `real`                | The **write** half: CaC → gated Workflow → `helm/deploy` over the plugin port → the Intent→Run descent, ending in a real Deployment. **Start here.**                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| **[vSphere: provision a VM + the live graph](../plugins/vcenter/demo/README.md)** | `plugins/vcenter/`                      | vSphere (vspheresim)     | `build-real`          | The **read** half joined to write: a Syncer projects the whole topology into a live graph (Views, Facets, Relations), and the _same_ dual-verb plugin provisions a VM into it — the write reflected back in the read-model, and the built VM **boots a real guest** and reports a reachability coordinate.                                                                                                                                                                                                                                                                                    |
| **[EC2: provision a real instance](../plugins/awsec2/demo/README.md)**            | `plugins/awsec2/`                       | EC2 (floci)              | `build-real`          | Real provisioning: a gated Workflow runs a genuine `RunInstances` against floci (real instance _containers_, not a mock), and the Syncer observes the new instance appear — the graph coming alive _with_ what you build. **Not SSH-able** — see the fidelity note below.                                                                                                                                                                                                                                                                                                                     |
| **[app install with a certificate](app-cert/README.md)**                          | **here** (ansible + openbao + declared) | SSH (Linux host)         | `real`                | **Configuration management** taken seriously: a real SSH converge with declared privilege escalation, a certificate minted by a collection **pinned into the execution environment at build time**, write-back bounded by declaration — and a Run that reached nothing failing instead of reporting green.                                                                                                                                                                                                                                                                                    |
| **[scale a fleet: change a 1 to a 3](scale-fleet/README.md)**                     | **here** (kubecompute + ansible)        | Kubernetes (kind)        | `real`                | **Cardinality, and the asymmetry it found.** Edit ONE number in one Intent and exactly the shortfall is offered — two builds, not three, because the reconcile correlates what is already built. Approve, and the SAME Assignment, unedited, configures the new hosts over `kubectl exec`, never touching port 22 (ADR-0156): the connection method is observed by the provider that built the host, so the same Assignment would reach a vSphere VM without moving. Edit it back and **nothing is offered** — this demo FOUND that `kubecompute` advertises `provisions` and no `decommissions`, so count-down is not symmetric on this substrate. Reported and booked, not papered over. |
| **[region-to-cert — the capstone](region-to-cert/README.md)**                     | **here** (six plugins)                  | Kubernetes + EC2 (floci) | `build-real`          | **The whole chain, from an estate that names no substrate.** A declared size+pool becomes a CIDR an ALLOCATOR chose and `tofu apply` built; a declared fleet-of-one becomes a host whose address the provider CAUSED; the same converge recipe that serves a hand-declared host installs Apache on it; and a certificate whose key is born on that host, whose subject is derived from the host's own observed address, is signed by a **real CA**. Two gated builds, two drift remediations, four Findings that close. **Read the demo before quoting it: it is two proofs, not one chain.** |

## Roadmap (planned demos)

Each future demo teaches _and closes one gap_ toward the full multi-substrate capstone:

- ~~**app install (with a certificate)**~~ — **shipped** as
  [app-cert](app-cert/README.md), on the fully-featured Ansible plugin ADR-0117 built for it. Note what it
  deliberately does **not** teach: certificate **renewal**. Renewal is a Baseline expiry check against a
  `cert.expiry` Facet projected by a cert-issuer Connector's Syncer — a real CA behind a Connector, not a
  play minting its own. That is its own demo, and this one draws the line rather than blurring it (§1.2 —
  one authority per fact). It is also honest about **PLG-1** ([enterprise-readiness.md](../docs/enterprise-readiness.md)):
  the managed node is one _we_ run, in-cluster; in production it is an operator-owned fleet behind
  bastions, so what the demo proves is the plugin's execution depth, not its reach.
- ~~**enterprise estate (capstone)** — networks/VLANs across regions + shared services across
  Kubernetes, vSphere, and EC2 in one Intent.~~ **Shipped, and narrower than this entry asked for**,
  as [region-to-cert](region-to-cert/README.md). The three things it was waiting on all landed —
  per-instance fan-out (ADR-0058), a K8s Compute provider (ADR-0151), and one reconcile spanning two
  substrates. What did NOT land is the phrase "in one Intent": an `Intent/Subnet` has no honest
  Kubernetes implementation and no dev substrate has both bootable machines and networks, so the
  capstone is **two proofs in one estate** rather than one chain. The demo's README argues that at
  length rather than quietly delivering less than the entry promised.
- **vSphere in the capstone** — region-to-cert binds two substrates; the reference estate already
  binds a third (`vsphere-dc` → vcenter) with the SAME Intent set. Folding that into the capstone
  needs a floor that runs vspheresim beside kind, not new platform work.
- ~~**real SSH converge** (follow-on to ec2-only) — provision _then configure_ over SSH into a floci
  instance.~~ **Withdrawn: it cannot be built, and the reason this entry existed was a false premise.**
  This said "floci gives real SSH-able instances; the open problem is the EE Job (in kind) reaching them
  across the host/kind network boundary." Measured 2026-07-27 (HAR-1 in
  [enterprise-readiness.md](../docs/enterprise-readiness.md)): **floci instances are not SSH-able at all.**
  No AMI ships an sshd binary, user-data is never executed, and `RegisterImage` accepts a custom image
  then ignores it — so the network boundary was never the blocker; there is nothing listening to reach.
  The network half of floci _is_ fully real and stays the substrate for the provisioning leg. A
  provision→configure demo therefore needs a Compute provider whose machines boot, which is what the
  capstone below now carries. Guarded by `TestFociFidelityBoundary` so the claim cannot rot back.

## A note on what actually runs these

The `run.sh` scripts are only the turnkey harness — they call the same `/api/v1` endpoints the UI, CLI, and
MCP agents call (§1.6). **The orchestration is real Temporal**: each demo's Workflow is declarative YAML
compiled to a Temporal DAG, every Step is an activity, and an approval gate is a durable Temporal **signal
wait** (`workflow.GetSignalChannel` + a timeout timer) — which is why a gate can park for 24h with nothing
running. The runner's auto-approval goes through the real `POST /gates/{id}/decision` → authz check →
Temporal signal. Delete the scripts and click through the UI: the same Temporal workflow runs.
