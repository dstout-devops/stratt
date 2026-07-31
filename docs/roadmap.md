# Roadmap & phase status

Living status tracker for the phased plan in **[stratt-charter.md](../stratt-charter.md) §8**. The charter
is the authority on _what each phase is_; this file records _where we actually are_ against it, with
evidence. Update it when a phase deliverable lands or a gate is met.

**Two things gate a phase, and they are different:**

- **Code deliverables** — the capabilities a phase ships. These are built here and verifiable in the repo.
- **Promote / OSS gates** — real-world conditions (daily-driver adoption, N-day zero-data-loss, an SLO
  window, a security review, going public with OSPO clearance). **Code cannot satisfy these** — they need
  operation, time, and org/legal steps. A phase can be "code-complete" while its exit gate is still open.

Legend: ✅ done · 🔶 partial · ⏸ deferred (deliberate) · 🚫 blocked · ⬜ not started

---

## Phase 0 — Spike ✅

The thesis slice. **Done** — go/no-go recorded in [ADR-0008](adr/0008-phase0-go-no-go-measurements.md).

| Deliverable                                               | State | Evidence                                                                                                          |
| --------------------------------------------------------- | ----- | ----------------------------------------------------------------------------------------------------------------- |
| Entity/Facet/Provenance store                             | ✅    | `core/internal/graph` (migration `00001_graph_spine`)                                                             |
| One native Syncer (vCenter-class)                         | ✅    | `plugins/vcenter` (re-centered from in-tree, ADR-0046; [ADR-0007](adr/0007-phase0-syncer-sdk-and-dev-harness.md)) |
| View query → Temporal Workflow → K8s Job (ansible-runner) | ✅    | `orchestrate`, `dispatch`, `plugins/ansible` (EE-Job transport, ADR-0051)                                         |
| Facts projected back with provenance                      | ✅    | `graph.RunProjector`, `orchestrate.ProjectFacts`                                                                  |
| Live SSE tail                                             | ✅    | `events.Bus.Tail`, `GET /runs/{id}/events`                                                                        |

## Phase 1 — Usable core 🔶 (code ✅ · exit gate 🚫)

**Code-complete.** The promote gate (Nebulae daily-driver, 30 days zero data loss) and the **OSS gate
(repo public with DCO/ADRs/quickstart)** are **not met** — the repo stays private until OSPO/IP clearance
(charter §7.4, the highest project risk). So Phase-1 _work_ is done; its exit gate is blocked.

| Deliverable                                                      | State | Evidence                                                                                                           |
| ---------------------------------------------------------------- | ----- | ------------------------------------------------------------------------------------------------------------------ |
| Ansible Actuator (EEs, per-target results, slicing)              | ✅    | `plugins/ansible` (EE-Job shim, ADR-0051), `RunInput.Slices`                                                       |
| `script` Actuator                                                | ✅    | `plugins/script` (EE-Job shim, ADR-0046)                                                                           |
| Git desired-state sync + `stratt apply`/`plan`                   | ✅    | `desiredstate`, `POST /desired-state/{plan,apply}`                                                                 |
| Views UI                                                         | ✅    | `ui/` ([ADR-0012](adr/0012-views-ui-v1.md))                                                                        |
| Workflows + Gates                                                | ✅    | `orchestrate.RunDAG`, `DecideGate` ([ADR-0011](adr/0011-workflows-gates-v1.md))                                    |
| Schedules                                                        | ✅    | `triggers`, Temporal Schedules ([ADR-0010](adr/0010-triggers-v1-schedules.md))                                     |
| CredentialRefs (Vault + K8s)                                     | ✅    | `dispatch.CredentialMount` ([ADR-0009](adr/0009-identity-authz-credential-brokering.md))                           |
| OIDC + basic OpenFGA                                             | ✅    | `authz` (OpenFGA + tuples), OIDC resolver                                                                          |
| Helm chart                                                       | ✅    | `deploy/charts/stratt` ([ADR-0013](adr/0013-helm-packaging.md))                                                    |
| MS Graph + cloud-instance Syncers                                | ✅    | `plugins/msgraph`, `plugins/awsec2` (re-centered, ADR-0046; [ADR-0014](adr/0014-connector-breadth-msgraph-ec2.md)) |
| **Promote:** Nebulae daily-driver, 30d zero data loss            | ⬜    | operational, not code                                                                                              |
| **OSS gate:** OSPO clearance → repo public (DCO/ADRs/quickstart) | 🚫    | charter §7.4 blocker                                                                                               |

