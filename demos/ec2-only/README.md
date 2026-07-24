# Demo — EC2: provision a real instance through a gated Workflow

**What you'll see:** declare an EC2 instance in Config-as-Code, approve one gate, and Stratt provisions
a **real** instance — then its Syncer observes the new instance appear in the graph. About ten minutes,
one command, a real EC2 instance at the end.

**Fidelity: real.** The substrate is [floci](https://github.com/floci-io/floci) (ADR-0093) — a real-host
EC2 dev backend that launches _actual_ Docker containers as EC2 instances (SSH-able, real lifecycle,
real EC2 API). It is **not** a mock (it replaced moto). `RunInstances` really launches a Linux instance.

---

## Stratt in one paragraph

Stratt is an estate-automation platform: a typed graph of everything you run, plus a durable
orchestration engine, where every tool is a **plugin** behind one sovereign plugin port. The
[vSphere demo](../vsphere-only/README.md) showed provisioning against a real _API_ (vcsim) with no guest
OS. This demo goes all the way to **real fidelity**: a real EC2 API _and_ a real running instance — and
starts from an empty cloud, so you watch the graph come alive _with_ the instance you build.

## What this demo teaches

- **Real provisioning over the plugin port.** The gated
  [compute-build](estate/workflows/compute-build.yaml) Workflow invokes the targetless
  `awsec2/create-vm` Action — a genuine `RunInstances` against floci (ADR-0058/0095).
- **The gate.** Nothing is created until a `platform-admins` approver says so — a first-class, audited
  decision.
- **Build → observe closure.** floci starts empty; after the build, the awsec2 Syncer's OBSERVE loop
  picks up the freshly-provisioned instance and it appears in the
  [ec2-instances](estate/views/ec2-instances.yaml) View — the write reflected in a live read-model
  (charter §1.2 — projections, never a second truth).
- **The descent (§1.8).** Intent → Workflow → **Run** → task event, inspectable in the UI/CLI/API/MCP —
  and on failure the Run now carries the real cause (`GET /runs/{id}.error`).

---

## Run it (turnkey — one command)

Prerequisites: Docker, `go`, `kubectl`, `jq`, and this repo. Then:

```bash
task demo:ec2-only:run
```

That will (from nothing): bring up kind + an EC2-only Stratt whose desired state IS this demo's estate
(staged as CaC), start a **floci** real-host EC2 backend, launch the gated `compute-build` Workflow,
**auto-approve** its gate, provision `web-01`, and wait for the Syncer to observe it. It prints the
declared **fidelity** up front.

## Walk it by hand (the narrated path)

1. **Stand up the floor.** `task demo:ec2-only:run` stages [estate/](estate/) into the declarations
   mount and brings up kind + the spine + strattd + the awsec2 plugin + floci. The awsec2 Actions +
   instance Syncer are **boot-wired** (the chart derives `STRATT_AWS_PLUGIN_ADDR` from the plugin, and
   `STRATT_AWS_INTERVAL` enables the Syncer — ADR-0095).
2. **Launch the build Workflow.** In the UI (`cd ui && npm run dev`) → **Workflows → compute-build → Run**
   (or `POST /api/v1/workflows/compute-build/runs`). It parks on the gate.
3. **Approve the gate** as a `platform-admins` member. The `build` Step runs `awsec2/create-vm` — a real
   `RunInstances` against floci.
4. **Watch the graph come alive.** Open **Views → ec2-instances**: within a Syncer cycle the new instance
   appears (keyed on `aws.instanceId`). Your _write_ is now visible in the _read-model_.
5. **Watch the descent.** Descend the **Run** in the UI: Workflow → Run → the `awsec2/create-vm` task
   event.

## What you just learned

You provisioned a real cloud instance through a gated, audited Workflow and watched it reflected back in
a live graph — real fidelity, the write and read halves closed. The enterprise capstone is _more of this
shape_: many substrates, one graph, one Intent.

## Clean up

```bash
task demo:ec2-only:down   # uninstall stratt + stop floci (stops the instance containers too)
task dev:kind:down        # full teardown — delete the kind cluster
task dev:down             # stop the whole compose substrate (incl. floci)
```

## What's next in the series

The library ([../README.md](../README.md)) grows toward the full multi-substrate **enterprise estate**:
the **capstone** — one Intent spanning networks/VLANs across regions and shared services across
Kubernetes, vSphere, and EC2 in a single graph. A follow-on can add the **real SSH converge** (ansible
over SSH into a floci instance, the ADR-0084 pattern) — provision _then_ configure, end to end.
