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

| Demo                                                                          | Substrate                | Fidelity     | Proven live                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| ----------------------------------------------------------------------------- | ------------------------ | ------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [k8s: deploy an app](../plugins/helm/demo/README.md)                          | Kubernetes (kind)        | `real`       | gated `helm/deploy` → a real Deployment 1/1 Ready serving its page                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| [vSphere: provision a VM + the live graph](../plugins/vcenter/demo/README.md) | vSphere (vspheresim)     | `build-real` | Syncer projects the topology; gated `vcenter/create-vm` → the built VM observed back, and its guest boots and reports a coordinate                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| [EC2: provision a real instance](../plugins/awsec2/demo/README.md)            | EC2 (floci)              | `build-real` | gated `awsec2/create-vm` → a real floci instance container running, observed into the graph (0→1) — **asserted since 2026-08-01, and unasserted before it**: the runner polled `ec2-instances` while the estate declares `provisioned-instances`, so the check read a missing View as an empty one and its failure branch printed prose instead of exiting. The Syncer (`STRATT_AWS_INTERVAL`, values-demo-ec2.yaml) always worked. **Re-graded from `real` 2026-07-27**: floci's network model is fully real, but no AMI ships sshd and user-data never runs, so there is no guest to converge (HAR-1)                                                                                                                                                                                                                                                                                                                                                            |
| [app install with a certificate](../demos/app-cert/README.md)                 | SSH (Linux host)         | `real`       | gated ansible converge: SSH as an unprivileged user → privilege escalation → a `community.crypto` X.509 cert → TLS read back off the wire, `app.config` projected with Run provenance, and a no-op Run refused                                                                                                                                                                                                                                                                                                                                                                                                                    |
| [scale a fleet: change a 1 to a 3](../demos/scale-fleet/README.md)             | Kubernetes (kind)        | `real`       | **cardinality, and the asymmetry it found.** `count: 1 → 3` offers EXACTLY two builds (web-01 is not re-offered); approve and the SAME Assignment, unedited, converges the new hosts over `kubectl exec` — exactly TWO drift Findings open, and all three end up carrying an `app.config` the Run WROTE. `count: 3 → 1` offers NOTHING: this demo found that `kubecompute` advertises `provisions` and no `decommissions`, so count-down is not symmetric on this substrate. **Added to this table 2026-08-01 — it had been missing since the demo shipped, and until that day its converge was narrated rather than run** |
| [region-to-cert — the capstone](../demos/region-to-cert/README.md)            | Kubernetes + EC2 (floci) | `build-real` | **the whole chain, from an estate naming no substrate.** Two gated `Intent/Subnet` builds through real `tofu apply` → `10.30.0.0/24` + `10.30.1.0/24`, distinct ranges NetBox allocated and no declaration contains; a gated `Intent/Compute` build → a pod + Service, `mgmt.address` the provider CAUSED; `apache-configure` → HTTP served off the wire, `app.config.port=8080 writerKind=run`; `cert-issue` → key `0600` **born on target**, `issuer=Stratt Dev Root CA`, subject derived from the host's own address; all four Findings RESOLVED. Graded at the **floor** of its two legs — the kubernetes leg is `real` alone |

**This is the first real dent in the "live-cluster e2e" gap** named in the enterprise-readiness section
below: the platform is now proven not only structurally and by unit/integration tests, but by
reproducible, asserting runs against a real cluster + real/simulated substrates.

**They were NOT non-rotting, and claiming so here was circular** (corrected 2026-08-01; it read
"Each runner is CI-able and **non-rotting** — a demo that stops working fails its own runner"). A
runner only fails when something runs it. **`task e2e:live` is now the something**, wired to **weekly** (Mon) /
`v*` tags / dispatch in `.github/workflows/e2e-live.yml`, with the suite derived from the Taskfile's
own demo targets so a new demo is gated because it exists.

Its first run found **three** real defects in demos this very table called live-verified — a
dispatch-table race in `vsphere-only`, a launch that had been HTTP 400ing in `ec2-only`, and an
`ec2-only` "observe" step that cannot observe because the demo declares no Syncer. All six pass now
(`e2e:live` EXIT=0, ~26 min). **E2E-1 stays 🟡 until the workflow has actually executed in CI** —
automation nobody has run is the same claim-nobody-checked this whole section is about.

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

- **Demo runners are load-bearing integration tests with no shell lint gate.** There is no
  `shellcheck` or `shfmt` anywhere in the Taskfile, yet `demos/*/run.sh` is what actually asserts
  the platform works end to end — six defects across the demo library were caught by these scripts
  and by nothing else. Two hazards were measured while writing a single ~15-line helper on
  2026-08-01: a local named `status` (a **read-only special variable in zsh**, so the by-hand walk
  the READMEs invite breaks for any zsh reader), and `jq -r '.x // "default"'` **not** applying its
  default on an EMPTY document — jq emits nothing and exits 0, so a failure message rendered a
  blank where its diagnosis should have been. Both are shellcheck/inspection-class. The gate is
  cheap; the argument for it is that a runner that fails for the wrong reason is worse than no
  runner, which is this branch's recurring finding.

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

### ~~Booked next~~ SHIPPED — the port now reports an ignored input (2026-07-31)

ADR-0151 D4's honesty used to rest on a convention: `kubecompute` emitted `provider params … ignored`
because its author chose to, and **nothing in the port required it**, so a provider that dropped
params silently produced a green build of a wrong-shaped host after a human approved the gate. That
is the same class this session closed three times over, sitting one level lower — at the sovereign
port, where it applies to every plugin in every language.

`InvokeResult.consumed_params` (additive) now carries the fact, and the core computes `sent −
consumed` and puts the difference on the Run as a `params-ignored` event.

**The field declares what was CONSUMED, and that inversion is the whole design.** The intuitive shape
is `ignored_params`, filled in by the provider — and it rewards exactly the behaviour it exists to
expose, because a provider that drops params silently returns an empty list and looks perfect.
Reported the other way round, a provider that says nothing has **everything** it was sent reported:
silence becomes the loudest outcome rather than the quietest, and the honesty requires no goodwill.

**The core could not do this alone,** which is why the fact had to cross the port: `params` is opaque
by charter (§1.5), so there is nothing to inspect — only something to compare against what was sent.
The comparison lives in `orchestrate`, which built those params, rather than in `pluginhost`, which
crosses every class and must not learn one class's convention. Only the opaque `params` object is in
scope; the shared launch interface is not, because `placement` is accepted-and-ignored **by design**
on this substrate (ADR-0123 D2) and folding it in would report a designed no-op as a defect on every
build.

**No conformance test, deliberately** — there is nothing for one to protect. The default is already
the safe one. Scope today is `kubecompute-build`, the only Workflow forwarding `{{.launch.params}}`;
as other builders begin to, they either declare what they read or are correctly reported as reading
none.

### AWX-012 + AWX-017 — the account nobody offboards (ADR-0155, 2026-07-31)