## Phase 2 — Seams + intent layer ✅ (code)

**Code-complete.**

| Deliverable                                                                                              | State | Evidence                                                                                                                                                             |
| -------------------------------------------------------------------------------------------------------- | ----- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| OpenTofu Actuator (plan/apply Gates, encrypted HTTP state backend, output→Contracts)                     | ✅    | `plugins/opentofu` (re-centered, ADR-0046), `statebackend` (core transport) ([ADR-0016](adr/0016-opentofu-actuator.md)/[0017](adr/0017-tofu-outputs-to-entities.md)) |
| Trigger engine (webhook + Alertmanager Emitters, CEL)                                                    | ✅    | `triggerengine`, `emitters`, `rules` ([ADR-0018](adr/0018-trigger-engine.md))                                                                                        |
| Intent/Assignment/Blueprint compiler (claim types, ownership registry, membership-delta, max-delta gate) | ✅    | `compiler` ([ADR-0023](adr/0023-intent-compiler.md))                                                                                                                 |
| Baselines + Findings v1 (check-mode + tofu plan, flap damping)                                           | ✅    | `baselines`, `graph.findingstore` ([ADR-0019](adr/0019-baselines-findings-v1.md))                                                                                    |
| MCP actuator/Action adapter + platform MCP server                                                        | ✅    | `plugins/mcp` (EE-Job transport, ADR-0053), `mcpserver` (core agent-native surface) ([ADR-0021](adr/0021-platform-mcp-server.md)/[0022](adr/0022-mcp-actuator.md))   |
| AWX importer + `/api/v2` façade                                                                          | ✅    | `awximport`, `awxfacade` ([ADR-0025](adr/0025-awx-importer-and-ansible-scm-content-ref.md)/[0026](adr/0026-awx-api-v2-facade.md))                                    |
| Notifications                                                                                            | ✅    | `notify` ([ADR-0027](adr/0027-notifications.md))                                                                                                                     |

## Phase 3 — Enterprise + fleet 🔶 (code ~90% · gates 🚫)

Substantially built. Two Connectors are **deliberately deferred** (no current need or environment to
connect to — revisit when a real tenant requires them); the promote/OSS gates are open.

| Deliverable                                                                                        | State | Evidence                                                                                                                                                                                              |
| -------------------------------------------------------------------------------------------------- | ----- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Sites (NATS leaf)                                                                                  | ✅    | `sitegw`, `siteproto`, `cmd/stratt-agent` ([ADR-0032](adr/0032-sites-remote-execution-loci.md))                                                                                                       |
| Full OpenFGA (View-scoped execution, use-without-read)                                             | ✅    | `authz.RelationRunner`/`RelationUser` ([ADR-0028](adr/0028-view-scoped-execution-authz.md))                                                                                                           |
| HA + DR runbook                                                                                    | ✅    | [ADR-0040](adr/0040-high-availability-and-disaster-recovery.md), [runbooks/ha-dr.md](runbooks/ha-dr.md)                                                                                               |
| audit → SIEM sink                                                                                  | ✅    | `forwarder`, `cmd/stratt-forwarder` ([ADR-0034](adr/0034-audit-stream-and-siem-forwarder.md))                                                                                                         |
| SCIM                                                                                               | ✅    | `scim` ([ADR-0035](adr/0035-scim-service-provider.md))                                                                                                                                                |
| Pull agent + Bundles                                                                               | ✅    | `cmd/stratt-agent` (pull), `bundle` ([ADR-0032](adr/0032-sites-remote-execution-loci.md))                                                                                                             |
| Evidence store (object-lock) + CIS pack                                                            | ✅    | `evidencestore`, `packs/cis` ([ADR-0029](adr/0029-evidence-store-object-lock.md)/[0033](adr/0033-cis-pack-compliance-as-data.md))                                                                     |
| `Intent/Certificate` + `Intent/FileSet` + `Intent/Access` GA                                       | ✅    | `plugins/certissuer` (Syncer + reconcile Actuator, ADR-0050), `types.Intent{Certificate,FileSet,Access}` ([ADR-0030](adr/0030-intent-certificate-ga.md)/[0036](adr/0036-intent-fileset-access-ga.md)) |
| **Jamf Connector**                                                                                 | ⏸     | deferred — no current need/environment                                                                                                                                                                |
| **ConfigMgr (SCCM AdminService) Connector**                                                        | ⏸     | deferred — no current need/environment                                                                                                                                                                |
| **Promote:** production for a bounded service class; 99.9% 30-day SLO; security review             | ⬜    | operational, not code                                                                                                                                                                                 |
| **OSS gate:** v1.0; ≥2 external maintainers; ≥3 community plugins; CNCF Sandbox; vocabulary freeze | 🚫    | gated by §7.4 going-public                                                                                                                                                                            |

