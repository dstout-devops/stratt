# AAP 2.7 parity — what we have, what we need

**Target:** Red Hat **Ansible Automation Platform 2.7** (GA 2026-05-28) as the feature bar for the
"structurally-open successor" thesis (charter §0, §7.1). This is the **full platform** — six
components, not just the AWX job-runner — plus the 2.7-specific deltas.

**Framing (charter §8):** *"parity features lead every phase so the platform is useful before it is
novel."* Parity is the **floor**; the estate-graph + typed-seams + intent layer is the **differentiator**
AAP structurally lacks (charter §7.1: estate graph ❌, typed seams ❌, intent/routing ❌, cross-tool
drift ❌, provisioning ❌, any-K8s ❌ — all ✅ for Stratt). This doc tracks the floor.

> Evidence base: a 5-way codebase inventory (2026-07-19) cross-referenced against the AAP 2.7
> release notes. Component verdicts below are grounded in files + ADRs, not aspiration.

## Scorecard

| AAP 2.7 component | Verdict | One-line |
|---|---|---|
| **Automation Controller** (job runner) | 🟢 **code-complete core**, 🟡 edges | Every core capability shipped; gaps are notification sinks + `/api/v2` route breadth |
| **Policy-as-code** (OPA gate, 2.6+) | 🟢 **ahead** | 4-valued lattice + typed Control library + dual PEPs + obligations vs AAP's thin OPA allow/deny |
| **Platform Gateway** (unified UI/API/RBAC/SSO) | 🟢 **code-complete core**, 🟡 UI/analytics | Unified UI, OIDC, OpenFGA, SCIM, one Principal, one audit stream, platform MCP; gaps are analytics/org/admin UI |
| **Automation Mesh** (distributed exec) | 🟢 **code-complete**, one gap | Sites (push+pull) + signed Bundles + Cells (a partitioning story AAP lacks); gap = multi-hop relay nodes |
| **Event-Driven Ansible** (rulebooks) | 🟡 **partial** — spine yes, depth no | Trigger engine covers ingest→CEL→launch+dedup; missing rulebook format, stateful/meta conditions, throttling, source breadth |
| **Automation Hub** (content/EE/supply-chain) | 🔴 **biggest gap** | No content registry, no EE-build factory, SBOM/SLSA pipeline unbuilt; plugin+contract-pinning model substitutes the *trust* half only |

**Bottom line:** the AWX-successor **job-runner + governance + distributed-execution + identity** surface is
built and in places ahead. The credible-replacement work concentrates in **(1) Hub-class content/supply-chain,
(2) EDA rulebook depth, (3) `/api/v2` + notification breadth for a clean migration**, plus the unbuilt
**live-cluster proof**.

---

## Per-component detail

### 1. Automation Controller — 🟢 code-complete core
The AWX object model is deliberately collapsed onto Named Kinds (charter §2 mapping): job template →
Step preset / single-Step Workflow, inventory/smart/constructed → **View**, survey → input Contract,
job → **Run**, credential → **CredentialRef**, project → SCM content-ref. AWX-shaped objects exist only
in the `awxfacade` wire layer.

