# Roadmap & phase status

Living status tracker for the phased plan in **[stratt-charter.md](../stratt-charter.md) §8**. The charter
is the authority on *what each phase is*; this file records *where we actually are* against it, with
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

| Deliverable | State | Evidence |
|---|---|---|
| Entity/Facet/Provenance store | ✅ | `core/internal/graph` (migration `00001_graph_spine`) |
| One native Syncer (vCenter-class) | ✅ | `plugins/vcenter` (re-centered from in-tree, ADR-0046; [ADR-0007](adr/0007-phase0-syncer-sdk-and-dev-harness.md)) |
| View query → Temporal Workflow → K8s Job (ansible-runner) | ✅ | `orchestrate`, `dispatch`, `plugins/ansible` (EE-Job transport, ADR-0051) |
| Facts projected back with provenance | ✅ | `graph.RunProjector`, `orchestrate.ProjectFacts` |
| Live SSE tail | ✅ | `events.Bus.Tail`, `GET /runs/{id}/events` |

## Phase 1 — Usable core 🔶 (code ✅ · exit gate 🚫)

**Code-complete.** The promote gate (Nebulae daily-driver, 30 days zero data loss) and the **OSS gate
(repo public with DCO/ADRs/quickstart)** are **not met** — the repo stays private until OSPO/IP clearance
(charter §7.4, the highest project risk). So Phase-1 *work* is done; its exit gate is blocked.

| Deliverable | State | Evidence |
|---|---|---|
| Ansible Actuator (EEs, per-target results, slicing) | ✅ | `plugins/ansible` (EE-Job shim, ADR-0051), `RunInput.Slices` |
| `script` Actuator | ✅ | `plugins/script` (EE-Job shim, ADR-0046) |
| Git desired-state sync + `stratt apply`/`plan` | ✅ | `desiredstate`, `POST /desired-state/{plan,apply}` |
| Views UI | ✅ | `ui/` ([ADR-0012](adr/0012-views-ui-v1.md)) |
| Workflows + Gates | ✅ | `orchestrate.RunDAG`, `DecideGate` ([ADR-0011](adr/0011-workflows-gates-v1.md)) |
| Schedules | ✅ | `triggers`, Temporal Schedules ([ADR-0010](adr/0010-triggers-v1-schedules.md)) |
| CredentialRefs (Vault + K8s) | ✅ | `dispatch.CredentialMount` ([ADR-0009](adr/0009-identity-authz-credential-brokering.md)) |
| OIDC + basic OpenFGA | ✅ | `authz` (OpenFGA + tuples), OIDC resolver |
| Helm chart | ✅ | `deploy/charts/stratt` ([ADR-0013](adr/0013-helm-packaging.md)) |
| MS Graph + cloud-instance Syncers | ✅ | `plugins/msgraph`, `plugins/awsec2` (re-centered, ADR-0046; [ADR-0014](adr/0014-connector-breadth-msgraph-ec2.md)) |
| **Promote:** Nebulae daily-driver, 30d zero data loss | ⬜ | operational, not code |
| **OSS gate:** OSPO clearance → repo public (DCO/ADRs/quickstart) | 🚫 | charter §7.4 blocker |

## Phase 2 — Seams + intent layer ✅ (code)

**Code-complete.**

| Deliverable | State | Evidence |
|---|---|---|
| OpenTofu Actuator (plan/apply Gates, encrypted HTTP state backend, output→Contracts) | ✅ | `plugins/opentofu` (re-centered, ADR-0046), `statebackend` (core transport) ([ADR-0016](adr/0016-opentofu-actuator.md)/[0017](adr/0017-tofu-outputs-to-entities.md)) |
| Trigger engine (webhook + Alertmanager Emitters, CEL) | ✅ | `triggerengine`, `emitters`, `rules` ([ADR-0018](adr/0018-trigger-engine.md)) |
| Intent/Assignment/Blueprint compiler (claim types, ownership registry, membership-delta, max-delta gate) | ✅ | `compiler` ([ADR-0023](adr/0023-intent-compiler.md)) |
| Baselines + Findings v1 (check-mode + tofu plan, flap damping) | ✅ | `baselines`, `graph.findingstore` ([ADR-0019](adr/0019-baselines-findings-v1.md)) |
| MCP actuator/Action adapter + platform MCP server | ✅ | `plugins/mcp` (EE-Job transport, ADR-0053), `mcpserver` (core agent-native surface) ([ADR-0021](adr/0021-platform-mcp-server.md)/[0022](adr/0022-mcp-actuator.md)) |
| AWX importer + `/api/v2` façade | ✅ | `awximport`, `awxfacade` ([ADR-0025](adr/0025-awx-importer-and-ansible-scm-content-ref.md)/[0026](adr/0026-awx-api-v2-facade.md)) |
| Notifications | ✅ | `notify` ([ADR-0027](adr/0027-notifications.md)) |

