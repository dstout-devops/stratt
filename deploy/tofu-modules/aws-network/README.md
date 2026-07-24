# aws-network — the OpenTofu AWS network module (ADR-0112 B3)

The module the `opentofu-network` `provisioning` provider runs to build a VPC + subnet + SG +
route-table + IGW, from a **NetBox-allocated CIDR** (ipam, ADR-0111) with **S3 tofu state**
(statestore, ADR-0105), against the **floci** real-EC2 backend (ADR-0093).

## How it's wired

- **Mounted** into the opentofu plugin pod at `${STRATT_TOFU_MODULE_ROOT}/aws-network` (default
  `/modules/aws-network`); invoked with `params.module = "aws-network"`.
- **`stratt_ipam_cidr`** is injected as a `-var` by the plugin from the resolved `ipam` handle
  (ADR-0112 D3) — the module never picks a CIDR.
- **`backend "s3" {}`** is filled by the injected `statestore` handle via `-backend-config`.
- **`stratt_entities`** (`outputs.tf`) projects the subnet Entity by `aws.subnetId` (D5); the awsec2
  Syncer writes the `net.subnet` Facet by OBSERVE — one co-owned Entity, no fourth writer.

## Bring-up steps that need a real `tofu` (deferred deployment validation — no tofu binary in the dev image)

1. **Generate + commit the provider lockfile (§7.3, ADR-0112 D4).** `tofu init` in this dir against
   the pinned `aws ~> 5.60` provider, then commit the resulting **`.terraform.lock.hcl`** (provider
   hash pins). It cannot be hand-authored — the hashes must match the real provider. **Until it is
   committed, this module is not release-ready.**
2. **`tofu validate`** — this module has not been validated in-repo (no tofu binary here); confirm it
   at bring-up.
3. **The live floci run** — `tofu apply` landing a real VPC/subnet on floci, and a VM SSH-able in it
   (the DeepWiki-vs-docs conflict on floci's instance realness is settled here — ADR-0112 D7).

## Open mechanism (the build Workflow — ADR-0112 follow-up)

`provisions: {Subnet: opentofu-subnet-build}` names a build Workflow, but a Workflow Step is either an
_actuation_ (`viewName + actuator`) or a _targetless Action_. Crossplane's `subnet-build` uses a
targetless **Action** (`crossplane/provision`); the opentofu **Actuator**'s apply is _workspace-scoped_,
so `opentofu-subnet-build` needs either a synthetic/anchor View for the actuation **or** a targetless
`opentofu/apply` Action wrapper. This is a real design decision (see ADR-0112 follow-ups), resolved at
bring-up alongside the live run — not guessed at here.
