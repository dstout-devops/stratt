# OSS/Apache-2.0 Connector Tool Landscape

**Purpose:** Candidate open-source infrastructure tools for a unified "glue" front-end
connector (Ansible/Linux/Proxmox/ESXi/Kubernetes/Docker/networking/storage/Temporal/etc.),
filtered for compatibility with an Apache License 2.0 product.

**Core licensing principle:** This tool integrates with target systems via CLI, REST/gRPC
API, or SSH — it does **not** embed their source code. License conflicts only arise from
*linking/vendoring/forking* code into our own codebase, not from *orchestrating* an
external process over a network/API boundary (this is exactly how Ansible, GPLv3, safely
manages proprietary and copyleft systems alike). Bucketing below reflects that distinction.

---

## Bucket 1 — Safe to embed / vendor / link code from
Permissive licenses (Apache 2.0, MIT, BSD, MPL 2.0, PostgreSQL License). No restrictions
if we pull in SDKs, client libraries, or fork code directly into our repo.

| Category | Tool | License |
|---|---|---|
| Config Mgmt | SaltStack | Apache 2.0 |
| Config Mgmt | Puppet (core) | Apache 2.0 |
| Config Mgmt | Chef (core) | Apache 2.0 |
| IaC | OpenTofu | MPL 2.0 |
| IaC | Pulumi | Apache 2.0 |
| Containers | Docker Engine / Moby | Apache 2.0 |
| Containers | Podman | Apache 2.0 |
| Containers | Kubernetes | Apache 2.0 |
| Containers | K3s | Apache 2.0 |
| Containers | containerd | Apache 2.0 |
| Containers | CRI-O | Apache 2.0 |
| Containers | Helm | Apache 2.0 |
| Containers | Rancher | Apache 2.0 |
| Workflow | Temporal | MIT |
| Workflow | Cadence | MIT |
| Workflow | Apache Airflow | Apache 2.0 |
| Workflow | Argo Workflows / Argo CD | Apache 2.0 |
| Workflow | Prefect (core) | Apache 2.0 |
| Workflow | Conductor (Netflix) | Apache 2.0 |
| Workflow | StackStorm | Apache 2.0 |
| Networking | Open vSwitch | Apache 2.0 |
| Networking | Calico | Apache 2.0 |
| Networking | Cilium | Apache 2.0 |
| Networking | MetalLB | Apache 2.0 |
| Networking | NetBox | Apache 2.0 |
| Networking | CoreDNS | Apache 2.0 |
| Networking | Nginx | BSD-2 |
| Storage | Longhorn | Apache 2.0 |
| Storage | Rook | Apache 2.0 |
| Monitoring | Prometheus | Apache 2.0 |
| Monitoring | Alertmanager | Apache 2.0 |
| Monitoring | OpenTelemetry | Apache 2.0 |
| Monitoring | Telegraf | MIT |
| CI/CD | Jenkins | MIT |
| CI/CD | Gitea / Forgejo | MIT |
| CI/CD | Tekton | Apache 2.0 |
| CI/CD | Drone CI (core) | Apache 2.0 |
| GitOps | Argo CD | Apache 2.0 |
| GitOps | Flux | Apache 2.0 |
| Secrets/PKI | OpenBao | MPL 2.0 |
| Secrets/PKI | step-ca / smallstep | Apache 2.0 |
| Secrets/PKI | cert-manager | Apache 2.0 |
| Identity | Keycloak | Apache 2.0 |
| Service Mesh | Istio | Apache 2.0 |
| Service Mesh | Linkerd | Apache 2.0 |
| Service Mesh | Envoy | Apache 2.0 |
| API Gateway | Kong (core) | Apache 2.0 |
| API Gateway | Traefik | MIT |
| Cloud/IaaS | OpenStack | Apache 2.0 |
| Cloud/IaaS | CloudStack | Apache 2.0 |
| Cloud/IaaS | Harvester | Apache 2.0 |
| Database | PostgreSQL | PostgreSQL License |
| Database | etcd | Apache 2.0 |
| Messaging | NATS | Apache 2.0 |
| Messaging | Apache Kafka | Apache 2.0 |
| Messaging | RabbitMQ | MPL 2.0 |
| Dev Portal | Backstage | Apache 2.0 |

---

## Bucket 2 — Safe to orchestrate/integrate, but do NOT embed/vendor/link source
Copyleft (GPL/AGPL/LGPL) or otherwise incompatible-for-linking licenses. These remain
excellent **integration targets** — call them via CLI, REST API, SSH, or their official
stable client bindings. Never copy their source into our repo or statically link internals.

| Category | Tool | License | Integration Note |
|---|---|---|---|
| Config Mgmt | Ansible | GPLv3 | CLI/API only, never vendor internals |
| Virtualization | Proxmox VE | AGPLv3 | REST API only |
| Virtualization | KVM/QEMU | GPLv2 | Control via libvirt API |
| Virtualization | libvirt | LGPLv2.1 | Use official language bindings/dynamic linking; confirm terms with legal |
| Virtualization | oVirt | Apache 2.0 (mgmt) / GPLv2 (some components) | Verify per-component |
| Virtualization | XCP-ng / Xen | GPLv2 | API/CLI only |
| Networking | FRRouting (FRR) | GPLv2 | CLI/API only |
| Networking | OPNsense / pfSense | Mixed (mostly BSD, some GPL components) | Verify per-component before vendoring |
| Networking | WireGuard | GPLv2 | Control via `wg`/`wg-quick` CLI only |
| Networking | HAProxy | GPLv2 | Config/API only |
| Storage | Ceph | LGPLv2.1 | Use RBD/RADOS client libraries via dynamic linking |
| Storage | GlusterFS | GPLv2 / LGPL | CLI/API only |
| Storage | ZFS (OpenZFS) | CDDL | CDDL is GPL-incompatible; orchestrate via CLI only, never link |
| Monitoring | Zabbix | GPLv2 | API only |
| Monitoring | Icinga | GPLv2 | API only |
| Monitoring | Grafana | AGPLv3 | Deploy as independent service; do not bundle into our distributed image |
| Monitoring | Loki | AGPLv3 | Same as above |
| Storage | MinIO | AGPLv3 | Same as above — AGPL's network-use clause triggers on bundling/redistribution, not on orchestrating a separately-deployed instance |
| Identity | FreeIPA | GPLv3 | API/CLI only |