## Phase 3 — Enterprise + fleet 🔶 (code ~90% · gates 🚫)

Substantially built. Two Connectors are **deliberately deferred** (no current need or environment to
connect to — revisit when a real tenant requires them); the promote/OSS gates are open.

| Deliverable | State | Evidence |
|---|---|---|
| Sites (NATS leaf) | ✅ | `sitegw`, `siteproto`, `cmd/stratt-agent` ([ADR-0032](adr/0032-sites-remote-execution-loci.md)) |
| Full OpenFGA (View-scoped execution, use-without-read) | ✅ | `authz.RelationRunner`/`RelationUser` ([ADR-0028](adr/0028-view-scoped-execution-authz.md)) |
| HA + DR runbook | ✅ | [ADR-0040](adr/0040-high-availability-and-disaster-recovery.md), [runbooks/ha-dr.md](runbooks/ha-dr.md) |
| audit → SIEM sink | ✅ | `forwarder`, `cmd/stratt-forwarder` ([ADR-0034](adr/0034-audit-stream-and-siem-forwarder.md)) |
| SCIM | ✅ | `scim` ([ADR-0035](adr/0035-scim-service-provider.md)) |
| Pull agent + Bundles | ✅ | `cmd/stratt-agent` (pull), `bundle` ([ADR-0032](adr/0032-sites-remote-execution-loci.md)) |
| Evidence store (object-lock) + CIS pack | ✅ | `evidencestore`, `packs/cis` ([ADR-0029](adr/0029-evidence-store-object-lock.md)/[0033](adr/0033-cis-pack-compliance-as-data.md)) |
| `Intent/Certificate` + `Intent/FileSet` + `Intent/Access` GA | ✅ | `plugins/certissuer` (Syncer + reconcile Actuator, ADR-0050), `types.Intent{Certificate,FileSet,Access}` ([ADR-0030](adr/0030-intent-certificate-ga.md)/[0036](adr/0036-intent-fileset-access-ga.md)) |
| **Jamf Connector** | ⏸ | deferred — no current need/environment |
| **ConfigMgr (SCCM AdminService) Connector** | ⏸ | deferred — no current need/environment |
| **Promote:** production for a bounded service class; 99.9% 30-day SLO; security review | ⬜ | operational, not code |
| **OSS gate:** v1.0; ≥2 external maintainers; ≥3 community plugins; CNCF Sandbox; vocabulary freeze | 🚫 | gated by §7.4 going-public |

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
connectors listed above were originally *in-tree*; they have all been re-centered out. This is a re-architecture
of existing capabilities, not new phase work — the deliverables above still stand, they now live behind the port.

| Slice | State | Evidence |
|---|---|---|
| Sovereign plugin port + content-blind spine (the thesis) | ✅ | [ADR-0046](adr/0046-stratt-as-substrate.md); `sdk/stratt/plugin/v1`, `pluginhost` |
| Port v1 full surface (write-back, relations, rung ladder, plan pinning) | ✅ | [ADR-0047](adr/0047-plugin-port-v1-full-surface.md); `pluginhost.ApplyRaw`/`PlanStep` |
| Integration taxonomy (connector-plugin vs migration-tool vs core-transport) | ✅ | [ADR-0048](adr/0048-integration-taxonomy-plugin-tool-transport.md); AWX importer relocated, façade kept |
| Sites over the port (agent = authenticated relay, governance stays hub-side) | ✅ | [ADR-0049](adr/0049-sites-over-the-plugin-port.md); `sitegw`, `siteproto` typed stream |
| Certificate lifecycle as a reconcile Actuator | ✅ | [ADR-0050](adr/0050-certificate-reconcile-actuator.md); `plugins/certissuer` |
| Ansible EE-Job subprocess transport (GPL boundary in the EE image) | ✅ | [ADR-0051](adr/0051-ee-job-speaks-the-port.md); `plugins/ansible`, one `govern` |
| SecretBroker port (per-call resolution; core holds no material, §2.5) | ✅ | [ADR-0052](adr/0052-secretbroker-port.md); `sdk/secretbroker` |
| MCP as a generic transport (the last domain logic leaves the core) | ✅ | [ADR-0053](adr/0053-mcp-transport-generic-connector.md); `plugins/mcp` EE-Job shim |
| Per-Step facet write-scope (least-authority write-back at the one governor) | ✅ | [ADR-0054](adr/0054-per-step-facet-claim.md); `pluginhost.govern` grant∩scope |

