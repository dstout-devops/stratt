# ADR 0156 — How to reach a host is OBSERVED, not declared

- **Status:** **ACCEPTED** (promoted 2026-08-01 by the steward; proposed 2026-07-31). Promoted on
  evidence rather than age: the transport is **live-proven end to end** — `demo:region-to-cert` and
  `demo:scale-fleet` converge kubecompute-built pods over `kubectl exec` with a brokered kubeconfig,
  `ansible-runner rc=0`, under `task e2e:live` (EXIT=0, all six demos). **D4a was added on the way**:
  the transport shipped with no reach credential at all and could not authenticate, which only a real
  converge exposed. Charter review by hand — this session's rules bar the
  subagent; §1.1/§1.2/§1.4/§1.5/§1.8/§2.4/§2.5/§9 answered inline. **No new runtime dependency**
  (three new EE content variants; the control plane gains nothing).
- **Date:** 2026-07-31
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams), §1.2 (projections, never a second truth), §1.4 (boring
  spine — core authors no tool key), §1.5 (opaque params), §1.8 (never hide diagnosis), §2.4 (no two
  facts that can disagree), §2.5 (credentials brokered), §9 (no ontology creep)
- **Extends ADR-0084** (the address/credential split) with the transport half. **Scopes ADR-0153
  D1/D6** rather than superseding them — see D5.

## Context

The question that produced this ADR was a plain one: _can we change a `count` from 1 to 3 and get
three more machines, across vSphere, EC2 and Kubernetes?_ The build half answers yes. The **converge**
half did not, and the reason was assumed rather than checked: "every substrate needs sshd and a
network path to port 22."

Measuring the actual Ansible collections falsified that. **All three substrates ship a native
connection plugin, and none of them uses SSH:**

| Substrate  | Plugin                          | Reaches the guest via              | Guest needs                       | Control node needs             |
| ---------- | ------------------------------- | ---------------------------------- | --------------------------------- | ------------------------------ |
| Kubernetes | `kubernetes.core.kubectl`       | `kubectl exec`                     | **nothing**                       | a **kubeconfig** (`pods/exec`) |
| vSphere    | `community.vmware.vmware_tools` | VMware Tools guest ops via vCenter | VMware Tools + guest credentials  | vCenter credentials            |
| EC2        | `amazon.aws.aws_ssm`            | an AWS SSM Session                 | SSM Agent, instance profile, curl | AWS credentials                |

> **The last column was added 2026-08-01, and its absence was not cosmetic.** The original table had
> only "Guest needs", so kubectl's requirement read as **nothing** — which is true of the guest and
> false of the run. Reaching a pod needs no sshd, no agent and no account on the target, and it still
> needs a credential on _this_ side, because the execution pod is deliberately spawned with
> `AutomountServiceAccountToken: false` — "the pod has no cluster identity". A column that asks only
> about the far end of a connection cannot describe a transport whose whole cost is at the near end.
> See D4.

Two of those change what is possible rather than merely how it is spelled:

- **`kubectl` needs nothing in the guest.** The capstone SSHes into a pod today, which forces
  `kubecompute` to build pods running `sshd` with authorized keys — a coupling between a provider and
  a connection method that exists only because the connection method was assumed.
- **`vmware_tools` needs no network path to the guest at all**, because it goes through vCenter's
  guest-operations API. That is how an isolated-segment VM is converged, and it is a large part of
  the case W0.1's bastion work exists for.

And the harness gap that prompted all this — floci ships no `sshd` (HAR-1) — turns out **not** to be
the blocker it looked like: two of three substrates never needed one.

### Why the Step is the wrong home for this

ADR-0153 put `connection.type` on the Step. That is right for a device an operator names, and wrong
for a host a provider built, for the reason ADR-0151 already settled: **no declaration above the
provider should name a substrate.** If a converge Workflow has to say `type: kubectl`, then
`web-servers-apache` — one Assignment over one View — needs a second copy for vSphere and a third for
EC2, and the estate is naming substrates again in the one place ADR-0151 removed them from.

Worse, it makes a **mixed-substrate View unconvergeable**: connection settings rendered as inventory
GROUP vars are one value for the whole Run, so a View holding a pod, a VM and an EC2 instance cannot
be converged at all.

## Decision

### D1 — `mgmt.transport` is an observed Facet beside `mgmt.address`