## Phase 4 — Consolidation ⬜ (not started as a phase)

Cross-domain patch rings, self-service portal, cost analytics, Helm/Packer Actuators, ServiceNow push,
CRD interface, verified-plugin registry, ACP addressability. Not begun as planned Phase-4 work — **but see
below.**

---

## Cross-cutting: the substrate re-centering (dark-matter, ADR-0046 arc)

Orthogonal to the phase plan, the platform underwent its largest architectural shift: the **dark-matter
re-centering** ([ADR-0046](adr/0046-stratt-as-substrate.md)). The Apache-2.0 core is now a thin,
**content-blind spine** — graph / coordinates / contracts / reconcile / authz / audit with **zero tool
domain logic** — and **every tool is a plugin behind the sovereign plugin port**. The Phase 0–2 actuators and
connectors listed above were originally _in-tree_; they have all been re-centered out. This is a re-architecture
of existing capabilities, not new phase work — the deliverables above still stand, they now live behind the port.

| Slice                                                                                                            | State                       | Evidence                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| ---------------------------------------------------------------------------------------------------------------- | --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Sovereign plugin port + content-blind spine (the thesis)                                                         | ✅                          | [ADR-0046](adr/0046-stratt-as-substrate.md); `sdk/stratt/plugin/v1`, `pluginhost`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| Port v1 full surface (write-back, relations, rung ladder, plan pinning)                                          | ✅                          | [ADR-0047](adr/0047-plugin-port-v1-full-surface.md); `pluginhost.ApplyRaw`/`PlanStep`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| Integration taxonomy (connector-plugin vs migration-tool vs core-transport)                                      | ✅                          | [ADR-0048](adr/0048-integration-taxonomy-plugin-tool-transport.md); AWX importer relocated, façade kept                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| Sites over the port (agent = authenticated relay, governance stays hub-side)                                     | ✅                          | [ADR-0049](adr/0049-sites-over-the-plugin-port.md); `sitegw`, `siteproto` typed stream                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| Certificate lifecycle as a reconcile Actuator                                                                    | ✅                          | [ADR-0050](adr/0050-certificate-reconcile-actuator.md); `plugins/certissuer`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| Ansible EE-Job subprocess transport (GPL boundary in the EE image)                                               | ✅                          | [ADR-0051](adr/0051-ee-job-speaks-the-port.md); `plugins/ansible`, one `govern`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| SecretBroker port (per-call resolution; core holds no material, §2.5)                                            | ✅                          | [ADR-0052](adr/0052-secretbroker-port.md); `sdk/secretbroker`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| MCP as a generic transport (the last domain logic leaves the core)                                               | ✅                          | [ADR-0053](adr/0053-mcp-transport-generic-connector.md); `plugins/mcp` EE-Job shim                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| Per-Step facet write-scope (least-authority write-back at the one governor)                                      | ✅                          | [ADR-0054](adr/0054-per-step-facet-claim.md); `pluginhost.govern` grant∩scope                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| Tiered genesis bootstrap — Stratt self-deploys its own services/plugins (dogfood)                                | ✅                          | [ADR-0102](adr/0102-tiered-genesis-bootstrap.md); `dev:genesis`; helm/deploy self-deploy of the real OpenFGA server + backend promotion, **proven live in kind**                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| Runtime Connector/Actuator registry — enable/disable plugins with **no restart**, reconciled from CaC            | ✅                          | [ADR-0103](adr/0103-runtime-connector-registry.md); `connectorregistry`; helm (Actuator) + declared (Connector) enabled+disabled at runtime, strattd `restarts=0`, **proven live fully in-kind (no compose)**                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| Capability dependencies — plugins `requires` a capability _contract_, resolved first-class (the anti-Jenkins)    | 🟡 framework + verification | [ADR-0104](adr/0104-plugin-capability-dependencies.md); `types.ValidCapability` + `provides`/`requires` on both Kinds; registry resolves declared providers (store-visible, replica-consistent, health-independent), gates enablement, surfaces unmet/ambiguous as D6 **pending** (§1.8). **Slice 2:** leader-only provider verification (`graph.capability_provider`) — a phantom `provides` its Manifest doesn't back is verified=false and never counts (§1.5). ≥2-provider estate binding + the four enterprise providers (Temporal-spine, OpenBao, S3, EC2) are the follow-ons                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| S3 as a provider-agnostic `statestore` provider — **code-complete end-to-end** (the first real capability chain) | 🟡 code-complete            | [ADR-0105](adr/0105-s3-capability-provider-agnostic.md); provider-agnostic class Contract (`capabilities/statestore.*`); awss3 advertises+resolves `statestore` via `awss3/statestore-resolve`; `s3-statestore` provider declaration; `ApplyRequest`/`PlanRequest` field `resolved_capabilities` (legible, §2.5-safe); orchestration resolves the bound provider + invokes its resolve Action + validates the class Contract + injects at dispatch (Plan, Apply **and now Invoke/Destroy** — [ADR-0145](adr/0145-the-actuator-builder-step-form.md) D2, which found the Action seam had no `resolved_capabilities` at all, so `requires:` was a promise only half a declaration's dispatch surface kept); OpenTofu renders `-backend-config` from the handle. S3 is provider #1 — Artifactory/GCS drop in behind the same contract. **Live opt-in proof CLOSED (2026-07-28):** `task dev:tofu:proof` runs a real `tofu` build against a real S3 state bucket (seaweedfs) and asserts the state object landed, so a retry converges instead of building a second VPC |
| OpenBao as a multi-capability provider — the enablement-gate exemplar                                            | 🟡 provider verified        | [ADR-0106](adr/0106-openbao-multi-capability-provider.md); the reusable **enablement-gate vs resolve-inject** distinction. OpenBao advertises `keycustodian` + `certissuer` (guarded on the PKI mount; `secretbroker` excluded — SDK-side, §2.5); `openbao` provider declaration, no resolve Action (enablement-gate); keycustodian core-use stays on `portCustodian` (ADR-0104 D7). D1 guardrail: an enablement-gate's reach-path must be the CLASS contract, never a named provider's mechanism. First `requires:` consumer (a cert Blueprint Step) booked                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| EC2 as the `provisioning` provider — the last enterprise add                                                     | 🟡 provider verified        | [ADR-0107](adr/0107-ec2-provisioning-provider.md); awsec2 advertises `provisioning` (enablement-gate, no resolve Action). Reconciles with the already-shipped ADR-0058 `builder:` reach-path (provider-coupled — a named §1.5 gap the class-contract refactor closes). Non-goal boundary: machine-_coordinate_ provisioning, never OS imaging/PXE (bare-metal provisioner = a plugin Stratt drives). EC2 provider #1 — GCE/KubeVirt siblings. **All four enterprise adds now landed as capability participants**                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |

**Verified in-repo (structural):** `core/internal/connectors/` is empty; `internal/actuators`,
`internal/actions`, `internal/emitters` hold only the seam interfaces, no tool logic; **21 plugin packages**
live in `plugins/` (ansible, ansible-automation, awsec2, awss3, chef, crossplane, declared, helm,
kubecontainers, kubeservices, mcp, mesh, msgraph, netbox, notify, openbao, opentofu, puppet, salt, script,
vcenter), each self-contained with its own `go.mod` + `cmd` + `Dockerfile`; the execution path routes by registry
lookup, not a tool-name switch, with no platform-default actuator
(ADR-0046). The residual tool-name strings in core are legitimate — opaque routing-key registration in the
composition root (`cmd/strattd`), the AWX `/api/v2` compat façade, and the AWX one-shot migration tool.

**Live proof landed (ADR-0102/0103).** The arc is no longer only unit/integration-proven: it now has a **first
live end-to-end run on a real in-cluster NATS+K8s+Temporal** (kind, in-cluster substrate, **zero compose**) —
Stratt self-deploys the real OpenFGA server through its own gated helm/deploy loop (ADR-0102), and the runtime
registry enables/disables a Connector (declared) and an Actuator (helm) at runtime from CaC with the strattd
pod at `restarts=0` (ADR-0103). This is the foundational threshold: the spine + orchestration + sovereign port

- reconcile, proven together against a live cluster. Broader live coverage is still ahead — fleet scale, the
  remaining 17 boot-env plugins migrated onto the registry, and targets beyond kind (KubeVirt / bare-metal /
  Cobbler / richer Ansible connectors). The exit gates are unchanged: none of this moves a promote/OSS gate,
  which still waits on §7.4.