**AGPL callout:** AGPL software (Proxmox, Grafana, Loki, MinIO) is safe to *orchestrate* as
an independently-run, separately-deployed service that our tool talks to over the network.
It becomes risky if we *bundle/redistribute* it as part of our own installer, container
image, or product distribution — that can trigger AGPL's source-disclosure obligations.
Keep these as "bring your own instance" integrations, not embedded components.

---

## Bucket 3 — Avoid depending on entirely (non-OSS / source-available / field-of-use restricted)
Not OSI-approved, or carry field-of-use/competitive restrictions inappropriate for an
Apache 2.0 product to depend on or promote.

| Tool | License Issue | Use Instead |
|---|---|---|
| Terraform | BUSL 1.1 (source-available, not OSI-approved) | **OpenTofu** (MPL 2.0) |
| HashiCorp Vault | BUSL 1.1 | **OpenBao** (MPL 2.0) |
| HashiCorp Nomad | BUSL 1.1 | Kubernetes / Nomad OSS pinned to pre-BUSL version (not recommended long-term) |
| n8n | Sustainable Use / Fair-code license | Temporal, Airflow, or StackStorm |
| Redis | RSALv2 / SSPL (as of v7.4+) | **Valkey** (BSD-3, Linux Foundation fork) |
| VMware ESXi / vCenter | Proprietary | N/A — integration-only via public API; no code dependency possible or intended |

---

## Policy / governance decision engines (ADR-0061)

Candidate **Policy Decision Point** engines for the governance decision surface. Stratt's
**built-in** PDP is CEL (the existing `core/internal/rules`, cel-go) + a typed Control library —
**no policy engine is embedded in the content-blind core**; external engines integrate as
**plugins** behind the sovereign policy Contract (subprocess/gRPC), normalised to the four-way
`ALLOW | DENY | REQUIRE_APPROVAL | ESCALATE` `Decision`. Dependency-scout reviewed 2026-07-18.

| Engine | License | Verdict | Integration note |
|---|---|---|---|
| CEL (cel-go) | Apache-2.0 | **built-in** | already in core; hermetic, cost-bounded; the Tier-0 guard + Control-library predicate language. Plan the `google/cel-go`→`cel-expr/cel-go` import-path bump. |
| Open Policy Agent (OPA/Rego) | Apache-2.0 (CNCF graduated) | **plugin — optional-only, never core-bundled** | flagship recommended engine; rich Rego. ~50 transitive deps (embedded KV, WASM runtime, OTel/OCI) must stay behind the plugin boundary — a CI `go.mod`-graph diff guards `core/`. Monitor governance post the 2025 Styra→Apple acquihire (CNCF ownership held). |
| Cerbos (PDP) | Apache-2.0 | **plugin** | Go-native gRPC PDP; YAML+CEL policies. Never depend on the commercial **Cerbos Hub** — OSS PDP over gRPC only. |
| Cedar | Apache-2.0 | **plugin — via Rust reference** | formally-verified RBAC/ABAC. Integrate over subprocess/gRPC against the Rust `cedar-policy` binary; do **not** vendor `cedar-go` (partial parity — lacks the validator/partial-eval that justify Cedar). AWS Verified Permissions = proprietary hosting, integration-only. |
| Kyverno-JSON | Apache-2.0 (CNCF graduated) | **plugin** | validate a compiled OpenTofu/Crossplane **plan** pre-apply (admission PEP). |
| ~~HashiCorp Sentinel~~ | **proprietary (enterprise-only)** | **excluded** | un-embeddable, incompatible with the Apache-2.0 posture; its IaC-guardrail role → OPA/conftest or Kyverno-JSON. |

**Open-core watch:** Cerbos Hub, AWS Verified Permissions (Cedar), and Styra EOPA / OPA Control
Plane are commercial or newly-open *hosting/tooling* layers — depend only on the Apache-2.0 engine
cores, never the managed control planes; verify EOPA's post-2025 OSS license before recommending it.

## Practical Rules for Contributors

1. **Bucket 1** — freely vendor SDKs, fork code, or statically/dynamically link.
2. **Bucket 2** — build a plugin/driver that shells out or calls the tool's stable
   API/CLI/SSH interface. Never copy source from these projects into our repo.
   For AGPL tools specifically, ensure our distribution model treats them as external,
   independently-deployed dependencies (not bundled).
3. **Bucket 3** — do not take a hard dependency on these. Prefer the listed OSS
   replacement, or treat as "optional third-party integration only" in docs/marketing
   so we're never seen as depending on non-OSS infrastructure.
4. When in doubt on a specific component/version (dual-licensed or partially-relicensed
   projects), verify the current license of that exact version before vendoring — several
   projects in this space have changed licenses in the last two years (Terraform, Vault,
   Nomad, Redis, and parts of Elastic/OpenSearch historically).

---

*Generated as a planning artifact for the OSS glue-connector project. Revisit licensing
status periodically — this space has seen frequent relicensing events (2023–2026).*
