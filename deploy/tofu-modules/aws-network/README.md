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

## Bring-up: DONE (2026-07-28, ADR-0145)

All three items below were open for months because there was no `tofu` binary in this container to
close them. There is now (`task tools:tofu`, pinned 1.12.5, sha256-verified).

1. **The provider lockfile is generated and COMMITTED** (`hashicorp/aws` 5.100.0; hashes for
   `linux_amd64`/`linux_arm64`/`darwin_arm64`). This was **ADR-0112 D4's binding condition** for
   shipping the module at all (§7.3), and it is why the module was not release-ready. Regenerate with
   `task tofu:lock` when `required_providers` moves, and commit the result.
2. **`tofu validate` runs in `task ci`** (`task tofu:validate`) — hermetic, with its working state in
   gitignored `.bin/` so the gate never depends on what ran before it.
3. **The live run happened**: `task dev:tofu:proof` applies this module against the real floci EC2 API
   with real S3 state, through the `opentofu/apply` Action a launched `Intent/Subnet` build invokes,
   and asserts the subnet from the API independently. See ADR-0145's Consequences for what it checks.

**A note that cost an hour:** floci is genuinely **region-scoped**, like AWS. A subnet this module
creates in its default `eu-west-1` is correctly invisible to a reader querying `us-east-1` — which
reads exactly like "the build created nothing". Pin one region across the module and every reader.

## Open mechanism — SETTLED by ADR-0145 D1

ADR-0112's follow-up #7 asked whether a workspace-scoped Actuator builder needs a synthetic/anchor
View or a targetless `opentofu/apply` wrapper. It is **a targetless Action**. A `tofu apply` converges a
workspace, not a set of graph Entities, so it has no View to actuate against; and only the Action seam
carries the estate overlay that a build's `stratt.intent/singleton` correlation label must ride — it
cannot come out of this module, because the plugin refuses any `stratt.*`-prefixed label in
`stratt_entities` (see the note in `outputs.tf`). What that form does NOT get is the ADR-0047 plan pin;
ADR-0145 D3 states the gap rather than hiding it.