| Capability | Status | Evidence |
|---|---|---|
| Job Templates | 🟢 | [orchestrate/](../core/internal/orchestrate/), ansible Actuator [plugins/ansible/](../plugins/ansible/), ADR-0051 |
| Workflow DAG + approval nodes | 🟢 | [orchestrate/workflow.go](../core/internal/orchestrate/workflow.go) (`RunDAG`, gates), ADR-0011 |
| Projects (SCM content) | 🟢 | in-EE git clone [plugins/ansible/shim.go](../plugins/ansible/shim.go), ADR-0025 |
| Inventories (+ dynamic/smart/constructed) | 🟢 | Views [graph/reader.go](../core/internal/graph/reader.go) + Syncers; façade `viewToInventory`, ADR-0012/0024 |
| Credentials + injectors | 🟢 | CredentialRef + SecretBroker [sdk/secretbroker/](../sdk/secretbroker/), ADR-0052 |
| Execution Environments | 🟢 | [ee/Dockerfile](../ee/Dockerfile) (ansible-runner `/runner` contract), ADR-0051 |
| Schedules | 🟢 | [triggers/reconcile.go](../core/internal/triggers/reconcile.go) (Temporal Schedules), ADR-0010 |
| Surveys | 🟢 | input Contract `ansible.input.v4` + parametrized Views, ADR-0024 |
| Job slicing / per-target results | 🟢 | `RunOutcome.PerTarget`, `Slices` [orchestrate/across.go](../core/internal/orchestrate/across.go), ADR-0054 |
| Fact caching | 🟢 | facts → governed Facets w/ Provenance, ADR-0054 |
| Webhooks | 🟢 | Emitter receiver [emitters/](../core/internal/emitters/), ADR-0018 |
| **Notifications** | 🟡 | **webhook sink ONLY** — [notify/dispatcher.go](../core/internal/notify/dispatcher.go) rejects other kinds; no Slack/email/SMTP/PagerDuty. ADR-0027 |
| **`/api/v2` façade** | 🟡 | [awxfacade/](../core/internal/awxfacade/): job_templates/jobs/inventories(+launch/cancel/stdout) shipped; **missing** workflow_job_templates, schedules, projects, credentials routes. ADR-0026 |
| Custom credential **types**/injectors | 🟡 | fixed `injectionFor` map, not a user-definable injector DSL |

### 2. Policy-as-code — 🟢 ahead of AAP 2.7
AAP 2.7's policy feature is a thin, new, OPA-only **binary allow/deny** gate on job execution. Stratt
reproduces that (external OPA/Kyverno via [policy/exec.go](../core/internal/policy/exec.go), ADR-0074) as
*one pluggable provider behind a port*, then goes well beyond: a **four-valued Decision lattice**
(allow/require-approval/escalate/deny, ADR-0062), enforced **obligations** that become tracked Findings
(ADR-0075), a **typed Control library** — TimeWindow/SoD/Waiver/BreakGlass/Quorum (ADR-0067–0071), **dual
PEPs** (execution gate *and* config-admission at every door incl. the imperative API, ADR-0073/0076),
**un-bypassable mandatory floors** (ADR-0066), and hash-chained durable decision recording (ADR-0065).
Self-imposed TODOs only (shape unification, live-refresh, gRPC transport for external engines).

### 3. Platform Gateway — 🟢 code-complete core, 🟡 UI/analytics
| Capability | Status | Evidence |
|---|---|---|
| Unified UI (single-pane) | 🟢 | [ui/src](../ui/src) TanStack SPA: Views/Entities/Runs/Workflows/Gates/Triggers/Findings/Baselines |
| SSO / OIDC | 🟢 | [authz/oidc.go](../core/internal/authz/oidc.go), Zitadel, ADR-0012 |
| Unified RBAC (OpenFGA) | 🟢 | [authz/openfga.go](../core/internal/authz/openfga.go), ADR-0028 |
| User/team lifecycle (SCIM 2.0) | 🟢 | [scim/](../core/internal/scim/), group→team authz, ADR-0035 |
| One Principal (UI/CLI/CI/agent) | 🟢 | `types.Principal`, one `ResolvePrincipal` seam for `/api/v1` + MCP, §1.6 |
| One audit stream + SIEM | 🟢 | hash-chained [audit/](../core/internal/audit/) + [forwarder/](../core/internal/forwarder/), ADR-0034 |
| Platform MCP server (agent door) | 🟢 **differentiator** | [mcpserver/](../core/internal/mcpserver/), ADR-0021 — AAP has no first-class MCP door (theirs is 2.7 tech-preview) |
| **Cost/usage accounting** | 🟡 | MCP-call counts only (`types/usage.go`); **no run-minute/monetary/federated cost**, no dashboard (§7.6) |
| **Multi-tenancy** | 🟡 | Cells (ADR-0044) + Environments (ADR-0057) + authz scoping; **no first-class Organization container** |
| CLI query verbs | 🟡 | `stratt plan/apply/import/bundle`; no `get/describe` estate-query UX |
| In-app activity-stream / org / settings UI | 🔴 | audit is backend/SIEM only; no admin/settings/analytics screens |