**Verified in-repo (structural):** `core/internal/connectors/` is empty; `internal/actuators`,
`internal/actions`, `internal/emitters` hold only the seam interfaces, no tool logic; **12 tools** live in
`plugins/` (ansible, awsec2, certissuer, chef, mcp, msgraph, notify, opentofu, puppet, salt, script, vcenter);
the execution path routes by registry lookup, not a tool-name switch, with no platform-default actuator
(ADR-0046). The residual tool-name strings in core are legitimate — opaque routing-key registration in the
composition root (`cmd/strattd`), the AWX `/api/v2` compat façade, and the AWX one-shot migration tool.

**Honest caveat:** this is **structurally code-complete and proven by unit/integration tests** (Site-forwarded
governance, the govern grant∩scope intersection) — **not** yet by a live end-to-end run on a real
NATS+K8s+Temporal cluster (environment-blocked). The exit gates are unchanged: the re-centering does not move
any promote/OSS gate, which still waits on §7.4.

## Ahead of the roadmap: multi-region Cells

The **[ADR-0044](adr/0044-control-plane-cells.md) Cells workstream (slices 1–7, complete)** delivers
multi-region active/active with fenced Source re-home — a capability the roadmap places at Phase 4 and
beyond. [ADR-0040](adr/0040-high-availability-and-disaster-recovery.md) explicitly *deferred* cells, and
the 99.99% multi-region target sits *above* Phase-3's 99.9% single-region SLO
([evidence map](evidence/multi-region-99_99.md)). The cross-Cell mechanisms (federated read, partial-result
honesty, fenced re-home over real HTTP) are **demonstrated end-to-end** by the two-Cell harness
(`task e2e:cells`) against live Postgres — the measured SLO still needs a real fleet. Follow-up
[ADR-0045](adr/0045-db-driven-syncer-home-gate.md) (full re-home auto-cutover) is Proposed, not scheduled.

## Dev follow-ups / test hygiene

- **Two timing-sensitive tests flake under concurrent `task ci` load** (each passes clean in isolation
  and on re-run; neither is a correctness bug): `core/internal/triggers/reconcile_test.go` (a
  `time.Sleep(500ms)` around a reconcile cadence) and `core/internal/siterelay` (relay timing). On a
  heavily-loaded box (kind + Crossplane + the full parallel suite) they occasionally miss their fixed
  sleep window and fail with a bare `FAIL` / exit 201. **Fix:** replace the fixed `time.Sleep` with a
  poll-until-condition-with-deadline (e.g. `require.Eventually`-style) so the gate is deterministic
  regardless of host load. Low-risk, isolated to test files; do before wiring CI on a shared runner.

## Where we are, in one line

Phases 0–2 code-complete; Phase 3 code ~90% (Jamf + ConfigMgr Connectors deferred by choice); multi-region
Cells shipped ahead of schedule; and the whole platform has been **re-centered onto the sovereign plugin port
(dark-matter, ADR-0046 arc)** — the core spine is content-blind and every tool is a plugin, verified structurally
in-repo and by unit/integration tests, with the live-cluster e2e still outstanding. **No phase's promote/OSS
exit gate is met** — every one ultimately waits on the charter §7.4 going-public step (OSPO/IP clearance) plus
real operational evidence (SLO, security review, adoption), none of which is a coding task.