The Syncer that observed a host also observes **how to reach it**. This is exactly ADR-0084's shape
extended by one fact: `mgmt.address` says WHERE, `mgmt.transport` says BY WHAT MEANS, and both are
projections of what the provider actually did (§1.2) rather than something an estate asserts.

Multi-writer by design, and already precedented: `mgmt.address` is written by the declared-estate
Syncer, the vcenter Syncer and build project-backs alike, under cross-source per-key Facet ownership
(ADR-0041/0042). `mgmt.transport` follows it — `kubecompute` writes `kubectl`, `vcenter` writes
`vmware_tools`, `awsec2` writes `aws_ssm` or `ssh`.

### D2 — it crosses the port as a legible KIND and an OPAQUE coordinate

`ApplyTarget.transport` carries `{kind, coordinates}`: `kind` is a legible string, `coordinates` is
the validated Facet document as bytes.

**Core never branches on `kind`.** It carries it and logs it, exactly as it carries `address` — that
is what keeps this from becoming a closed set of substrates the spine knows about (§9), while still
leaving a Run's descent legible (§1.8). The coordinates stay opaque for the §1.5 reason `params` are
opaque: the shape is the _transport's_, typed by the pinned `mgmt.transport` schema, and a core that
parsed it would be learning what a Kubernetes namespace is.

A typed per-kind message was the alternative and it fails the same test: the proto would have to
enumerate every substrate, and adding one would be a port change.

### D3 — the shim renders PER-HOST vars, so a mixed-substrate View converges in ONE Run

`buildInventory` already writes per-host vars (`ansible_host`, `ansible_port`, and
`ansible_connection=local` for the reserved local address). The transport renders the same way:

```
[all]
web-01 ansible_connection=kubectl ansible_kubectl_namespace=stratt-hosts ansible_kubectl_pod=web-01
web-02 ansible_connection=vmware_tools ansible_vmware_guest_path=/DC0/vm/web-02
web-03 ansible_connection=aws_ssm ansible_aws_ssm_instance_id=i-0abc ansible_aws_ssm_region=eu-west-1
web-04 ansible_host=10.0.1.9
```

One Run, four substrates, and **the Intent, the Blueprint and the Assignment name none of them.**
That property is the whole point: it is the converge-side equivalent of what ADR-0151 did for builds.

The SHIM authors every `ansible_*` key, as always (§1.4, ADR-0084 D3). Core hands over a typed
coordinate and learns no ansible vocabulary.

### D4 — the Facet carries COORDINATES, never credentials

`mgmt.transport` holds what identifies the target — a pod and namespace, a VM path or uuid, an
instance id and region. It holds no vCenter password, no guest password, no AWS key, no kubeconfig.

That is ADR-0084 D4's split applied to the transport: the address half is observed and public, the
credential half is the Step's brokered `CredentialRef` (§2.5). `vmware_tools` needs guest credentials
and `aws_ssm` needs AWS credentials — both arrive the way every other credential does, and the shim
resolves them at their mount.

#### D4a — kubectl's reach credential, and the sentence above that was wrong by omission (2026-08-01)

This decision listed the credential every transport needs **except kubectl**, whose row said
"nothing". That was written from the guest's point of view and it made the transport look free. It is
not, and the gap was found the only way it could be — by running it. `demos/region-to-cert` is the
first thing in this repo to actually converge over the kubectl transport, and it failed:

```
runner_on_unreachable: Failed to create temporary directory. In some cases, you may have been
able to authenticate and did not have permissions on the target directory.
```

Every word of which points at the target's filesystem. The pod was healthy and the identical `mkdir`
succeeded when run with permission. The real cause was the **API server refusing `kubectl exec`**,
because the execution pod holds no credential at all: `dispatch.go` spawns it with
`AutomountServiceAccountToken: false`, commented "the pod has no cluster identity".

**That property is kept, not traded away.** An execution pod runs arbitrary automation content; an
ambient token that can exec into any pod in the cluster would make every Run a lateral-movement
primitive. §2.5 wants reach authority brokered and use-granted, not ambient — so the answer is the
one this decision already prescribes for the other two transports, applied to the one it skipped:

- **`connection.kubeconfigRef`** (`ansible.input.v9`, a sibling of v8 — the loader takes the highest
  version and every v8 declaration renders identically) names a `CredentialRef` already on the Step.
  The shim renders its **mount path** as the `ansible_kubectl_kubeconfig` group var. A path, never a
  value; the kubeconfig is not staged (kubectl applies no mode check, and a second copy of a bearer
  credential is a second place it can leak from).
- **The grant is scoped to `create pods/exec` + `get pods` in the hosts' namespace.** A kubeconfig is
  a bearer credential, so the token's reach _is_ the blast radius of any content any Run executes.
  `task dev:kubecompute:up` mints exactly that and nothing wider.
- **The shim refuses the Run when a kubectl-transported target has no brokered kubeconfig**, naming
  the missing field and the reason. This is a §1.8 fix as much as a §2.5 one: the failure it replaces
  named the guest for a control-node authorization failure, and an operator following that message
  would go and chmod a directory on a pod that was never the problem.

**Why the existing guards did not catch it.** D6 checks what the EE _contains_ — the collection and
the binary — and both were present, so nothing refused. Content and credential are independent axes;
there are now three, and the credential one is a declaration-level question that needs no filesystem.

**The general lesson, recorded because it is the second time on this arc:** a transport is not
shipped when it renders, and it is not shipped when the image carries its collection. It is shipped
when something has reached a host with it. D7 already cost this once with `ansible-doc`; the
correction there was to stop trusting a check that could not fail, and the correction here is the
same shape one layer out — `demos/scale-fleet` asserted the Facet was **observed** and narrated it as
"a host that CONVERGES, over `kubectl exec`", a converge it never ran. Both demos' claims are now
bounded by what they execute.

### D5 — a Step-declared type and an observed transport are REFUSED together, never resolved

ADR-0153's `connection.type` is **scoped, not removed**, and the line is a real one:

- A host a **provider built** is observed, so its transport is a Facet. Kubernetes, vSphere, EC2.
- A **network device** is discovered, not built — nothing provisions a switch, so no Syncer observes
  a transport for it. `network_cli`/`netconf` stay Step-declared, which is correct.

When a target carries `mgmt.transport` AND its Step declares `connection.type`, the shim **fails and
names both**. It does not pick. Two homes for one fact resolved by a precedence rule is exactly what
§2.4 refuses, and it is the same refusal ADR-0153 D6 already makes for a `local` target under a
non-ssh type.

### D6 — a transport whose tooling the EE lacks is refused BEFORE the run, on two axes

ADR-0153 D7 established this and it now has a second axis, because these transports need more than a
collection:

| Transport      | Collection         | Binary on the control node |
| -------------- | ------------------ | -------------------------- |
| `kubectl`      | `kubernetes.core`  | `kubectl`                  |
| `vmware_tools` | `community.vmware` | — (a Python library)       |
| `aws_ssm`      | `amazon.aws`       | `session-manager-plugin`   |

The collection half reuses D7's read of `/etc/stratt/ee-content.json`. The binary half is
`exec.LookPath`, which — unlike `ansible-doc`, whose exit code D7 measured as useless — actually
answers. Both fail with the missing thing named, before the play runs, instead of an exception from
inside a connection plugin naming a module the estate never wrote.

## Consequences

**The original question becomes answerable across all three substrates**, and the answer is one
`Intent/Application`, one Assignment, one Run. That is the config-as-code claim, and it is now a
property of the model rather than of the demo.

**`kubecompute` can stop shipping sshd in its pods.** That coupling is retired rather than worked
around — though the SSH path stays supported, because a pod an operator wants to reach over SSH is
still legitimate.

**floci's missing sshd stops blocking the EC2 converge story** — `aws_ssm` is the production path for
a large share of EC2 estates anyway. It does **not** become provable in dev: floci simulates the EC2
API, not Systems Manager, so an SSM converge still needs real AWS. Stated, not smoothed over.

**Three EE content variants**, and the platform floor does not move — the floor is bounded to what the
platform's own shipped content requires (ANS-012's rule), and no shipped content root speaks to a
substrate. An adopter selects the variant from an Actuator declaration (ADR-0117 D3).

**What this does not do:** it does not make any of the three transports live-proven. `kubectl` is
provable on kind today and will be; `vmware_tools` needs a vCenter with real VMware Tools, which
vspheresim does not implement; `aws_ssm` needs real AWS. The parity docs must say which is which,
because a shipped connection type is not a proven one — the lesson ADR-0153 D7 already cost once.