### 4. Automation Mesh — 🟢 code-complete, one gap
Sites (push+pull, [sitegw/](../core/internal/sitegw/), ADR-0032), the pull **agent** as an authenticated
transport relay (ADR-0049), cosign-verified **Bundle** pull ([bundle/](../core/internal/bundle/)), and
**Cells** (control-plane partitioning, ADR-0044/0045) together give receptor-class remote execution *plus*
a partitioning story AAP has no equivalent for. **Genuine gap:** **multi-hop / relay (hop) nodes** — Stratt
is a flat hub↔leaf NATS model; Receptor's arbitrary routable mesh (control/hop/execution chains for deep
DMZ traversal) is not modeled. *(Note: `plugins/mesh/` is the service-dependency connector — a false
friend, unrelated to automation mesh.)*

### 5. Event-Driven Ansible — 🟡 partial (spine, not depth)
The Trigger engine (Emitter × CEL → Workflow/View launch, ADR-0018) covers the **spine**: event ingest,
condition eval, at-least-once durable launch (JetStream), content-hash dedup, and full authz/descent
parity. It is **not a rulebook engine**. Gaps:
- **Source breadth** — 3 kinds (webhook, alertmanager, salt-stream) vs AAP's dozens of `ansible.eda.*`
  sources (Kafka, SQS, Azure SB, journald, file-watch…). Only alertmanager has a structured explode.
- **Rulebook format** — a Trigger is `1 Emitter + 1 CEL → 1 target`; no ordered multi-rule ruleset.
- **Stateful / meta conditions** — CEL sees one event; no `count > N within Ms`, no cross-event correlation.
- **Throttling / debounce / rate-limit** — dedup only.
- **Inline meta-actions** — can only launch a Workflow/View; no `set_fact`/`post_event`/`run_module`.

### 6. Automation Hub — 🔴 biggest gap
AAP's hosted, signed, versioned **collection** distribution + **EE registry** + **collection-signing** trust
story. Stratt covers the **trust + execution half** convincingly — boot-time hash-**pinned** Contracts that
refuse-to-boot on drift (ADR-0015), core-side **verify-don't-trust** of plugin outputs (ADR-0047),
cosign-verified OCI **Bundles** (ADR-0032), digest-pinned images throughout — and reframes "content breadth"
as independently-shipped **plugin images**, each its own CI unit (ADR-0046). What's genuinely missing:
- **Content registry / index** — no collection hosting, no plugin catalog/discovery/version-resolution
  (relies on external OCI + hand-pinned Helm refs).
- **EE build tooling** — no `ansible-builder` / `execution-environment.yml` factory; a single hand-written
  [ee/Dockerfile](../ee/Dockerfile) (compat asserted, not automated) and no EE distribution service.
- **Supply-chain pipeline** — charter §7.3 promises cosign/SBOM/SLSA "from release one," but
  [.github/workflows/ci.yml](../.github/workflows/ci.yml) implements only DCO. Image signing + SBOM + SLSA
  attestation are **unbuilt** (signing is real only on the pull-Bundle path). *(This is enterprise-crack
  SEC-5/SUP-1, now sharper because the container collector projects digests.)*
- **Remote/upstream sync** — no Galaxy mirror, no `requirements.yml` resolution, no air-gap content seeding
  (git-sync covers SCM-project delivery only).

---

## 2.7-specific deltas

| AAP 2.7 new feature | Stratt posture |
|---|---|
| **MCP server integration** (tech preview) | 🟢 **ahead** — platform MCP server is GA-in-repo (ADR-0021), agent-native is a founding discipline (§1.6) |
| **No RPM installer** (containers/OpenShift only) | 🟢 **advantage** — any-K8s Helm (charter §7.1: any-K8s ✅ vs AAP ❌ OCP-only) |
| **OIDC IdP for Vault** (short-lived job-scoped JWTs) | 🟡 **divergent** — SecretBroker resolves per-call, never-persisted creds (ADR-0052); we don't literally issue Vault JWTs, but the "no long-lived creds" goal is met differently |
| **Self-service portal / visual EE builder / content catalog** | 🔴 missing (ties to Hub gap + no EE-build UI) |
| **Automation dashboard / ROI analytics** | 🔴 missing (cost/usage is MCP-calls only) |
| **AI assistant / BYOK (Lightspeed)** | ⚪ **not our thesis** — agent-native via MCP is our answer, not AI content-generation |
| **Dev workspaces** (browser VS Code) | ⚪ **out of scope** — a dev-tooling product, not the platform layer |

