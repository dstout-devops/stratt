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
  the gated Workflow → approve → **assert** the outcome. Reproducible and CI-able, so it can't
  silently rot; reuses the proven `genesis-selfdeploy` launch/approve/assert loop. A matching
  **`demo:<name>:down`** tears the demo's footprint back down (full nuke: `task dev:kind:down`), so
  every demo has a clean build-up ⇄ tear-down lifecycle and re-runs from a known state.
- **`README.md`** — the narrated walkthrough: the turnkey one-liner _and_ the by-hand path, with
  "open the UI to watch the descent" (charter §1.8).

Demos are teaching scenarios in their own tree; the single embedded `managed-web` convergence demo in
`estate/` (ADR-0084) remains the reference estate's in-place example. Demos _use_ the reference-estate
patterns — they don't fork them.

## The library

| Demo                                                                   | Substrate         | Fidelity     | Teaches                                                                                                                                                                                                                                                                                                    |
| ---------------------------------------------------------------------- | ----------------- | ------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **[k8s: deploy an app](k8s-deploy/README.md)**                         | Kubernetes (kind) | `real`       | The **write** half: CaC → gated Workflow → `helm/deploy` over the plugin port → the Intent→Run descent, ending in a real Deployment. **Start here.**                                                                                                                                                       |
| **[vSphere: provision a VM + the live graph](vsphere-only/README.md)** | vSphere (vcsim)   | `build-real` | The **read** half joined to write: a Syncer projects the whole topology into a live graph (Views, Facets, Relations), and the _same_ dual-verb plugin provisions a VM into it — the write reflected back in the read-model.                                                                                |
| **[EC2: provision a real instance](ec2-only/README.md)**               | EC2 (floci)       | `real`       | Real provisioning: a gated Workflow runs a genuine `RunInstances` against floci (real SSH-able instance containers, not a mock), and the Syncer observes the new instance appear — the graph coming alive _with_ what you build.                                                                           |
| **[app install with a certificate](app-cert/README.md)**               | SSH (Linux host)  | `real`       | **Configuration management** taken seriously: a real SSH converge with declared privilege escalation, a certificate minted by a collection **pinned into the execution environment at build time**, write-back bounded by declaration — and a Run that reached nothing failing instead of reporting green. |

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
- **enterprise estate (capstone)** — networks/VLANs across regions + shared services across
  Kubernetes, vSphere, and EC2 in one Intent. The app-install rung above is now in place; still needs
  per-instance fan-out (ADR-0058), a K8s Compute provider, and multi-substrate simultaneous reconcile.
- **real SSH converge** (follow-on to ec2-only) — provision _then configure_ over SSH into a floci
  instance (the ADR-0084 pattern). floci gives real SSH-able instances; the open problem is the EE Job (in
  kind) reaching them across the host/kind network boundary.

## A note on what actually runs these

The `run.sh` scripts are only the turnkey harness — they call the same `/api/v1` endpoints the UI, CLI, and
MCP agents call (§1.6). **The orchestration is real Temporal**: each demo's Workflow is declarative YAML
compiled to a Temporal DAG, every Step is an activity, and an approval gate is a durable Temporal **signal
wait** (`workflow.GetSignalChannel` + a timeout timer) — which is why a gate can park for 24h with nothing
running. The runner's auto-approval goes through the real `POST /gates/{id}/decision` → authz check →
Temporal signal. Delete the scripts and click through the UI: the same Temporal workflow runs.