## Ahead of the roadmap: multi-region Cells

The **[ADR-0044](adr/0044-control-plane-cells.md) Cells workstream (slices 1–7, complete)** delivers
multi-region active/active with fenced Source re-home — a capability the roadmap places at Phase 4 and
beyond. [ADR-0040](adr/0040-high-availability-and-disaster-recovery.md) explicitly _deferred_ cells, and
the 99.99% multi-region target sits _above_ Phase-3's 99.9% single-region SLO
([evidence map](evidence/multi-region-99_99.md)). The cross-Cell mechanisms (federated read, partial-result
honesty, fenced re-home over real HTTP) are **demonstrated end-to-end** by the two-Cell harness
(`task e2e:cells`) against live Postgres — the measured SLO still needs a real fleet. Follow-up
[ADR-0045](adr/0045-db-driven-syncer-home-gate.md) (full re-home auto-cutover) is Proposed, not scheduled.

## Enterprise-readiness (the cracks)

Capability is not the same as credibility as the dark-matter substrate. The gaps between what the charter/ADRs
**claim** and what the shipped artifact **enforces** — the cracks an enterprise reviewer would point at — are
tracked, evidence-backed and status-flagged, in **[enterprise-readiness.md](enterprise-readiness.md)**. Seeded
2026-07-18 by three grounded code audits. The core is enterprise-grade; the enforcement wiring and operational
envelope (obligation enforcement, admission on the API, EE-Job sandboxing, NetworkPolicy, supply-chain
signing, observability, backup/DR) are the unbuilt half — with **live-cluster e2e now partly delivered** by
the three live-verified demos (see the demo-library section above). Maintain that tracker as cracks are
found and closed. **§7.4 OSPO/IP clearance is CLEARED** — the repo may go public; each phase's promote/OSS
exit gate still requires its own operational evidence (SLO, security review, adoption).

## The demo library — and the live-cluster e2e it delivered

**[demos/](../demos/README.md)** ([ADR-0116](adr/0116-demo-library.md)) is a growing library of
self-contained, narrated, **turnkey** scenarios that teach Stratt by running it. Five ship, each
**live-verified end to end on kind** (build-up → gated Workflow → asserted real outcome → teardown):

| Demo                                                                          | Substrate            | Fidelity     | Proven live                                                                                                                                                                                                                                                            |
| ----------------------------------------------------------------------------- | -------------------- | ------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [k8s: deploy an app](../plugins/helm/demo/README.md)                          | Kubernetes (kind)    | `real`       | gated `helm/deploy` → a real Deployment 1/1 Ready serving its page                                                                                                                                                                                                     |
| [vSphere: provision a VM + the live graph](../plugins/vcenter/demo/README.md) | vSphere (vspheresim) | `build-real` | Syncer projects the topology; gated `vcenter/create-vm` → the built VM observed back, and its guest boots and reports a coordinate                                                                                                                                     |
| [EC2: provision a real instance](../plugins/awsec2/demo/README.md)            | EC2 (floci)          | `build-real` | gated `awsec2/create-vm` → a real floci instance container running, observed into the graph (0→1). **Re-graded from `real` 2026-07-27**: floci's network model is fully real, but no AMI ships sshd and user-data never runs, so there is no guest to converge (HAR-1) |
| [app install with a certificate](../demos/app-cert/README.md)                 | SSH (Linux host)     | `real`       | gated ansible converge: SSH as an unprivileged user → privilege escalation → a `community.crypto` X.509 cert → TLS read back off the wire, `app.config` projected with Run provenance, and a no-op Run refused                                                         |
| [region-to-cert — the capstone](../demos/region-to-cert/README.md)            | Kubernetes + EC2 (floci) | `build-real` | **the whole chain, from an estate naming no substrate.** Two gated `Intent/Subnet` builds through real `tofu apply` → `10.30.0.0/24` + `10.30.1.0/24`, distinct ranges NetBox allocated and no declaration contains; a gated `Intent/Compute` build → a pod + Service, `mgmt.address` the provider CAUSED; `apache-configure` → HTTP served off the wire, `app.config.port=8080 writerKind=run`; `cert-issue` → key `0600` **born on target**, `issuer=Stratt Dev Root CA`, subject derived from the host's own address; all four Findings RESOLVED. Graded at the **floor** of its two legs — the kubernetes leg is `real` alone |