---

## Prioritized gap backlog

**Tier 1 — parity-blocking / migration-blocking (do first):**
- **P1 · Live-cluster e2e** — the actual replacement proof (see below). Nothing here is *new* code; it's the
  first real integration of importer + façade + EE plugin on kind.
- **P2 · `/api/v2` route breadth** — `workflow_job_templates`, `schedules`, `projects`, `credentials` so
  existing AWX/AAP tooling survives cutover (the strangler-fig front door, §7.6).
- **P3 · Notification sinks** — at least Slack + SMTP/email beyond webhook (a real Controller expectation).

**Tier 2 — real feature gaps (differentiated approach OK, but must be honest):**
- **P4 · EDA rulebook depth** — stateful/meta conditions + throttling + a rulebook authoring format +
  source-plugin breadth. Own ADR (this is a design surface, not a patch).
- **P5 · EE build factory** — an `ansible-builder`-compatible `execution-environment.yml` → image path.
- **P6 · Supply-chain pipeline** — cosign image signing + SBOM (syft) + SLSA provenance in CI (§7.3, SEC-5/SUP-1).
- **P7 · Cost/analytics** — run-minute + per-Principal cost accounting + the strangler-fig routing dashboard (§7.6).

**Tier 3 — divergent-by-design / non-goals (document, don't chase blindly):**
- Collection registry + Galaxy remote sync (plugin model substitutes; **air-gap seeding** is the part that
  still matters to enterprises — revisit).
- Multi-hop / relay mesh nodes (flat hub↔leaf is a deliberate simplification; deep-DMZ traversal is the real
  use case to weigh).
- Organization container (Cells + Environments + authz substitute).
- In-app activity-stream / admin / settings UI screens.

---

## The end-to-end (the replacement proof)

The pieces exist **today** — this is wiring + a live harness, not new subsystems:

1. **Import** a real AWX **24.6.1** export via `stratt import awx` (ADR-0025) → Step presets + Views +
   Workflows + CredentialRefs reconciled into the estate.
2. **Launch** an imported job template via `POST /api/v2/job_templates/{id}/launch` (ADR-0026 façade).
3. **Execute** over the **ansible EE plugin** as an ephemeral K8s Job speaking the sovereign port (ADR-0051),
   against a View-resolved target.
4. **Tail** via `GET /api/v2/jobs/{id}/stdout` and **cancel** via the native cancel path.
5. **Project facts back** as governed Facets (ADR-0054) — the "AWX can't do this" moment.

**What the e2e needs that isn't wired yet:** a live-cluster harness (kind + Helm + the EE image built), a
seeded AWX export fixture, and — if we want the workflow half demoed through the compat door — the
`workflow_job_templates` façade route (P2). Everything else is shipped.

**This is the single highest-leverage move toward a credible "AAP running on Stratt" claim** — and it will
surface whatever integration gaps the in-repo verification has been hiding (the live-cluster e2e, E2E-1, is
still outstanding).

---

## Sources
- [What's new in AAP 2.7 — Red Hat Developer](https://developers.redhat.com/articles/2026/06/10/whats-new-red-hat-ansible-automation-platform-2-7)
- [What's New in AAP 2.7 — Red Hat blog](https://www.redhat.com/en/blog/whats-new-ansible-automation-platform-2-7)
- [AAP 2.7 release notes — Red Hat Documentation](https://docs.redhat.com/en/documentation/red_hat_ansible_automation_platform/2.7/whats_new-overview_of_redhat_ansible_intro)
