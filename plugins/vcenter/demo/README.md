# Demo — vSphere: provision a VM + watch the graph come alive

**What you'll see:** point Stratt at a vSphere datacenter and its Syncer projects the _whole_ topology
into a live, queryable graph — regions, availability zones, datastores, VMs — then you approve one gate
and Stratt provisions a new VM through the vCenter plugin, and watches it appear back in that same graph.
About ten minutes, one command, both halves of the estate model on one substrate.

**Fidelity: build-real.** The substrate is [vcsim](https://github.com/vmware/govmomi/tree/main/vcsim) —
a _real_ vCenter API (govmomi). `create-vm`, power, snapshot, migrate all execute against a real
inventory; there is no guest OS booting. The projection and orchestration are 100% real.

---

## Stratt in one paragraph

Stratt is an estate-automation platform: a typed graph of everything you run, plus a durable
orchestration engine, where every tool is a **plugin** behind one sovereign plugin port. The
[k8s-deploy demo](../k8s-deploy/README.md) showed the **write** half — declare, gate, deploy. This demo
adds the **read** half and joins them: a Connector _observes_ a system of record and projects it as a
rebuildable read-model (charter §1.2 — projections, never a second truth), and the _same_ plugin
provisions into it. Views come alive.

## What this demo teaches

- **The graph as a read-model.** The vCenter Syncer enumerates vSphere and projects typed Entities —
  `region`, `availability-zone`, `datastore`, `host`, `vm` — with **Facets** (typed attributes at named
  seams) like `storage.datastore` and `vm.config`, and **Relations** (`vm --runs-on--> host`,
  `az --in-region--> region`). vSphere stays authoritative; the graph is rebuilt from it.
- **Views are live queries.** [estate/views/](estate/views/) — `dev-vms`, `availability-zones`,
  `datastores` (the last filters on a `storage.datastore` Facet field, so it's the Contract that _pins_
  that schema, §1.1). Each is a saved query over the graph, not a copy.
- **Dual-verb plugins (ADR-0113).** The _same_ vCenter plugin that observes also provisions: the gated
  [vsphere-vm-build](estate/workflows/vsphere-vm-build.yaml) Workflow invokes the targetless
  `vcenter/create-vm` Action, and the build output correlates by identity with what the Syncer sees.
- **The descent (§1.8).** Intent → Workflow → **Run** → task event, inspectable in the UI/CLI/API/MCP.

---

## Run it (turnkey — one command)

Prerequisites: Docker, `go`, `kubectl`, `jq`, and this repo. Then:

```bash
task demo:vsphere-only:run
```

That will (from nothing): bring up kind + a vSphere-only Stratt whose desired state IS this demo's
estate (staged as CaC), start a **vcsim** vCenter simulator, wait for the Syncer's first OBSERVE (the
graph coming alive), then launch the gated `vsphere-vm-build`
Workflow, **auto-approve** its gate, provision `web-01`, and watch the Syncer observe it. It prints the
declared **fidelity** and the live graph counts up front.

## Walk it by hand (the narrated path)

The turnkey runner does these for you; do them yourself to _feel_ both halves.

1. **Stand up the floor.** `task demo:vsphere-only:run` stages [estate/](estate/) into the declarations
   mount and brings up kind + the spine + strattd + the vCenter plugin + vcsim. The vCenter Actuator +
   Actions are **boot-wired** (the chart derives `STRATT_VCENTER_PLUGIN_ADDR` from the plugin, so
   strattd registers the Syncer _and_ `vcenter/create-vm` at boot — ADR-0113).
2. **Watch the graph come alive.** The Syncer's OBSERVE loop enumerates vcsim and projects the topology.
   Open the UI (`cd ui && npm run dev`) → **Views** → `dev-vms` (the ~25 VMs), `availability-zones`, and
   `datastores`. Or `GET /api/v1/views/dev-vms/entities`. This is the read-model — rebuildable, not a
   second truth.
3. **Launch the build Workflow.** **Workflows → vsphere-vm-build → Run** (or
   `POST /api/v1/workflows/vsphere-vm-build/runs`). It parks on the gate.
4. **Approve the gate** as a `platform-admins` member. The `build` Step runs `vcenter/create-vm` against
   vSphere.
5. **Watch the descent + the closure.** Descend the **Run** in the UI (Workflow → Run → the
   `vcenter/create-vm` task event). Then re-open `dev-vms` — the Syncer's next OBSERVE picks up `web-01`,
   so your _write_ is now visible in the _read-model_. The loop is closed.

## What you just learned

You saw both halves of the estate model on one substrate: a Connector projecting a system of record into
a live typed graph (Views, Facets, Relations), and the _same_ plugin provisioning into it through a
gated, audited Workflow — the write reflected back in the read. Everything larger — many substrates in
one graph — is _more of this shape_.

## Clean up

```bash
task demo:vsphere-only:down   # uninstall stratt + stop vcsim (kind kept for a fast re-run)
task dev:kind:down            # full teardown — delete the kind cluster
task dev:down                 # stop the whole compose substrate (incl. vcsim)
```

## What's next in the series

The library ([../README.md](../README.md)) grows toward the full multi-substrate **enterprise estate**:
an **ec2-only** demo (real SSH converge), then the **capstone** — one Intent spanning networks/VLANs
across regions and shared services across Kubernetes, vSphere, and EC2 in a single graph.