**AWX-017 needed an identity-plane decision, and the join was the whole problem.** AWX knows a
USERNAME; the identity plane keys by `<idp>/<scimId>`. Every obvious bridge is wrong: letting the AWX
plugin write an identity fact is a §2.1 registration error (ADR-0130 D1 says so in those words);
having an operator configure "this Controller authenticates against that IdP" is a convention typed
into an env var, joined by string equality — **exactly the shape ADR-0154 had just spent its length
repairing**, and repeating a defect one ADR after fixing it is not a plan; and keying by bare
username is sound only if usernames are unique across every IdP, which nothing guarantees.

**The answer: the SCIM projector emits the key, and only when it is unambiguous.** A `user` Entity
gains `identity.userName` as a SECOND way to be ADDRESSED — claiming nothing about the person,
contesting no ownership, the same move that lets the AWX half point at `ansible.playbook`. And the
projector is the only component that CAN decide unambiguity: it enumerates every IdP in one pass, so
it alone sees that `jsmith` exists in two directories. When it does, **neither** entity gets the key.
That is the correct answer, not a gap.

Lowercased, measured rather than assumed: RFC 7643 §4.1.1 makes SCIM `userName` unique per provider
with `caseExact: false`, so normalising cannot merge two people and it makes the join survive a
Controller storing `JSmith` for an IdP's `jsmith`.

**The AWX half emits a soft `same-account-as` edge and its ABSENCE is the finding** — the
relation-presence mechanism of ADR-0085 as sharpened by ADR-0154. A dropped edge means either no IdP
knows this login (the account an offboarding process misses, because offboarding runs against the
IdP) or the name is ambiguous. Both deserve a Finding. `same-account-as` asserts a CORRESPONDENCE —
that two logins share a name, a fact about strings — never an identity, and
`TestINV3_AuthzConsultsNoGraph` keeps it structurally unable to reach an access decision.

**Verified two ways on purpose.** The unambiguity rule is a pure function with its own test, because
the graph package's tests are Postgres-gated and SKIP without a database — which is exactly how an
inert mechanism stays green here. The key actually LANDING and resolving is then asserted end to end
against a real store, which a pure-function test cannot show.

**AWX-012** rides along: `ansible.credentialtype`, where `managed: false` is the migration question —
a credential of a custom type has no equivalent until that type's fields and injectors exist on the
other side. Field names, which of them are secret, and the injector delivery modes; the injector
TEMPLATES are not projected, being arbitrary operator text the mode already summarises.

**AWX-005 stays declined, deliberately.** ADR-0130 D3 refused role grants on three grounds, and one
of them is not a cost problem a better read shape answers: a projected grant graph is one query from
being used as an authorization truth, and "INV-3 stops it" is an argument about mechanism rather than
about what people build on top of a convincing-looking permission graph. That decision stands, and
reopening it silently would make "we looked and said no" render the same as "nobody looked".

### AWX-001 · the Project, and the orphan signal it repairs (ADR-0154, 2026-07-31)

The last `adopt-only` 🔴 in the AWX object-model's projection column, and the audit's own Tier 1 — for
a reason that is not breadth. **ADR-0085's orphan-template Baseline was ambiguous by construction.**
It reads the presence of `ansible.template --runs--> ansible.playbook`, whose target is keyed by the
AWX Project's NAME concatenated with a playbook path and matched against the operator-set
`STRATT_ANSIBLE_CONTENT_ID`. So the edge rests on a convention someone types into an env var — and
when that convention is broken the edge drops, **byte-identically** to it dropping because the
content genuinely is not projected. One signal, two very different causes, no way to tell them apart.

`ansible.project` gives the template an ID-JOINED companion, `uses-project`, on the identifier AWX
issued rather than a name a human aligned. `uses-project` present with `runs` dropped now says "the
content root is the missing half, and here is its scm_url and revision"; `uses-project` absent is a
different diagnosis entirely. The `runs` edge is deliberately NOT re-keyed — the content half
identifies playbooks by project id and relative path and knows nothing of AWX ids, so translating
would have Stratt assert a correspondence neither system states (§1.2). The name join was never
wrong; it lacked a companion.

**`scm_revision` binds catalogue to execution** — the only fact in the mirror that says which BYTES
the Controller is running. It is projected and **compared to nothing**: the content half reads a
filesystem and projects no revision, so there is no second value to diff, and claiming a drift check
that cannot be computed is the plausible-wrong-answer this repo keeps refusing. The comparison is
booked, not implied.

**§2.5, fifth application, and this one has a new shape.** AWX stores repository credentials as a
separate object — but a real estate routinely embeds a PAT directly in the clone URL, because it
works and nobody stopped them. Dropping `scm_url` would lose _which repository_, the fact most
needed, to guard a minority case; projecting it verbatim would import live tokens. So the userinfo is
removed and `scmUrlRedacted` says so — and the boolean matters as much as the redaction, because
silently stripping would leave a reader unable to tell a clean URL from a scrubbed one, and "this
repository is cloned with an embedded credential" is its own finding. A bare username is NOT flagged
(the flag must mean a credential was present, not that there was an `@`), and a value that cannot be
parsed but contains an `@` is withheld entirely — a value that cannot be proven safe is not one to
project. The simulator seeds a PAT-bearing URL so a verbatim projection has something to fail on.

Poll cost 10 → 11 collections, moved deliberately; project SYNC JOBS are not read, because that is
run history (§3) and `status` + `lastUpdated` already carry current state. The disjoint-namespace
guard caught the manifest contract again, which is now twice in three commits that it has earned its
literal counts.

### ANS Tier 3 — and the config observation that turned out to be a bug fix (2026-07-31)

`ansible.cfg` (ANS-005), the repo's own modules and plugins (ANS-006), and the root that IS a
collection (ANS-007). With Tier 2 the day before, **the content-root audit is now green except
ANS-009** (multi-document playbooks, still unexamined) — the Ansible half of a migration is visible.

**ANS-005 was not an observation, it was a fix.** The audit says ansible.cfg "changes the meaning of
everything else in the root", and reading it proved the point immediately: `roles_path` moves where
roles live, and this Syncer's role reader looked in `roles/` unconditionally. A repo configured with
`roles_path = galaxy_roles` projected **zero roles and said nothing** — reporting on a layout the
tool was not using. The reader now searches `roles/` plus any relative, in-root `roles_path` entry.
Absolute and escaping entries are skipped, because this Syncer cannot read outside the content root
and must not pretend to have — but they still appear in the projected value, so the gap is visible
rather than silent (§1.8).

**The §2.5 rule needed a fourth application, and this time a denylist would have failed.**
`ansible.cfg` can hold a real credential: a `[galaxy_server.*]` section takes a Galaxy API token. So
the projected settings are a bounded ALLOWLIST of structurally-meaningful values, and every other key
contributes its NAME only — which keeps the token out **by construction** rather than by having
anticipated it.

**ANS-006 covers both layouts, deliberately.** A playbook repo puts custom code in `library/` and
`filter_plugins/`; a collection-shaped repo puts it in `plugins/modules/` and `plugins/filter/`.
Covering one would report half of a real repo as shipping no custom content — the same silent-gap
shape this batch exists to close. Classification is by DIRECTORY, because ansible loads plugins by
directory and that is the only fact knowable without reading the program.

**ANS-007 reuses the ANS-002 shape.** The root collection is an `ansible.collection` beside the
required ones, marked `root` — one question, one Kind. Its own `dependencies` live in galaxy.yml and
a projection reading only requirements.yml sees none of them.