**This is the first real dent in the "live-cluster e2e" gap** named in the enterprise-readiness section
below: the platform is now proven not only structurally and by unit/integration tests, but by
reproducible, asserting runs against a real cluster + real/simulated substrates. Each runner is CI-able
and **non-rotting** — a demo that stops working fails its own runner.

**Demos behaved as an integration-test instrument, every single time.** Landing the first three surfaced
(and fixed) six real defects no unit test caught; the capstone added four more of its own (see "Booked by
the capstone" below) plus two in the harness — `dev:connector-e2e` could not stand up on a genuinely cold
kind cluster at all, and demo staging vs estate staging were two implementations of one job differing in
whether they carried declarations. The original six: the desired-state wire API couldn't carry a targetless `action` Step or a
`gateOnly` CredentialRef (§1.6 asymmetry vs the Git door); the **genesis floor declared no helm
Actuator**, so the shipped self-deploy dogfood could never register `helm/deploy`; a failed plugin
Action's real cause was masked and no Run surfaced any error (§1.8 — see DESC-4 in
[enterprise-readiness.md](enterprise-readiness.md)); `plugins/{vcenter,awsec2}/go.sum` were incomplete
for their standalone image builds; and floci's healthcheck probed with a `wget` its image lacks
(false-unhealthy, also breaking `dev:stack:up`). Budget demo work accordingly.

**Next, in order:**

1. **A fully-featured Ansible plugin.** Ansible ships today only as an **EE-Job subprocess shim**
   (ADR-0051, `plugins/ansible` — ~800 lines incl. tests); the app-install demo needs the real thing.
   ⚠️ **Design it against PLG-1** ([enterprise-readiness.md](enterprise-readiness.md)): every substrate a
   plugin talks to in dev is one _we_ run and own (vspheresim, floci, the in-cluster `managed-web` sshd node);
   in production these are **external, operator-owned systems** — a customer's AAP/AWX, Galaxy/Automation
   Hub, credential vault, and a fleet behind bastions. Do not bake dev's reachability/ownership
   conveniences into the plugin contract. This already bit us: the ec2-only demo's SSH-converge act is
   deferred precisely because floci's instances are reachable only because we host them.
2. **An app-install demo** — install an app that requires a **TLS certificate** (a web server), so
   certificate issuance/renewal is taught alongside install.
3. ~~**The enterprise-estate capstone** (multi-substrate, one Intent), which additionally needs per-instance
   fan-out (ADR-0058), a K8s `Compute` provider, and multi-substrate simultaneous reconcile.~~
   **SHIPPED as [demos/region-to-cert](../demos/region-to-cert/README.md)** — all three prerequisites
   landed, and the demo drives declare → gated network build → gated host build → Apache converge →
   CA-signed certificate on one floor, asserting each leg. **It is narrower than this entry asked
   for, in one specific way that is worth reading rather than glossing:** "multi-substrate, one
   Intent" turned out to be unbuildable honestly, because `Intent/Subnet` has no Kubernetes
   implementation that is not an invention, and the aws substrate has networks but no bootable
   machines. So the capstone is **two proofs in one estate** — the network leg on aws, build →
   converge → certificate on kubernetes — with the placement resolver correctly refusing to join
   them. The demo's README argues that at length instead of quietly delivering less.

   **What running it found, which is the point of running it:** `task dev:connector-e2e` could not
   stand up on a genuinely cold kind cluster at all (its backends ran before the `helm install` that
   creates their namespace, and `stratt-hosts` was created un-adoptable so the first install
   collided with it) — both now fixed and verified from a destroyed cluster; the demo staging and
   the estate staging were **two implementations** of "vendor an admitted plugin" differing in
   whether they carried declarations, now one; a plugin's estate is not admittable standalone,
   because the ansible plugin's Triggers and Workflows name environments and Views belonging to the
   **reference** estate; the estate loader **silently reads only the first document** of a
   multi-document YAML file; and a composed Workflow's `facetWriteScope` can name a Facet namespace
   with no registered owner, failing at the far end of an approved gate — statically checkable, and
   the third instance of the class `provisions` (ADR-0145) and `remediates` (CERT-1) already closed.

## Dev follow-ups / test hygiene

