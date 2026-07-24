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
- **`estate/`** — the scenario's Config-as-Code (Views / Workflows / Intents / …), applied with
  `stratt apply -d demos/<name>/estate` on top of a running Stratt. Normal estate CaC — no second
  config language (charter §1.1).
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

| Demo                                                                   | Substrate         | Fidelity     | Teaches                                                                                                                                                                                                                          |
| ---------------------------------------------------------------------- | ----------------- | ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **[k8s: deploy an app](k8s-deploy/README.md)**                         | Kubernetes (kind) | `real`       | The **write** half: CaC → gated Workflow → `helm/deploy` over the plugin port → the Intent→Run descent, ending in a real Deployment. **Start here.**                                                                             |
| **[vSphere: provision a VM + the live graph](vsphere-only/README.md)** | vSphere (vcsim)   | `build-real` | The **read** half joined to write: a Syncer projects the whole topology into a live graph (Views, Facets, Relations), and the _same_ dual-verb plugin provisions a VM into it — the write reflected back in the read-model.      |
| **[EC2: provision a real instance](ec2-only/README.md)**               | EC2 (floci)       | `real`       | Real provisioning: a gated Workflow runs a genuine `RunInstances` against floci (real SSH-able instance containers, not a mock), and the Syncer observes the new instance appear — the graph coming alive _with_ what you build. |

## Roadmap (planned demos)

Each future demo teaches _and closes one gap_ toward the full multi-substrate capstone:

- **enterprise estate (capstone)** — networks/VLANs across regions + shared services across
  Kubernetes, vSphere, and EC2 in one Intent. Depends on per-instance fan-out (ADR-0058), a K8s
  Compute provider, and multi-substrate simultaneous reconcile — built up by the demos above.