**One ordering bug, caught by its test.** The root collection was appended to `snap.Collections`
_before_ the requirements read, which ASSIGNS that slice rather than appending — so it was silently
discarded. The comment now sits where the next field added there will read it.

### The Ansible content root stops being a list of files (2026-07-31) — ANS-002/003/004/008

Tier 2 of the tool audit ("the estate cannot see where configuration comes from") closed as one
batch, because it is one mechanism in one place: `group_vars`/`host_vars` scopes, role `meta/`
dependencies, and the `requirements.yml` roles half that was never parsed while its collections half
always was.

**`ansible.varscope` carries key NAMES and never values (§2.5)** — the third instance of that line
after credentials (ADR-0128 D2) and schedule `extraDataKeys` (ADR-0132 D3), and the one where it
bites hardest: a `group_vars` file routinely holds credentials in the clear, which is precisely why
people vault them. But scope alone does not answer the motivating question either — knowing
`group_vars/web.yml` exists says nothing about why a host got `http_port: 8080`. The names are the
answer and are not secret. **ANS-008 fell out of it**: a `$ANSIBLE_VAULT` file is present with
`vaulted: true` and NO keys, never decrypted, and an empty key list _with_ that flag distinguishes
"binds nothing" from "binds things I cannot show you" (§1.8).

**Precedence is observed, never computed.** Two scopes binding one name is ansible's normal case;
both project and neither is marked a winner. Computing the winner would reinterpret the execution
model (§9 — the line this audit says is correctly held) and would have Stratt assert a fact about a
run that has not happened (§1.2).

**Three defects found by writing the tests, all of the silent-wrong-answer kind:**

1. **An identity collision.** `roleID` used `"roles/" + name` for a required role — byte-identical to
   an in-tree role's path. An in-tree `apache` and a `requirements.yml` entry named `apache` produced
   ONE identity, so one silently overwrote the other: the same entity asserted twice with different
   facets and no error anywhere. The spaces are now `roles/<path>` and `requirements/<name>`.
2. **A dependency edge that always dangled.** `meta/main.yml` names a role, not a location, so the
   edge target cannot be computed from the name — the first version pointed everything into the
   requirements space, which left every dependency on an IN-TREE role pointing at nothing, forever.
   It now resolves against the observed role set first.
3. **A `requirements.yml` half that parsed to empty.** Decoding the whole `roles:` list into one
   struct shape fails on the first bare string — and real files MIX the bare and mapping forms.
   Measured against yaml.v3 rather than assumed. It is now per-entry, symmetric with the collections
   half beside it.

**And an existing guard caught a defect in the PREVIOUS commit.** `TestHalvesOwnDisjointNamespaces`
counts what each half advertises; AWX-009 had added `ansible.notification` to `TombstoneSchemes` and
to the operator grant but **not to `Contracts`** — registration tolerates that, so the projection was
writing a facet nothing schema-validated. "Own what you project" (§1.1) only holds if the manifest
points at the shipped schema. Fixed here.

### AWX-009 · where a Controller sends its outcomes, without importing the credentials (2026-07-31)

`notification_templates` → `ansible.notification`, the tenth collection the AAP mirror projects. It
answers the migration question "where does this Controller send job outcomes, by what driver, and
which have a hand-written message body I must re-author?" — each row is a Sink to declare on cutover,
and `notificationType` is its `kind`, which is only a straight mapping because ADR-0125 made a Sink's
kind name its delivery Action and left core holding no driver list.

**The §2.5 line is the whole design, and the obvious rule is the wrong one.** AWX returns
`$encrypted$` for `token`/`password` and returns everything else IN THE CLEAR — so "project what AWX
did not encrypt" reads safe and is not: for the commonest driver the cleartext field IS the
credential. A Slack or Teams incoming-webhook URL is a bearer secret with the token in its path.
`configKeys` therefore carries key NAMES only, and the discard happens in `UnmarshalJSON`, so the Go
type has **no field the values could live in** — structural, not a habit in the normalizer, and
nothing a well-meaning "just add the endpoint" edit can reach. Same line ADR-0128 D2 drew for
credentials and ADR-0132 D3 for schedule `extraDataKeys`, applied where it bites hardest. The
simulator seeds real secret shapes (a webhook URL with a token in the path, a bearer header) so a
leaking projection has something to fail on.

**A test that could not fail, caught.** The first version of the leak test marshalled the entities
and grepped for the secrets — but protobuf JSON base64-encodes facet bytes, so it matched nothing in
either direction. It went green while proving nothing. The fix decodes the facets before searching;
the note is in the test, because the shape recurs.

**Attachments are absent BY BUDGET and it is stated rather than silent.** Which template notifies
through which of these, on started/success/error, exists in AWX only as three sub-resources PER
TEMPLATE — 3×len(job_templates) requests per poll, on the largest collection in any real Controller.
That is the different-order-of-cost ADR-0131's budget exists to refuse; booked as a detail-tier
opt-in. The poll-cost test caught the new collection read immediately and its literal was bumped
deliberately (9 → 10), which is exactly what that constant is for.

### The reach gap closed for network devices — and a password that is only ever a path (ADR-0153)

