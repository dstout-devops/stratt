# region-to-cert — the capstone

> **One command:** `task demo:region-to-cert:run` · **Teardown:** `task demo:region-to-cert:down`
> **Fidelity:** `build-real` — the floor of its two legs. The kubernetes leg is `real` on its own;
> the network leg is not, and the weaker one governs the grade. See
> [What is real here](#what-is-real-here), and read it before you quote the demo.

Every other demo in the library proves one seam. This one proves the **whole estate-as-code chain**,
from an estate that does not name a substrate anywhere:

```
declare a region + a size          →  an ALLOCATOR picks the CIDR, tofu builds the subnet
declare "one Linux web server"     →  Kubernetes builds the host, and reports the name it CAUSED
declare "apache"                   →  the same converge recipe that serves a hand-declared host
                                      installs it on a machine that did not exist a minute ago
declare "a certificate"            →  its key is born on that host and never moves, its subject is
                                      derived from the host's own observed address, and a REAL CA
                                      signs it
```

Four declarations. Two gated builds. Two drift remediations. Four Findings that close because the
estate actually converged — not because anything was told to stop complaining.

## The one property worth watching

**Nothing above `estate/capability-bindings/region-to-cert.yaml` names a substrate or a provider.**
Not the Intents, not the Blueprints, not the Assignments, not the composed Workflow. Grep them:

```bash
grep -riE 'kubernetes|aws|kubecompute|opentofu|openbao|netbox' demos/region-to-cert/estate/intents/
```

You will find hits **in comments only** — the runner strips comments and asserts this before it does
anything else, because a portability claim nobody checks is a portability claim that rots.

The binding is where the landscape is chosen (ADR-0151 D2):

```yaml
- capability: provisioning
  substrate: kubernetes # ← change this word; every Compute Intent migrates, together
  intentKind: Compute
```

That is selection by a property the **providers declare**, not by a provider name. Its honest limit
is recorded in ADR-0151 D4: the line moves the _builder_, not provider-shaped `params` an Intent may
be carrying — which is why `web-fleet` carries none.

## Why this is two proofs and not one chain

The plan this demo closes asked for one chain: region → network → **a host on that network** → app →
certificate. Executing it found the limit, and the estate now says so rather than pretending.

- **`Intent/Subnet` on Kubernetes does not exist and should not be built.** Pod addressing is
  cluster-wide and CNI-owned; a Namespace is an API scope, not a segment; NetworkPolicy is
  allow/deny by label selector and carries **no address range at all**. Of the three things a subnet
  means on AWS — an allocated CIDR, a placement target, an isolation boundary — only the third has a
  Kubernetes analogue, and it has no addresses in it. A "kubernetes Subnet provider" could only be
  built by inventing a CIDR nothing honours, or by coupling to Calico/Cilium CRDs underneath a
  portability claim.
- **The aws substrate has networks and no bootable machines.** floci's network write is fully real,
  and its instances are real machine _records_ with nothing listening: no AMI ships `sshd`, user-data
  is never executed, and `RegisterImage` accepts a custom image then ignores it (measured
  2026-07-27, HAR-1). Guarded by `TestFociFidelityBoundary` so the claim cannot rot back.

So no single dev substrate offers both halves, and the placement resolver **correctly refuses** to
place a pod in an AWS subnet (ADR-0147 D3). The capstone is therefore the network leg proven on aws
and build → converge → certificate proven on kubernetes — one estate, one environment, one
reconcile, a **declared** mixed topology (ADR-0151 D2: the resolver refuses a mix it has to guess at
and accepts one an author wrote down).

That is a real gap, stated rather than papered over. Closing it needs a substrate with both, which
is a harness question, not a platform one.

## What each leg actually proves

### Leg 1 — the network (aws)

`estate/intents/app-subnet.yaml` declares a **region, a size and a pool**. It declares no address.

```yaml
params:
  region: us-east-1
  size: 24
  pool: 10.30.0.0/16
```

The `ipam` capability (NetBox) chooses the /24 and the core injects it as `var.stratt_ipam_cidr`;
`tofu apply` runs the committed `aws-network` module against a real EC2 API, with state in a real
S3-compatible store; the built subnet projects back carrying `net.cidr` — what it **actually got**
(§1.2) — and the `stratt.intent/singleton` label the next reconcile matches to decide the Intent is
built.

**The runner asserts the allocated CIDR appears nowhere in the estate.** That is the only way to tell
an allocation from a decoration: if the range were written in Git, NetBox would merely have agreed
with a hand-assignment, and two subnets declaring overlapping literals would collide with nothing to
notice (ADR-0111).

### Leg 2 — the host (kubernetes)

`web-fleet` is `count: 1` and a label. The build is **gated** — §5 Flow 1: a build Finding is
_offered_, never auto-launched, and the runner approves it through the real
`POST /gates/{id}/decision` → authz check → Temporal signal.

The provider then reports `mgmt.address` as the **DNS name it caused** by creating the Service —
not the pod IP, which changes on every restart and cannot be a certificate subject, and not a name
computed from a zone the estate guessed at (ADR-0142 D4: observed or caused, never computed).

### Leg 3 — the app

The built host lands in `view:web-servers` by the `fleet` label the **build** owns, so
`web-servers-apache` covers it without anything having to label it in — which a builder may not even
be permitted to do, since a label key has exactly one owner (ADR-0041).

`apache-configure` names **no View**: it inherits the Assignment's. That is what lets the same recipe
serve a hand-declared host and one built minutes ago. The runner reads the result **off the wire**,
because a file on disk proves a task ran while a served response proves the app is installed — and
then checks `app.config.port` was written with **Run** provenance, since a demo that only checked the
number would pass even if a Syncer had invented it.

### Leg 4 — the certificate

This is the line [app-cert](../app-cert/README.md) drew and deliberately did not cross: its
certificate is minted **by the play itself**, self-signed, because a real CA is a Connector rather
than something a play should be.

Here:

- the **key is born on the target** (`csr-gather`), only the CSR travels, and the signature comes
  back (`sign` → `deliver`). The runner asserts the key is on the host at `0600`;
- the **subject is derived from the host** (ADR-0150 D1) — `commonName:
"{{.entity.mgmt.address.address}}"`. One declaration covers a whole fleet; a 200-host tier is this
  file, not 200 of them. A host missing the named Facet is refused at launch, by name, rather than
  issued a certificate for a guessed subject;
- the runner asserts the issuer is **`Stratt Dev Root CA`** and that issuer ≠ subject, so a
  regression to self-signing fails the demo;
- `cert.presented` is **parsed off the file that landed on the host**, not restated from the request
  (ADR-0150 D5) — so a certificate issued but never delivered is a visible failure rather than a
  green Run.

And renewal is **drift**, not a schedule: `blueprints/certificate.yaml` compares
`cert.presented.notAfter` against the `renewBefore` window the Intent declares. The same declaration
is both the check and the launch parameter, so they cannot disagree about when a certificate is too
close to expiry.

## By hand

```bash
task demo:region-to-cert:run          # everything below, asserted
```

…or drive it yourself once the floor is up (the runner only calls the same `/api/v1` endpoints the
UI, CLI and MCP agents call — §1.6):

```bash
kubectl -n stratt port-forward svc/stratt 18094:8080
H='-H X-Stratt-Principal:bootstrap-admin'

curl -s $H localhost:18094/api/v1/findings?status=open | jq -r '.[] | .baseline + " " + .id'
# provision/app-subnet …   ← a build OFFERED
# provision/web-fleet …

curl -s $H localhost:18094/api/v1/findings/<id>/remediation | jq   # what WOULD be launched
curl -s $H -XPOST localhost:18094/api/v1/findings/<id>/remediation | jq -r .id
curl -s $H localhost:18094/api/v1/gates?status=pending | jq
curl -s $H -XPOST localhost:18094/api/v1/gates/<gate>/decision -d '{"approve":true}'
```

Then open the UI (`cd ui && npm run dev`) and walk the descent: Intent → Finding → Blueprint route →
Workflow → Run → task event. One click per level, all the way down (§1.8).

## What is real here

**Real:** the pod, its sshd, the SSH connection, `become`, Apache serving off the wire, the RSA key
generated on the target, the CSR, the X.509 certificate signed by OpenBao's PKI and read back off the
delivered file. Real Temporal orchestration — each Workflow is declarative YAML compiled to a
Temporal DAG, each Step an activity, and an approval gate a durable signal wait, which is why a gate
can park for 24h with nothing running.

**Build-real:** the network leg. Real `tofu apply` of a real committed module with a committed
`.terraform.lock.hcl`, real state in SeaweedFS, a real NetBox allocation — against **floci**, an EC2
API implementation rather than a mock. The API and the lifecycle are real; the cloud is not.

**The two limits, stated plainly:**

1. **No host is placed in the built subnet.** See [above](#why-this-is-two-proofs-and-not-one-chain).
2. **The built host is a pod in the same cluster** — PLG-1. What this proves is the platform's
   _execution depth_, not its _reach_: a production fleet sits behind bastions, and the
   bastion/ProxyJump path is built and unit-proven with no live proof yet.

## A note on the estate's shape

This is the first demo that admits plugin **estates** rather than contracts. It declares no Actuator
of its own: every provider, build Workflow and converge recipe it drives is one the plugin ships, and
what the demo contributes is the **scenario**. Copying those declarations in would be the
divergent-second-copy defect this repo keeps paying for — the demo would pass against its own copy of
a builder while the shipped one rotted.

Two costs of that admission are visible in the tree and worth reading, because they are honest
findings rather than boilerplate:

- [`estate/environments/prod.yaml`](estate/environments/prod.yaml) exists only because the ansible
  plugin ships prod-scoped Triggers;
- [`estate/views/plugin-owned-secure-hosts.yaml`](estate/views/plugin-owned-secure-hosts.yaml) and
  its sibling exist only because four of that plugin's Workflows name Views from the _reference_
  estate.

A plugin owning its Actuator, Workflows, Triggers and content (ADR-0137 D1) does not license it to
presume its adopter's environments and View names. Both files say so at length, and the fix — moving
those estate-shaped declarations out of the plugin tree — is booked rather than improvised here.

The one genuine duplicate is [`estate/workflows/cert-issue.yaml`](estate/workflows/cert-issue.yaml),
and its header explains why it has no shippable home: a Workflow that composes two plugins may live
in neither, so every adopting estate hand-copies it. That is a gap in the packaging model, not a
choice this demo made.