- ~~**Two timing-sensitive tests flake under concurrent `task ci` load**~~ — **fixed**, and the
  standing note that "neither is a correctness bug" was **half wrong**, which is the finding worth
  keeping.
  - `core/internal/triggers/reconcile_test.go` **was** the assumed shape: a fixed `20 × 500ms`
    window waiting on Temporal's visibility store, which gives up after ~10s of wall clock however
    loaded the host is. Now bounded by a **deadline** instead of an iteration count.
  - `core/internal/siterelay` **was not a test-timing problem at all.** There was no `time.Sleep`
    to remove. `NATSAcceptor.Accept` subscribed **lazily**, inside the goroutine running `Serve`,
    so a call published before that goroutine was scheduled hit a subject with **no subscriber** —
    and core NATS drops it silently, leaving the hub blocked until its per-call context expired
    with no message naming the cause (§1.8). The test raced that window; **`stratt-agent` shipped
    it too**, logging `plugin-port relay serving` before the subscription existed. Fixed at the
    source: `NATSAcceptor.Subscribe()` establishes the interest eagerly **and flushes** (the flush
    is the load-bearing half — `SubscribeSync` returning is not the server having registered), the
    agent calls it before it claims to be serving, and the test calls it before it publishes.
    Verified `-race -count=30` clean.
    **Honest bound on the claim:** the production defect is certain from the code path; that this
    exact window caused every observed CI flake is inferred, not reproduced.

### Booked by the capstone (2026-07-31) — found by building and running `demos/region-to-cert`

Four findings that are real, are not demo bugs, and were each deliberately **not** fixed inside a demo:

1. ~~**A plugin's estate is not admittable standalone.**~~ **FIXED (2026-07-31).**
   `plugins/ansible/estate` shipped Triggers scoped `environments: [prod]` and four Workflows naming
   `viewName: secure-hosts` / `web-hosts` — an environment and two Views belonging to the
   **reference** estate — so any other estate admitting that plugin had to mirror all four names or
   fail to load. The `facetWriteScope` check below then found a third face of the same coupling: the
   two collectors write `access.grants` and `fileset.content`, namespaces only the reference estate
   owns. ADR-0137 D1 says a plugin owns its Actuator, Workflows, Triggers and content; it does not
   license a plugin to presume its adopter's scopes. All six declarations are the reference estate's
   own compositions and now live in `estate/{workflows,triggers}/`, with a single consumer each
   (`estate/blueprints/{access,fileset}.yaml`) and no `remediates` map advertising them. The plugin
   keeps what is genuinely its own — Actuators, content, and the converge recipes that name no View.
   The capstone's three placeholder declarations were deleted with it.

2. **A cross-plugin composed Workflow has no shippable home.** `cert-issue` composes ansible and
   openbao, so `task plugins:boundary` correctly refuses to let it live in either plugin — and there
   is nowhere else for it to travel. Every adopting estate therefore hand-copies it: the reference
   estate has one, the capstone now has a second, and nothing makes them agree. That is the
   divergent-second-copy shape this repo keeps paying for, arriving through a door the boundary rule
   itself opened. The fix is a home for compositions — a pack an estate installs (ADR-0033's
   materialize-into-operator-Git move) rather than a file every adopter retypes.
3. ~~**`facetWriteScope` can name a namespace with no registered owner, and the Run dies after the
   gate.**~~ **FIXED (2026-07-31) — `checkFacetWriteScopeOwners`, and the class is now closed three
   for three.** ADR-0145 added the check for `provisions`, CERT-1 for `remediates`, this one for
   `facetWriteScope`. It mirrors the runtime's three ownership sources rather than inventing a
   fourth: a Blueprint route **that remediates** (a pure observation never seizes write-ownership,
   matching `compiler.resolveOwnership`), a **dialled** provider's `facetNamespaces`, and the
   namespaces core registers at boot — the last now read from `types` by both the daemon and the
   check, so the two cannot drift about what is owned.
   **The discriminator that makes it useful:** an EE-Job Actuator's `facetNamespaces` is a write
   CEILING, not a claim. `ansible-certificate` declares `[fileset.content, cert.presented]` and owns
   neither, so a naive "any facetNamespaces" version would have passed the very estate that failed
   live. `address` vs `image` is the whole test, and `TestEEJobActuatorCeilingIsNotOwnership` pins it.
   Deliberately permissive at the edges (a route counts whether or not an Assignment currently binds
   it): a false negative costs a diagnosis, a false positive would refuse an estate that works.