`ansible-tool.md` called ANS-001 "the one finding that changes what Stratt can be sold as": the ansible
Actuator spoke **SSH with a private key and nothing else**, so a network fleet or a password-only estate
was not partially supported, it was unsupported — and no document said so. `ansible.input.v8` closes most
of it: `connection.type: network_cli | netconf` with a required `networkOS`, plus the three credential
forms the connection surface had no shape for (ANS-001's password half, ANS-010, ANS-011).

**The credential half was the design, and the mechanism turned out better than the sketch.** The gap
register assumed a password would have to become a value somewhere. It does not: ansible-core takes all
three secrets as FILE PATHS — `--connection-password-file`, `--become-password-file`,
`--vault-password-file` / `--vault-id id@path` — **verified against the interpreter the EE pins, not
assumed from documentation**. So a password is never a value in the inventory, in extraVars, or in argv.
The shape everybody writes first, `ansible_password` as an inventory group var, is not a weaker option
but a forbidden one: `writeInventory` creates `inventory/hosts` at **0644** in the private data dir
**beside ansible-runner's `artifacts/`**, and §2.5 says material is never written to artifacts. Each
password resolves through the SAME `credentialFile` helper, gated by the SAME per-name use-grant check —
no new credential channel, no new authorization path.

**Three refusals, each because the alternative resolves itself silently.** `networkOS` is required for
the netcommon types because a guessed vendor CONNECTS and then issues another vendor's syntax. A non-ssh
`type` on a run containing a `local` target is refused, because `local` is a HOST var and host vars beat
group vars — implicit precedence hiding inside two declarations that each look right (§2.4). And a
duplicate vault `id` is refused, because ansible resolves that by ORDER, which is a silent winner by
another name.

**`winrm`/`psrp` are NOT in the enum, deliberately.** Windows is the most-asked-for row in the register,
and there is no freely-runnable Windows target in CI — so the value would ship as a code path nothing had
ever executed. An enum that ACCEPTS it fails at 3 a.m. on a fleet someone migrated; one that rejects it
fails at estate load with a message naming the gap. Same rule as ADR-0151 D3's unimplemented substrates
and the façade's unconvertible cron: **no answer beats a plausible wrong one.**

**AND THEN VERIFYING IT FOUND THE ADR HALF-APPLIED (D7, same day).** `network_cli` and `netconf` are
**not in ansible-core**. A Contract accepting the value on the default EE passes review, passes the
estate load, passes every unit test — and dies at connect time naming a python module the estate never
wrote, which is verbatim the `community.general.apk` failure `platform.requirements.yml` exists to
document. D1's argument had been applied to the Contract and not to the runtime.

The shim now refuses a netcommon type the image cannot honor, reading the EE's own run-visible content
manifest. **Not a probe:** `ansible-doc -t connection <name>` **exits 0 for a plugin that does not
exist** — measured — so the obvious check silently passes. Declaration errors are reported before image
errors, and an UNREADABLE manifest is a third outcome rather than a guess in either direction.
`ee/content/network.requirements.yml` is the other half, a VARIANT rather than a floor entry (the floor
is bounded to what the platform's own content needs, and no shipped content root speaks to a device),
carrying `ansible.netcommon` only — vendor collections are the adopter-shaped question ADR-0117 D3 puts
in a variant. **Verified against real images both ways:** the floor EE fails to load `network_cli` and
the shim refuses it; the variant resolves both plugins and the shim allows it.

**The honest limit is now narrower and still real: no DEVICE has been driven.** A collection that
installs is not a connection that works. That needs a CI-runnable target (FRR or cEOS) and is booked in
the same shape as PLG-1's bastion half — and the parity doc says so in place rather than letting an
image-verified row imply a proven one.

### The converge side stops naming substrates — and a pod with no sshd is converged (ADR-0156)

Asked whether estate-as-code truly spans vSphere, EC2 and Kubernetes — _change a count from 1 to 3
and get three more machines_ — the build half answered yes and the converge half did not. The reason
turned out to be an assumption nobody had checked: **"every substrate needs sshd and a network path
to port 22."**

Measuring the actual collections falsified it. `kubernetes.core.kubectl` needs **nothing in the
guest**. `community.vmware.vmware_tools` needs **no network path to the guest at all**, because every
operation travels the vCenter API. `amazon.aws.aws_ssm` goes through a Systems Manager session. The
harness gap that prompted the whole question — floci ships no sshd (HAR-1) — was never the blocker it
looked like: two of three substrates never needed one.

**So the transport is OBSERVED, not declared.** `mgmt.transport` sits beside `mgmt.address` — where
vs by-what-means, both projections of what the provider actually did — and the shim renders it as a
HOST var. One Assignment converges a pod, a VM and an EC2 instance in ONE Run, and the Intent, the
Blueprint and the Assignment name no substrate. That is the converge-side equivalent of what ADR-0151
did for builds, and it is why the Step was the wrong home: connection settings are group vars, one
value per Run, so a mixed-substrate View could not be converged at all.

**~~LIVE-PROVEN on kind~~ — RETRACTED 2026-08-01. This paragraph was the source of a false 🟢 that
propagated into `docs/parity/ansible-tool.md`, and correcting it is worth more than the claim was.**

It read: *"A pod asserted to have no sshd binary, no ssh client and nothing listening on port 22 —
then converged by the real EE image and the real shim over `kubectl`."* Three things are wrong with
that, all checkable:

1. **No such assertion exists in the repo**, then or now. Nothing greps for a missing sshd.
2. **No such pod exists.** `kubecompute` bakes `openssh` into every host it builds; sshd is
   measurably running in them (`ps` in a built pod, 2026-08-01).
3. **The platform path could not have run at all.** Execution pods are spawned with
   `AutomountServiceAccountToken: false`, and no kubeconfig was brokered anywhere — `pods/exec` was
   granted nowhere in the repo. A dispatched Run had nothing to authenticate to the API server with.
   `demos/region-to-cert` proved it by being the first thing to try: it failed `unreachable`.

What was almost certainly proven is the MECHANISM in isolation — ansible's kubectl connection
working from a hand-run pod, which is exactly the check I re-ran on 2026-08-01 and which passes
(`web-01 | SUCCESS => pong`). Recording that as the PLATFORM being proven is the same substitution
this arc keeps making: `ansible-doc`'s exit code, the base64 leak test, the `ParentClosePolicy` grep,
and `scale-fleet` asserting a Facet while narrating a converge. **A mechanism that works in a
scratch pod says nothing about whether dispatch can reach it.**

**Now genuinely live-proven** (ADR-0156 D4a): `task demo:region-to-cert:run` from a destroyed
cluster installs Apache on a kubecompute-built pod over `kubectl exec` with a brokered kubeconfig,
`ansible-runner rc=0`, HTTP served off the wire on :8080. The negative half holds too — the base EE
refuses the identical request, naming the missing collection.

**The coupling it was said to retire is NOT retired.** kubecompute still bakes sshd and
authorized_keys into every pod; the converge simply no longer uses them. Removing that bootstrap is
now unblocked and remains booked.

**A near-miss worth recording.** The first negative run failed with `unknown field "transport"`, which
looks like a refusal and is not: the image predated the proto change and its shim could not decode the
request at all. A stale binary's unrelated error would have been filed as the gate working. Rebuilt
from current source, it was real. Third time this session that a check which appeared to pass was
measuring nothing — after `ansible-doc`'s useless exit code and a leak test that grepped base64.

**What is NOT proven, stated rather than implied.** `vmware_tools` is shipped and unit-tested only:
vspheresim implements the vCenter API but not Tools guest operations. And **`aws_ssm` has no writer at
all** — the awsec2 Syncer can honestly observe neither EC2 path, because `KeyName` means a key is
AUTHORIZED rather than that sshd is listening (computing a reach fact, which ADR-0142 D4 forbids), and
SSM needs a different AWS API. Booked: the SSM client and the transport land together, since a Facet
has no other writer.

**The EE gate gained a second axis.** ADR-0153 D7 checked collections; `kubectl` and
`session-manager-plugin` are BINARIES the connection plugin execs, so a collection-only gate would
pass and fail at connect time. The kubectl binary is pinned by version AND sha256 — a version bounds
which release, only the checksum bounds which bytes — and the build fails if a version is given
without one.

### `/api/v2` route breadth is DONE (2026-07-31) — and two refusals are the point

`schedules`, `workflow_job_templates` + `workflow_jobs`, `projects`, and `credentials` +
`credential_types` all ship. The four families the parity audit named are complete; what remains on
that front is launch SEMANTICS (`ask_*_on_launch` beyond variables, AWX-015), which is a design
question about desired state rather than a missing endpoint.

**`projects` needed no design at all**, which is worth recording: ADR-0134 D2 already declares an
Actuator's `contentDir` to be "one project: playbooks, roles/, group_vars/", one Actuator per
project. The family is that mapping. The alternative — deriving projects from distinct SCM blocks in
Step params — would have been core reading a tool's params by name to invent an object, the §1.4 trap
that ADR spends a paragraph warning implementers about. `scm_type` is manual because nothing clones
at run time BY DESIGN, and `job_templates.project` is no longer null.

**`credentials` is where §2.5 is easiest to erode.** An AWX credential CONTAINS material; a
CredentialRef contains a POINTER, and no graph-store method returns material — not redacted, not
encrypted, none. `inputs` carries the declared injection KEY NAMES with AWX's `$encrypted$` sentinel:
it asserts "a secret stands here" (true), never "Stratt holds it" (false). The key names are
Git-declared and already served on /api/v1, so hiding them would hide diagnosis while protecting
nothing — but the LOCATOR is absent, because it is the address OF material and a compat listing is
not the place to widen who reads it. One synthetic `credential_type` for every ref: AWX's type says
what a credential is FOR, Stratt's `backend` says WHO BROKERS IT, and mapping one onto the other is a
category error that would read as fact.

**Two things were refused rather than half-shipped.** Attaching a credential (or an inventory) at
launch stays in `ignored_fields` — a Step's credentialRefs are declared and reviewed in Git
(ADR-0009), and a launch-time swap would make the compat surface the one door that skips that review.
And `POST /projects/{id}/update/` does not exist: an update means "clone the SCM again", nothing here
clones, and a no-op would tell an operator their content refreshed.

### Booked by the WFJT façade family (2026-07-31) — a cancel that would have lied

`/api/v2/workflow_job_templates/` + `/workflow_jobs/` shipped, and **14 of the 21 Workflows the
reference estate ships were invisible on the compat surface until they did**: `job_templates`
presents only single-Step, Gate-free Workflows, so every DAG, every gated Workflow and every
policy-checkpointed one had no AWX representation at all — the strangler-fig door failing at exactly
the shapes an adopter migrates FOR. The launch reuses `orchestrate.LaunchWorkflowRun`, **extracted**
from `api.Server.launchWorkflow` rather than copied; that function's own comment had warned for two
ADRs that a second launch path grows its own authz and its own drift, and this was the moment the
warning came due.

**What was deliberately NOT shipped: `workflow_jobs/{id}/cancel/`.** AWX offers it and it was two
lines here — but `RunDAG` has **no cancellation handling at all** (no `ctx.Done` path, no terminal
status write), and the native API has no workflow-run cancel door either. Wiring one only on the
compat surface would signal Temporal, tear the activities down, and leave `graph.workflow_run` saying
`running` **forever**: an operator who cancelled would be told the execution is still going, which is
strictly worse than not offering cancel. A 404 from the mux says "not offered" — absent rather than
wrong (§1.8). **The real gap is native:** a terminal-status writer in `RunDAG`, with the façade route
following it. Cancelling a Run (single-Step) already works and is unaffected.

### Charter review of this branch (2026-08-01) — performed as steward, subagents unavailable

CLAUDE.md requires `charter-guardian` before finalizing changes to Contracts, credentials or authz,
and `vocabulary-linter` before merging a new core-model identifier. Both were run **by hand against
the charter text**, not from memory. Reviewed: `ansible.input.v9` (`connection.kubeconfigRef`), the
`hosts-kubeconfig` CredentialRef and its authz tuples, ADR-0156 D4a, and the new
`e2e:*` / `dev:await-actuators` surface.

**Verdicts.** §1.1 type the seams — the new field attaches at a plugin-boundary Contract, no new
Facet, nothing typed onto a whole Entity. §1.4 boring spine — the SHIM authors every `ansible_*`
key; core sends typed transport coordinates and learns no ansible vocabulary. §1.5 sovereign
contracts — v9 is a pinned, hash-verified SIBLING of v8 (highest version wins), so a Step cannot pin
one and drift stays blocking. §1.6 one authz model — the use-grant is an OpenFGA tuple in Git, which
is what §2.5 means by "Platform RBAC is itself CaC". §2.1 — no Facet namespace gains a second writer.

**§2 vocabulary.** `kubeconfigRef` and `hosts-kubeconfig` are not banned terms and are not
core-model identifiers (a plugin Contract field and an estate CredentialRef name). The TaskEvent
`kind: "inventory"` I added needed a second look, since `inventory` IS banned — it is admissible for
the same reason `kind: "tofu"` and `kind: "kubectl"` already are: a plugin-emitted, tool-scoped label
naming ansible's own artifact, not a core-model identifier. The ban maps AWX *inventory* → **View**,
and nothing here renames a View.

**The one finding, and it was in something shipped hours earlier.** §2.5 says "material never
persists in the platform". The shim now EMITS the rendered inventory as a Run event — a second
distribution channel beside `inventory/hosts` — and that is safe only because every credential in it
is a PATH (ADR-0153 D3). That was an **assumption, not a checked property**. The existing guard,
`TestPasswordsAreFilePathsAndNeverValues`, inspects var NAMES for `pass` — a heuristic
`ansible_kubectl_kubeconfig` passes trivially while carrying anything at all. A name tells you
nothing about a value. Closed by `TestInventoryCarriesCredentialPathsAndNeverMaterial`: every
credential file returns unmistakable content, and the test asserts that content never appears in any
rendered var or in the inventory. Falsified by making the shim inline the kubeconfig — the guard
fires.

**Not settled here, deliberately.** ADR-0150 and 0153–0157 remain **Proposed**. Promoting an ADR is a
steward decision and reviewing my own design is not the same as an independent review — this pass
found one real defect in my own work, which is evidence for the value of the second pair of eyes, not
a substitute for it.

**Booked: the §2 freeze has no automated guard.** "Naming is API. Frozen at v1.0" is enforced by
review only — there is no test scanning core-model identifiers for the six banned terms, so the
freeze holds exactly as well as everyone's memory. A guard is cheap and belongs beside the other
repo-scanning checks in `task ci`.

### E2E-1 · the live gate exists, and its first run found three rotted demos (2026-08-01)

`task e2e:live` + `.github/workflows/e2e-live.yml` (weekly Mon / `v*` tags / dispatch, one runner per
demo). The suite is derived from the Taskfile's own `demo:<name>:run` targets, so joining the gate is
structural. All six demos pass, EXIT=0, ~26 min. Details in **E2E-1** under enterprise-readiness.

**Booked by it:**

- **`ec2-only` never measured its own observe half — and the first diagnosis of that was wrong,
  which is the part worth keeping.** The runner polled a View named `ec2-instances`; the estate
  declares `provisioned-instances`. Its `count()` maps the 404 to `0`, so the check reported "0 to
  start" (true by accident) and "0 at the end" (a missing View, not an empty one), and its failure
  branch printed prose instead of exiting — a soft pass on the closure the demo exists to prove.

  **The wrong turn:** I read `estate/` for a Source and a Connector, found neither, and concluded
  the demo had no Syncer and could not observe — then wrote that into the runner, the demo index and
  this file. The Syncer is real and enabled by `STRATT_AWS_INTERVAL: 15s` in
  `values-demo-ec2.yaml`; it is configured by HOST ENV rather than by an estate declaration, so
  looking only where declarations live produced a confident wrong answer about a working component.
  Fixed by running it: `provisioned-instances: 1`, observed, asserted. **Two facts with one name
  between them and no check that resolves it is the recurring shape** — the runner and the estate
  disagreed about a string, and nothing in CI could see it.

  Still worth doing: the Syncer's enablement lives in a values file while everything else about the
  demo lives in its estate, which is why the search missed it.
- **`actionlint` is not in the gate.** Both workflows were validated with it by hand
  (`go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7`, clean) and it caught nothing this
  time — but a workflow is now load-bearing infrastructure with no lint behind it, and the next
  editor gets no signal. Adding it to `task ci` is a new toolchain dependency and therefore owes the
  **dependency-scout** review the charter requires (§1.7), which is why it is booked rather than
  added.
- **Three demos have now independently hit the dispatch-table race.** `dev:await-actuators` is the
  shared answer, but `demo:region-to-cert` and `demo:ec2-only` still carry their own private wait
  loops inside their runners. Collapsing those onto the shared task removes two more second copies;
  low risk, not done here to keep this change reviewable.

### The kubectl transport had no way to reach anything (2026-08-01, ADR-0156 D4a)

Found by running `demos/region-to-cert` on a cold floor. It is the largest single finding of the
demos branch, and every part of it was invisible to the test suite.

**The defect.** ADR-0156 made `kubecompute` observe a `kubectl` transport, and the shim prefers an
observed transport over ssh — so the capstone's Apache converge, which had worked over ssh on
2026-07-30, began failing. It failed as `runner_on_unreachable: Failed to create temporary
directory … did not have permissions on the target directory`, which names the **guest's**
filesystem. The pod was healthy and that exact `mkdir` succeeded when run with permission. The real
cause was the API server refusing `kubectl exec`, because `dispatch.go` spawns every execution pod
with `AutomountServiceAccountToken: false` — "the pod has no cluster identity", deliberately. There
was no credential, and `pods/exec` appeared **nowhere in the repo**.

**Why nothing caught it.** Three separate guards were all satisfied:

1. ADR-0156 D6 checks the EE's _content_ — `kubernetes.core` and a `kubectl` binary. Both present.
2. ADR-0156's own transport table asked only what the **guest** needs, and for kubectl the honest
   answer is "nothing". There was no column for the control node, so the transport read as free.
3. `demos/scale-fleet` claimed the converge. It asserts the **Facet is observed** and narrated that
   as "a host that CONVERGES, over `kubectl exec` … the converge never touches port 22" — a converge
   it never launches. Measuring one thing and reporting a stronger one, which is this branch's
   recurring failure and the fourth instance of it.

**Fixed:** `connection.kubeconfigRef` (`ansible.input.v9`, a sibling of v8 — additive, every v8
declaration renders identically), rendered by the shim as the `ansible_kubectl_kubeconfig` group var
from the credential's **mount path**; a third validate axis that refuses a kubectl-transported target
with no brokered kubeconfig, naming the field and the reason; `task dev:kubecompute:up` mints a
ServiceAccount scoped to `create pods/exec` + `get pods` in `stratt-hosts` **only** — proven by a real
exec succeeding there and `Forbidden` in `stratt`. scale-fleet's claims are now bounded by what it
executes.

**Still open — the two transports that remain declared-not-proven.** `vmware_tools` and `aws_ssm`
have credentials named by ADR-0156 D4 and no guard demanding them, because neither has a target to
prove against (real vCenter; an SSM writer that does not exist yet). The kubectl guard is deliberately
_not_ generalized to them: a check written against no reachable target is how this defect shipped in
the first place. When each gets a live target, it gets its axis.

**Also booked:** `kubectl auth can-i create pods/exec --as=<sa>` reported **no** for a grant that
demonstrably works — the real exec succeeded with the same SA's token. Do not use `can-i` as evidence
for subresource grants; exec the thing.

**A KNOWN COST, measured rather than theorised: a Step declares its reach credentials
unconditionally, so a floor pays for credentials its targets never use.** `apache-configure` names
both `web-machine` (ssh) and `hosts-kubeconfig` (kubectl), because reach is per-HOST and a converge
recipe is not — one recipe must serve a mixed fleet. Dispatch therefore mounts what the STEP
declares, not what the targets turn out to need. `demos/scale-fleet` converges only pods, and the
moment its converge became real the Run died in `ContainerCreating` with `secret
"web-machine-creds" not found` — a MOUNT failure five minutes before anything could report why.

The obvious fix — resolve a credential only if some target turns out to need it — is refused, and
the reason is §2.5 rather than effort: it would make what a Step is AUTHORIZED to use depend on
graph state at launch time, and the CredentialRef use-check is the only authz gate an Action has.
Authority stays a declaration. What could improve without crossing that line is the DIAGNOSIS: a
mount failure for a declared CredentialRef is statically predictable at estate-load time (the Step
names a ref; the ref names a Secret; the floor either has it or does not), and today it surfaces as
a pod that never starts. Same class as the `fileset.content` unregistered-owner finding CERT-2
booked, and the fourth instance of "an advertised target nothing in the estate resolves".

**STILL OPEN — but NOT the race I first booked, and the correction matters more than the entry.**

What I recorded on 2026-08-01: "`mgmt.address` and `mgmt.transport` have different writers — the
build's terminal projection supplies the address, the Syncer's next Observe supplies the transport —
so a host is addressable before its reach method is known. Measured: the converge launched at
13:51:03 and the transport landed at 13:52:56."

**That mechanism does not exist.** Reading `plugins/kubecompute/server.go`: the build's terminal
projection calls the SAME `project()` the Syncer does, and it gates BOTH facets on
`pod.Status.Phase == PodRunning` — so the build emits NEITHER ("The built Entity rides the terminal,
WITHOUT mgmt.address: the pod has no address yet"). The two facets land together, atomically, in one
upsert. There is no window on this substrate. The timestamps I cited were the LATEST write of a
Facet the Syncer rewrites every cycle, which says nothing about when either first appeared — I read
a recency stamp as a first-appearance stamp. The converge that failed did so for the reasons since
fixed (no brokered kubeconfig, then a stale EE image), not for this.

**The real question is narrower and sharper: the shim cannot distinguish "reached by ssh" from
"reach method not yet known", because both are the absence of a Facet.** Two shipped cases prove it
is not theoretical:

- **awsec2 writes NO transport, deliberately and permanently** (`KeyName` means a key is authorized,
  not that sshd listens; SSM needs a different API). Absent transport here means ssh, and ssh is
  correct.
- **vcenter gates the two facets DIFFERENTLY**: `mgmt.address` from `Guest.HostName`/`IpAddress`,
  `mgmt.transport` from `ToolsRunningStatus == guestToolsRunning`. vCenter caches guest info, so a VM
  whose tools stop keeps a stale address and loses its transport — and the shim then falls back to
  ssh on a host whose observed answer was `vmware_tools`.

So absence is overloaded: for one provider it is an answer, for another it is a gap. That is §2.4's
territory — a value that means two things depending on who did not write it — and it needs an ADR.
Candidate shapes: a provider declares whether it observes transports at all (making absence
meaningful per-substrate); or `mgmt.transport` gains an explicit `ssh` value that awsec2 WRITES, so
absence always means unknown; or the shim refuses an unknown transport and the estate opts into ssh.
The second is closest to §1.2 — an observed fact stated rather than inferred from silence.

**DECIDED and PART-LANDED (2026-08-02, [ADR-0158](adr/0158-an-unobserved-transport-is-not-ssh.md)).**
The third shape won, and the second was refused outright: a provider writing "unknown" is a provider
ASSERTING something, and the Facet's whole contract is that it carries what was OBSERVED (D4).
Absence already expresses "nothing observed" precisely — the defect was the shim reading it as an
answer. So the shim now REFUSES a target with no observed transport and no declared type
(`requireReachMethod`), naming the target and both remedies, before `ansible-runner` is spawned.

🟢 **ACCEPTED and LIVE-PROVEN** (`task demo:app-cert:run` EXIT=0 on kind). Falsified two ways —
disabling the check fails 5 tests, deleting the CALL SITE fails exactly one, the end-to-end test
written for precisely that hole. `task ci` EXIT=0. The demo carries a second guard Workflow,
`unreached-target-guard`, whose Run must be refused with a message naming the target and both
remedies — so it is in E2E-1 and cannot rot back.

**Running it found three defects the unit tests could not**, which is the fourth time on this arc
that a reviewed-but-unexecuted seam was wrong:

- **The refusal reached no surface** — it returned an error instead of emitting a terminal, so the
  Run failed with the `error` field NULL and only a warn-level diagnostic line. **Fixed, and for
  all four reach axes**: the three ADR-0156 siblings (`validateTransports`, its credential axis,
  `refuseTransportAndDeclaredType`) had it too. Booked as a follow-up first, then done once the
  plugin port's OWN conformance suite turned out to grade a missing terminal as a `SeverityError`
  — "the core folds a stream that never terminated to FAILED; the Run fails with no stated cause"
  (`sdk/mockstratt/conformance.go`). That makes it a conformance violation to repair rather than
  ADR-0156's design to revisit. One table-driven test now covers all four, since the defect was
  exactly that three behaved one way and one another.
- **The refusal named a UUID.** `observedName(e)` falls back to the Entity ID when a host has no
  `*.name` label, so the message read `target d56e01a6-…`. Now prints the address beside it.
- **`GET /api/v1/runs?workflowRunId=` did not filter** — and the cause is sharper than a broken
  filter: **the parameter was never in the OpenAPI spec**, so an unknown query param was silently
  dropped and the route returned every Run in the estate. A caller got a plausible answer to a
  question it did not ask. The demo's new assertion read a different Workflow's error; the
  pre-existing vacuous-run assertion had the same bug and passed only because its Run happened to
  be the only failed one at that instant. **Fixed** (OpenAPI-first, §3): `workflowRunId` is a
  declared parameter delegating to `Store.ListRunsForWorkflowRun` — which already existed and which
  `GetWorkflowRun` already used, so only the route was missing. Pinned by a store test proving
  ISOLATION across two WorkflowRuns (the prior assertion used one, so a missing `WHERE` returned an
  identical answer) and falsified by removing the clause; the demo now asserts the server-side
  filter directly.

**Three of the ADR's own predictions were wrong and are corrected in it.** The per-host rendering
cost does not exist (ssh is the only declared type that can coexist with an observed transport, and
it authors no group var, so a mixed View works untouched); it is not a compile-time failure (targets
come from a View resolved at launch); and the migration was ~3× the stated size — **9 Steps across 8
estate files, five of which carried no `connection` block at all** and so were invisible to a grep
for `connection:`, plus 17 test functions in `plugins/ansible`. `demos/region-to-cert` needed
nothing; its pods observe `kubectl`.

**A drift this surfaced rather than caused, and it is still open.**
`contracts/facets/mgmt.transport.schema.json` says "awsec2 observes `aws_ssm` or `ssh`". **Neither
has a writer** — awsec2 deliberately writes no transport, and `aws_ssm` waits on the SSM client
booked above. So no shipped provider emits an observed `ssh` transport at all, which is exactly why
`ssh` is a DECLARED value in practice. The schema description should be corrected to describe what
is written; not done here, because a Facet schema edit is a pinned-hash change and deserves its own
change rather than a ride-along.

### Open follow-ups from the `fix/seam-continuity-and-fidelity` branch (2026-08-01)

Everything this branch deliberately did NOT finish, in one place, so none of it survives only in a
session's memory. Each line says what is blocked on what — several are blocked on a **target we do
not have**, which is a different thing from unfinished work and is marked as such.

**Owed verification (the highest-value items — code that exists and has not been proven):**

- ~~**ADR-0157 Phases 3–5 · WorkflowRun cancellation.**~~ 🟢 **DONE and LIVE-PROVEN (2026-08-03).**
  The native `POST /api/v1/workflow-runs/{id}/cancel` door, the `/api/v2/workflow_jobs/{id}/cancel/`
  façade route withheld in `1d7ffc0`, and the live proof — all three assertions green on kind,
  including **the K8s Job is gone**, now gated by `demo:app-cert`'s `cancel-guard` and therefore by
  E2E-1. ADR-0157 → Accepted.

  **The third assertion failed on its first run, and what it found had been shipped since
  ADR-0026: every cancellation was a lie.** The dispatcher Role granted `create`+`get` on
  `batch/jobs`, while `DeleteRunJobs` selects by the `stratt.dev/run-id` LABEL and so needs `list`
  before `delete` — it held neither. Measured: after a cancel, the guard's Job read
  `Complete, DURATION 2m4s` (its full sleep) and was **still present 243s later**, while the API had
  returned 202 and both rows read `canceled`. The pod converged a real host to completion.
  Invisible because the handler called cleanup as `_ = ExecuteActivity(…)`, and because
  `TestDeleteRunJobs` passes against a fake clientset with no RBAC while `helm lint` renders the
  Role without knowing what the Go calls — **the defect lived exactly in the gap between two
  passing tests.** Fixed: the verbs, the discarded error (now carried onto the Run's summary), and
  `TestChartGrantsTheJobVerbsThisPackageCalls` to pin them.

  Three more corrections to the ADR itself, all found by implementing it: a Gate does NOT unblock on
  cancellation (a Temporal `Selector` needs a `ctx.Done()` branch, so a cancelled DAG hung until its
  Temporal timeout and the door would have returned 202 having stopped nothing); D3 was not
  implementable as written (the inherited View was never persisted, so a Finding-launched
  remediation's cancel could not be authorized — migration 00050); and D5 needed a `canceled` Step
  outcome it had not named.
- **`aws_ssm` has no writer** (ADR-0156). The shim supports the transport; nothing produces it. The
  awsec2 Syncer can honestly observe neither EC2 path — `KeyName` means a key is AUTHORIZED, not
  that sshd is listening — so this needs the SSM client and an `ssm:DescribeInstanceInformation`
  permission. **The SSM client and the transport land together or not at all**, since a Facet has no
  other writer.
- **`vmware_tools` is shipped and unit-tested only** (ADR-0156). vspheresim implements the vCenter
  API but not Tools guest operations, so proving it needs **a real vCenter with a Tools-running
  guest**. Blocked on a target, not on design.
- **A live network-device run** (ADR-0153). The collection half is done and image-verified both
  ways; no real device has been driven. Needs an FRR or cEOS container in CI.
- **Windows (`winrm`/`psrp`)** (ADR-0153 D1). Blocked on a verifiable target ONLY — and note the
  plugins are in ansible-core, so no EE variant is needed. It is one enum entry plus a credential
  form that already exists.
- **`params-ignored` RunEvent publish** (`6e114fc`). The log half is tested; the publish half is not,
  because `Activities.Bus` is a concrete `*events.Bus` and no shipped Intent declares `params`.
- **E2E-1's `e2e:live` CI job.** Deliberately not added blind: a scheduled gate that cannot be made
  to fire from a dev session is an unverifiable gate, which is the shape this branch spent its
  length closing.

**~~BLOCKING for anyone quoting the capstone~~ — RESOLVED (2026-08-02). `demo:region-to-cert` DOES
pass from a cold floor; the entry below is kept for the diagnosis it records, not as a live blocker:**

Two independent green runs, both genuinely cold, both AFTER this entry was written:

- **CI, `e2e-live` run `30723212848`** (2026-08-01T23:22:42Z): job `region-to-cert` **success**. Every
  matrix job builds its own kind cluster, and `.github/workflows/e2e-live.yml` runs
  `task demo:region-to-cert:run` — "the SAME entry point a human uses, not a CI-shaped variant".
- **Locally (2026-08-02)**: `task dev:kind:down` to DESTROY the cluster, then
  `task demo:region-to-cert:run` → **EXIT=0**, full scenario, including both `Intent/Subnet` builds —
  which are the very `opentofu/apply` Actions the failure below names.

**What fixed it is NOT claimed.** The likeliest candidate is `fad1655` ("nothing ever ran `helm
dependency build` — every demo needed a machine where someone had fetched it by hand"), which landed
between this observation and the first green run, alongside the other CI fixes in that arc. Nobody
bisected it, so that stays a candidate rather than a cause.

**The lesson the entry earned, which outlives the bug:** it was written from one local failure and
stated as blocking; the evidence that contradicted it existed within hours and nothing reconciled the
two, so the tracker warned readers off a capstone that CI was proving green on every run. A finding
recorded and then not re-checked against later evidence decays into misinformation — the same failure
mode as an unexecuted seam, one layer up. **If a `BLOCKING` claim outlives the run that produced it,
re-run it before quoting it.**

<details><summary>The original entry, kept for the diagnosis (2026-08-01)</summary>

**`demo:region-to-cert` does not pass from a COLD floor (2026-08-01):**

Proof A dies after its gate is approved with:

```
cause: activity error (type: ExecuteAction …): no action registered as "opentofu/apply"
       (type: UnknownAction, retryable: false)
```

**This is almost certainly not new**, and that is the point. The capstone had only ever been run on a
WARM floor — every plugin already up from a previous run — and it passed there. Making `:run` reset
its own floor (this branch) is what first exercised the cold path, and the cold path fails. So the
capstone's green history says less than it appears to.

What was tried and did NOT fix it: reordering the task to wait for every Deployment BEFORE restarting
strattd, rather than after. That ordering was genuinely wrong and is fixed (`d56790e`) — it is the
same bug `demo:scale-fleet` hit with `kubecompute/create-host`, where it WAS the whole cause — but
the capstone still fails.

The unexplained observation, recorded because it is the next thread to pull: after a failed run the
`stratt-opentofu` pod was **54 seconds old**, i.e. it had come up _after_ strattd, despite the task
having waited for every Deployment to roll out first. Something restarts or replaces that pod after
the wait completes, and strattd therefore boots against a plugin that is not serving. Whether the
Action registration can recover from that at all (ADR-0103 promises a no-restart connector
lifecycle) is the question to answer first — if it can, this is a timing bug; if it cannot, the
no-restart claim needs revisiting.

**Not investigated further deliberately:** chasing it properly needs more than a guess, and a guess
committed here would be exactly the "looks fixed, was not measured" failure this branch spent its
length closing.

</details>

**Found by running things, and booked rather than fixed:**

- **A Gate decision with a missing `approve` field is silently a DENIAL** (found 2026-08-02 while
  writing ADR-0157's live proof, which sent `{"approved":true}` instead of `{"approve":true}` and
  watched the gate go `denied` by the caller who meant to approve it). `components.schemas.GateDecision`
  declares `required: [approve]`, but the handler decodes with `json.NewDecoder(...).Decode(&body)`,
  which does not enforce required fields — so an unknown key is dropped, `approve` defaults to
  false, and a typo becomes a decision recorded against the caller's Principal in the audit trail.
  Denial is the safe DIRECTION, which is exactly why it went unnoticed; it is still a wrong answer
  where a 400 belongs (§1.8), and the same decode pattern is worth auditing on the other write
  doors. Related to the `?workflowRunId=` finding in ADR-0158's arc: both are the API accepting a
  request it does not honour.

- **`kubecompute` advertises `provisions` but not `decommissions`.** It ships a build Workflow and no
  teardown Workflow, so an `Intent/Compute` count-DOWN offers nothing on the kubernetes substrate.
  vcenter has one (`vsphere-vm-teardown`, ADR-0114 D4), so the same edit against `vsphere-dc` would
  surface gated teardowns. Found by `demo:scale-fleet`, whose leg D reports this rather than
  asserting a number nobody measured.
- **`kubecompute` still bakes sshd into every pod it builds.** ADR-0156 makes that coupling
  removable — the kubectl transport needs nothing in the guest — and it was left in deliberately so
  the transport change and the pod change are not one variable.
- **Cross-Cell cancel is unmeasured** (ADR-0157 D6). The ADR does not claim the Gate-decision path
  federates correctly; that needs a two-Cell floor. Until measured, a peer-homed cancel must fail
  naming the reason rather than signal the local Temporal.

**Owed reviews (this session's rules barred the subagents):**

- **`charter-guardian` and `vocabulary-linter` on ADR-0150, 0153, 0154, 0155, 0156 and 0157.** Five
  of those add a Facet namespace or a Contract version; ADR-0150 names two reviews as gating in its
  own text. A steward must run them.
- **ADR-0152 stays GATED** on the steward decision it asks for (the §2.1/§2.4 amendment), and its
  CONTRACT half must land in a LATER release than the expand half (ADR-0078).

**Still open from the plan, unchanged by this branch:**

- **W6 residue** — ANS-013 (pre-flight syntax check), ANS-009 (multi-document playbooks), AWX-015
  (`ask_*_on_launch` beyond variables; attaching a credential or inventory at launch is deliberately
  refused today, and that is a desired-state question rather than a missing endpoint).
- **AWX-005 is DECLINED, not pending** (ADR-0130 D3), and the distinction matters: a projected grant
  graph is one query from being used as an authorization truth, which no read-shape fix answers.
  "We looked and said no" must not render the same as "nobody looked".
- **A cross-plugin composed Workflow still has no shippable home** — every estate hand-copies
  `cert-issue`. Needs a composition pack (ADR-0033's materialize-into-operator-Git shape).
- **ADR-0080 slice 2** — `software.package` has a bootstrap write-owner, not the Syncer collector.
- **`scm_revision` comparison** (ADR-0154 D2) — needs the content half to observe its own git
  checkout first.
- **W7** — Automation Hub class, EDA rulebook depth, mesh multi-hop, gateway edges.

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