4. ~~**The estate loader silently reads only the FIRST document of a multi-document YAML file.**~~
   **FIXED (2026-07-31)** — `refuseMultiDocument`, applied in `parseKind` (one call site, so every
   kind present and future is covered) and explicitly to `plugins.yaml`, which is parsed outside it
   and is the worst place to lose a document: a dropped admission loses every declaration that
   plugin ships and the estate still loads. **Refused rather than supported**, deliberately —
   supporting it means every parser returning one name for the duplicate map, `contentDir` resolving
   against a root derived from the file path, and diagnostics naming a file+index; all new surface
   for an affordance no shipped estate uses. A trailing `---` stays legal.

### Booked next — the port has no obligation to report an ignored input

**Where it comes from.** ADR-0151 D4's honesty rests entirely on a convention: `kubecompute` emits
`provider params ami,instanceType,region ignored` because its author chose to. **Nothing in the port
requires it**, so a provider that drops params silently yields a green build of a wrong-shaped host
after a human approved the gate. That is the same class this session closed three times over — an
advertised or supplied thing that quietly resolves to nothing — sitting one level lower, at the
sovereign port, where it applies to every plugin in every language.

**The design, worked out and recorded so the next step is unambiguous.** The obvious shape is an
`ignored_params` field the provider fills in, and it is the wrong one: a provider that ignores
silently simply returns an empty list, so the field rewards exactly the behaviour it exists to
expose. **Invert it.** The provider declares `consumed_params` — what it actually READ — and the core
computes `sent − consumed`. A provider that says nothing then has ALL of its params reported as
ignored, which makes silence the loudest possible outcome instead of the quietest. It also matches
the rule the rest of the platform already runs on: a plugin advertises only what it can back, and an
unbacked claim is refused rather than assumed.

**Layering, because it is not all in one place.** `params` is opaque by charter (§1.5), so the core
cannot detect ignoring by inspection — only by comparison against what it sent. The port carries the
fact (`InvokeResult.consumed_params`, additive), `pluginhost` passes it through governed alongside
`Rejections` (its exact mirror image: a rejection is something the plugin sent that core refused, an
ignored param is something core sent that the plugin refused), and the COMPARISON belongs in
`orchestrate`, which built the launch params and knows their shape — putting it in `pluginhost` would
leak the provisioning launch interface into the generic host.

**Not started rather than half-started.** A proto field nothing reads is precisely the
advertised-target-nothing-resolves defect, so this lands as one unit — proto + regen + host + the
comparison + a Run event carrying it (§1.8 descent) + `sdk/mockstratt` conformance + `kubecompute`
declaring what it consumes — or not at all.

> **Note on scope.** The phase tables above cite ADRs through ~0054 (the phase + dark-matter work). Later
> ADRs (0055–0091) extend the platform beyond the original phase plan — observability/OTel, API admission
> PEPs, in-place adopt + standing cutover reconciler, new estate dimensions (identity/software/service), and
> the **greenfield UI rebuild** ([ADR-0090](adr/0090-ui-rebuild-greenfield-charter-stack.md)/[0091](adr/0091-ui-is-a-first-party-bundled-pure-api-client.md): the UI is now a first-party, pure `/api/v1` client). Treat
> the tables as the phase spine, not an exhaustive log of recent work — the [ADR index](adr/README.md) is that.

## Where we are, in one line

Phases 0–2 code-complete; Phase 3 code ~90% (Jamf + ConfigMgr Connectors deferred by choice); multi-region
Cells shipped ahead of schedule; the UI has been rebuilt greenfield as a pure API client (ADR-0090/0091); and
the whole platform has been **re-centered onto the sovereign plugin port (dark-matter, ADR-0046 arc)** — the
core spine is content-blind and every tool is a plugin, now proven not only structurally + by unit/integration
tests but by a **first live in-cluster e2e** (ADR-0102 self-deploy + ADR-0103 no-restart connector lifecycle,
fully in-kind, no compose); broader live coverage (fleet scale, all plugins, non-kind targets) is the road
ahead. That arc has since been **finished at the boundary** (ADR-0137→0141): a plugin is a service rather
than a subdirectory — it owns its declarations AND its self contracts, pins them by digest so schema drift
between plugin and core is blocking (port invariant #5), and reaches core through a Go SDK covering BOTH
transports; capability routing is declared rather than derived at every layer (Step, Action, Actuator,
nested Workflow); and NO Actuator is registered in Go any more. **No phase's promote/OSS exit gate is met** — every one
ultimately waits on the charter §7.4 going-public step (OSPO/IP clearance) plus real operational evidence
(SLO, security review, adoption), none of which is a coding task.
