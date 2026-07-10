# Stratt — Project Charter
### An open estate automation platform
*(Working name pending trademark check. This document is the design authority for the project; it supersedes all prior drafts.)*

---

## 0. Thesis

The successor to AWX/AAP — and to the broader systems-management category (SCCM, Jamf, Intune, Chef, Salt) — is not a better job runner. It is a **platform layer**: a typed estate graph plus a durable orchestration engine, where every tool (Ansible, OpenTofu, Helm, Packer, Graph API, Jamf API, arbitrary MCP servers) is a plugin that consumes typed inputs from the graph and writes typed, provenance-stamped outputs back into it. Tools are actuators; the platform owns the seams between them — which is where the industry's duct tape actually lives.

Above the graph sits an **intent layer**: small, friendly documents teams write, compiled through platform-owned Blueprints into enforcement. Teams declare *what*; routing resolves *how* per device class. This makes Stratt a consolidation instrument for large-organization sprawl — a Backstage-like front-of-house, but one that outsources the *doing* to pluggable domain backends rather than opining on them, and one whose per-route cost/failure accounting makes the mess underneath legible enough to retire, one route at a time (§7.6, the strangler-fig posture).

**Market context (verified July 2026):** AWX frozen since 24.6.1 (July 2024) with releases paused indefinitely; AAP 2.7 removed the RPM installer, leaving Podman-on-RHEL or OpenShift as the only supported paths — no vanilla-Kubernetes story exists or is coming; Chef Infra Server EOL November 2026; Puppet forked to OpenVox after Perforce restricted binaries; Salt's maintenance under Broadcom is in open question. The market hole: an actively maintained, any-Kubernetes, config-as-code-first, structurally open successor. Every incumbent lost its community to governance failure, not technical failure. Governance is the moat.

---

## 1. Founding Disciplines
Binding on all decisions. Changes to this section require the highest review bar in the project.

1. **Type the seams, not the world.** Contracts (JSON Schema) attach at plugin boundaries and to named Facets — never to whole Entities, never as a universal ontology precondition. Every Facet schema must be demanded by a shipping Contract. (Anti-TOSCA/anti-CSDM; also the bet that distinguishes us from System Initiative's full-fidelity modeling.)
2. **Projections, never a second truth.** External systems of record stay authoritative (Terraform state, etcd, Intune, vCenter). The graph is a rebuildable read-model written only by Normalizers and Run provenance — enforced in the data layer, not by convention. Desired state lives in Git. Drift is the diff.
3. **Rug-pull-proof by structure.** Apache-2.0 everything — no gated tier, ever. DCO, not CLA (distributed copyright makes relicensing practically impossible). Foundation trajectory (CNCF Sandbox target). Public repo, roadmap, ADRs, and triage from first tagged release.
4. **Boring spine, pluggable everything.** Core team owns the spine (graph, orchestration, contracts, authz, audit). Community owns breadth via plugin surfaces. Dependencies: few, boring, huge-community (Postgres, NATS, Temporal).
5. **Sovereign contracts, multiple transports.** The platform's connector contract is its own; REST/gRPC, subprocess, and MCP are transports beneath it. No external protocol is load-bearing for the deterministic core. All plugin schemas are pinned and hash-verified at registration; schema drift is detected and blocking, never silently absorbed.
6. **Agent-native, human-first.** Every capability is exposed identically to UI, CLI, CI, and AI agents (via MCP) under one Principal model, one authorization model, one audit stream, with cost/usage accounting per identity.
7. **Evergreen contract.** Every runtime, toolchain, and substrate dependency remains ≥ N-1 on its major/LTS line (current or previous — never older), enforced by CI policy gates that fail builds, not by intention. Quarterly upgrade train; published support matrix. Applies bidirectionally: what Stratt runs on (Go, Node LTS, Postgres, Temporal, NATS — plus Python inside execution pods and the plugin SDK) and what Stratt supports (Kubernetes per upstream N-2 skew, Postgres N-1, actuator tool versions). Upgrade-friendliness is a first-class dependency-selection criterion; a library's upgrade track record is evaluated before adoption. The platform must never become the Django-monolith fossil its predecessor did — Red Hat's own admission that AWX's "architecture limits the ability to change" is this discipline's origin story.
8. **The abstraction must never hide diagnosis.** Hiding *mechanism* is the product; hiding *failure* kills trust. From any Intent, a user can descend the full ladder — Intent → Blueprint route → Workflow → Run → task event — in one click. Users who can't diagnose through the front end will route around it, and the platform dies.

**Non-goals (permanent unless amended here):** MDM protocol implementation (Intune/Jamf are Connectors); OS imaging/bare-metal; new configuration languages (playbooks, HCL, charts remain unmodified content); a writable CMDB (we feed CMDBs); a paid tier.

---

## 2. Vocabulary — the Named Kinds
Naming is API. Frozen at v1.0 with a formal deprecation policy thereafter. **Banned terms in core-model identifiers:** `inventory`, `playbook`, `job template`, `CI`, `CMDB`, `resource` — each is a tool-specific rendering or a namespace collision. Migration docs map old→new (AWX job template → Step preset; smart inventory → View; survey → input Contract with UI hints).

### 2.1 Graph plane
- **Entity** — a node: anything with identity (host, VM, device, cert, VPC, namespace, account). Identity keys + labels + typed document + per-attribute Provenance.
- **Relation** — typed directed edge (`runs-on`, `member-of`, `issued-by`, `depends-on`).
- **Facet** — a named, schema'd fragment of an Entity's document (`net.ipv4`, `os.kernel`, `cert.expiry`, `apps.installed`, `mgmt.channels`). Facets are where typing hardens progressively; schemas attach here and nowhere else.
- **Facet ownership registry** — every Facet namespace has a declared write owner (a Syncer, a Blueprint output, or a team) scoped by View. Two writers to one namespace is a registration error. Provenance is a lineage, never a fight.
- **Provenance** — per-attribute stamp: which Run/Syncer wrote it, when, from which Source. Non-optional; it is the audit story and the "why is this value here" answer — which by construction always has exactly one answer.
- **View** — a saved, **versioned, CaC-declared** graph query producing a live Entity set. Unifies inventory, smart/constructed inventory, Jamf Smart Groups, SCCM collections. Views referenced by Assignments cannot be UI-edited — Git only, because a View edit is a blast-radius change (§5.4).

### 2.2 Sources & Connectors
- **Source** — an external system of record, registered with CredentialRefs and trust settings.
- **Connector** — the versioned integration package for a Source; one noun, shipping some combination of three capability types:
  - **Syncer** — projection: bulk enumeration + delta ingestion → **Normalizer** → Entities/Facets/Relations with provenance. Requires full-fidelity transports (native REST/gRPC). MCP is not an admissible Syncer transport until the spec has real pagination/change-feed semantics.
  - **Action** — one typed operation (`create-vm`, `assign-policy`, `revoke-cert`): input Contract + output Contract + idempotency/dry-run declaration.
  - **Emitter** — event producer (webhook receiver, poller, stream subscriber) publishing typed events to the bus.
- **Trust tiers** — `core` (in-tree) / `verified` (reviewed, signed) / `community` (signed, sandboxed defaults). Applies to Connectors, Actuators, and Blueprints alike.

### 2.3 Execution plane
- **Actuator** — an execution-engine plugin that runs *tool content*: `ansible` (ansible-runner subprocess), `opentofu`, `script`, `helm`, `mcp` (generic MCP-client), future `packer`. Actuators interpret content and produce many effects; Actions are single contracted calls. This distinction is deliberate and permanent — their contract, drift-check, and sandboxing semantics differ.
- **Contract** — JSON Schema on any Step's inputs/outputs. Derivation ladder: hand-written (core) > tool-derived (tofu plan JSON, OpenAPI import) > MCP-declared-and-pinned. Only the top rung is admissible for Syncers; all rungs admissible for Actions/Actuators.
- **Step** — one contracted invocation: (Actuator + content ref + params) or (Action + params), with input bindings (Views, prior Step outputs, literals) and output declarations.
- **Workflow** — Temporal-backed DAG of Steps with success/failure/always edges, **Gates** (human/policy approval), convergence, nesting. The seam feature: a tofu Step's `outputs.instances[*]` binds directly into the next Step's View parameters — provision→configure in one graph, one RBAC model, one audit stream.
- **Run** — execution instance: status, per-target results, event stream, artifacts, cost/usage, provenance written.
- **Trigger** — anything that starts a Run: Temporal Schedule, Emitter event × CEL rule, manual, API/MCP. Cron is just one Emitter.
- **Bundle** — cosign-signed OCI artifact of content + deps for pull-mode agents. **Site** — remote execution locus (satellite dispatcher + NATS leaf).

### 2.4 Intent layer (the team-facing surface)
- **Intent** — a small declarative document of *what*, by payload kind: `Intent/Application`, `Intent/Certificate`, `Intent/FileSet`, `Intent/Access`, `Intent/Config`, extensible. Each kind has a schema (→ generated forms/validation). Every Intent kind carries a lifecycle field: **`onRemove: retain | revert | remove`** (default `retain`), and withdrawn-but-retained state always raises an orphan Finding — abandoned state is never silent, even when deliberate. Domain-specific removal semantics live in the schema (a Certificate's `remove` may mean revoke vs. let-expire), never in tribal memory.
- **Assignment** — binds an Intent to a View, per environment/ring, optionally behind a Gate. Kept separate from Intent so one Intent can be assigned differently across environments and patch rings.
- **Blueprint** — platform- or domain-owned composition that compiles (Intent × Assignment × View membership) into Baselines + remediation Workflows, **routed by capability-scoped Facets**. Routing keys are per-capability maps, never scalars — `mgmt.channels: {apps: intune, certs: ansible, files: ansible}` — because co-management is reality, not an edge case. Blueprints are versioned; Assignments pin a Blueprint version; Blueprint upgrades roll through rings with compile-diffs, with the same rigor as any change, because they are one. Blueprint authorship follows trust tiers with delegation bound to the facet-ownership registry (the macOS domain team ships and owns the Jamf routes) — the platform team is a steward, not a chokepoint.
- **Claim types** — every Facet a compiled Baseline manages is claimed as either:
  - **exclusive** — one Assignment may claim it per Entity; a double-claim is a compile error, and the ownership registry governs eligibility; or
  - **additive** — set-union semantics (`ensure contains` vs `ensure exactly`) with per-element provenance, for naturally additive state (local admin groups, sudoers, trust stores).
  There is no implicit precedence anywhere in the model. This is the anti-GPO axiom.
- **Baseline** — compiled (or hand-written, §6 ladder) desired state: View selector + expected Facet values and/or check Step + remediation Workflow ref + cadence.
- **Finding** — a drift/compliance/orphan result: Entity + Baseline + observed-vs-expected diff + severity + Evidence ref. One kind, framework-tagged.
- **Evidence** — immutable (object-locked) artifact bundle backing a Finding; the audit/PCI export unit.

### 2.5 Identity
- **Principal** — human or service/agent identity, one kind: agents launching Workflows via MCP live in the same authz, audit, and cost model as humans.
- **CredentialRef** — pointer + injection policy to brokered secrets (Vault/ESO/K8s/CyberArk). Material never persists in the platform; injected only into execution pods at spawn. `use-without-read` is a first-class grant.
- **Authorization** — ReBAC via OpenFGA: org → team → Principal, object-level grants, View-scoped execution ("may run this Workflow, but only against Entities in this View"). Platform RBAC is itself CaC (OpenFGA tuple manifests in Git).

---

## 3. Architecture — Five Planes

```
┌─ Interface ──────────────────────────────────────────────────────┐
│  Web UI · CLI (`stratt`) · REST/OpenAPI · MCP server             │
│  one API, one Principal model, one audit stream                  │
├─ Orchestration ──────────────────────────────────────────────────┤
│  Temporal: Workflows, Gates, Schedules, Baseline cadences        │
│  Trigger engine: NATS events × CEL → Workflow launches           │
├─ Actuation ──────────────────────────────────────────────────────┤
│  Dispatcher → K8s Jobs (tool/EE images) + event-shipper sidecars │
│  Actuators: ansible │ opentofu │ script │ helm │ mcp │ …         │
│  Action calls sandboxed identically · Sites via NATS leaf        │
│  Pull mode: stratt-agent (Go) + signed Bundles (OCI)             │
├─ Graph ──────────────────────────────────────────────────────────┤
│  Postgres 18: Entities/Relations/Facets (JSONB, versioned),      │
│  Views, Baselines, Findings, Runs, Provenance, ownership registry│
│  Syncers → Normalizers → projections · Git → desired state       │
│  Compiler: Intent × Assignment × Blueprint → Baselines/Workflows │
├─ Substrate ──────────────────────────────────────────────────────┤
│  Postgres │ NATS JetStream │ Temporal │ S3-compatible │ Loki │OTel│
│  OIDC (Zitadel-compatible) + SCIM │ OpenFGA │ cosign/SLSA/SBOM   │
└──────────────────────────────────────────────────────────────────┘
```

**Commitments and rationales:**
- **Postgres, not a graph DB.** Relational + JSONB + recursive CTEs + GIN facet indexes cover estate scale (10⁵–10⁶ Entities) with boring ops; revisit only on measured pain.
- **Temporal owns all lifecycle** — job runs, workflow DAGs, approvals, schedules, Baseline cadences, drift scans. Deletes the entire AWX dispatcher/callback pathology.
- **NATS JetStream** — run event streaming, Emitter ingestion, leaf-node Sites (the Receptor replacement), pull-agent transport.
- **K8s Jobs are the only execution primitive.** Ephemeral, network-policied, secret-injected pods; org-namespace multi-tenancy; Kyverno policy set shipped. Log lines → Loki; artifacts/facts → S3; Postgres stores summaries only (AWX's job-events-table pathology, eliminated).
- **Ansible as subprocess only** (GPLv3 boundary). The Go control plane never links Ansible; it shells out to the `ansible-runner` shim inside the EE image — the boundary is structural (a separate process in a separate image), enforced in CI, not by convention. **OpenTofu preferred over Terraform** (license + native state encryption). Platform implements the tofu HTTP state backend with encryption-at-rest and OpenFGA-guarded reads; state files are secrets-bearing artifacts.
- **ansible-builder EE compatibility preserved** — day-one content ecosystem.
- **Object storage is any S3-compatible store**, never a single named vendor. Reference implementations: Garage, SeaweedFS, or a cloud S3. This is the boring-spine (§1.4) and governance (§7.2) discipline applied to storage — MinIO's single-vendor licensing posture is exactly the dependency risk the charter warns against.
- **Control plane: Go.** The control plane is reconciliation controllers (sync controller, dispatcher, compiler cadences), a graph-store frontend, and a K8s-native operator posture — the exact domain where client-go/controller-runtime run deepest and where NATS, Temporal, and OpenFGA all ship first-class native SDKs. Go gives one language shared with the `stratt-agent` pull agent (not two), best-in-class toolchain evergreen (§1.7), single static binaries, and headroom for View queries / compile passes at 10⁶ Entities. Decisively, it is a **contributor-demographics decision** (§7.2): the Argo/Crossplane/NATS maintainers this CNCF-track platform courts write Go. Decoupled controllers, never a monolith — the coupling Red Hat named as the cause of AWX's death. **API is OpenAPI-first** (huma / oapi-codegen); the `/api/v2` façade is REST regardless and curl-ability matters for adoption. **Contracts and Facet schemas are data** — pinned, hash-verified JSON Schema documents (some tool-derived from tofu plans or MCP declarations), validated by a standard JSON Schema validator, never language classes (§1.5, §2.2). **Python survives where it earns its keep:** inside execution pods (the ansible-runner shim in the EE image) and as one supported plugin-SDK language, so Ansible-community contributors are not excluded.

### 3.1 Frontend
- **React + TypeScript + Vite, TanStack Router/Query.** Virtualized live log viewer; graph exploration, Run streaming, and Findings are the center-of-gravity screens.
- **Component strategy — vendored, not depended-upon:** headless accessible primitives (Radix today; Base UI on the succession watchlist given Radix's slowed maintenance) with shadcn-style copy-in components owned in-repo. Swapping the primitive layer is a refactor, not a rewrite, because nothing external owns our components. Tailwind for styling (build-time only, no runtime lock-in).
- **Design tokens as data:** all theming via CSS variables; no hardcoded styling in components.
- **Schema-driven rendering is the extensibility mechanism:** Intent forms, Step inputs, Finding tables, and plan diffs are generated from JSON Schema (Contracts). Plugins extend the UI by shipping *schemas, not React code* — every Connector gets a working UI for free, and community code never executes in the interface plane (supply-chain surface eliminated by design).
- **Real-time:** SSE (NATS-backed) for job output, host matrices, live Workflow DAGs.
- Evergreen contract (§1.7) applies fully: Node current-or-previous LTS, framework majors ≤ N-1, CI-gated.

---

## 4. Configuration as Code — the Full Shape

### 4.1 Repo topology (App-of-Apps, generalized)
- **Central registry repo** (platform stewards): Blueprint definitions + versions, Connector/Actuator registrations + trust tiers, facet-ownership registry, team enrollments (one entry per team → repo pointer + scope), and **admission policies on manifests themselves** — Kyverno-for-config: "no `exportable: true` cert Intents," "prod Assignments require a Gate," "team X may only target Views under org X."
- **Team repos**: Views (theirs), Intents, Assignments, and (if granted by tier) team-scoped Blueprints, Baselines, Workflows.
- **Sync controller** reconciles both into Postgres (Argo-compatible; CRD interface later as UX, Postgres remains truth).
- **Layering is deliberately dumb:** org defaults → team → environment overlay directories; explicit Kustomize-style overrides only; conflicts resolve per claim type (§2.4) — exclusive double-claims fail compile, additive claims union. No inheritance, no last-writer-wins, ever.

### 4.2 The worked example (canonical; used in docs and demos)
```yaml
# team repo: intents/chrome.yaml
kind: Intent/Application
name: chrome
spec: { package: google-chrome, channel: stable }
onRemove: revert
---
# team repo: assignments/kiosks.yaml
kind: Assignment
intent: chrome
to: view://retail/kiosk-devices      # versioned View, Git-declared
environments: [prod]
gate: change-window
blueprint: application@v3            # pinned
```
```yaml
# central registry: blueprints/application/v3.yaml
kind: Blueprint
for: Intent/Application
routes:
  - match: { capability: apps, facet: mgmt.channels.apps, eq: ansible, facet2: os.family, eq: windows }
    remediate: { actuator: ansible, role: winget-install }
  - match: { capability: apps, facet: mgmt.channels.apps, eq: jamf }
    remediate: { action: jamf/install-policy }
  - match: { capability: apps, facet: mgmt.channels.apps, eq: intune }
    remediate: { action: msgraph/assign-app }
observe: { facet: apps.installed, claim: additive, contains: "{{.package}}" }
```
The compiler emits Baselines + remediation Workflows; drift dashboards, Findings, Gates, and provenance fall out of the existing spine. The same shape carries `Intent/Certificate` (issuer ref to CLM connector; `exportable: false` as schema, not policy memo), `Intent/FileSet` (content-addressed OCI artifacts, checksum Facets), `Intent/Access` (additive claims, per-element provenance).

### 4.3 Safety machinery (from pressure testing; all mandatory)
- **`stratt plan` renders membership deltas, not just config diffs** — which Entities join/leave the compiled target set, per Assignment, pre-merge.
- **Max-delta gate:** if a Baseline's compiled target set changes more than a configured fraction between reconciles (Syncer relabel, View edit), execution pauses pending approval. The platform-level PodDisruptionBudget; the cheapest catastrophic-blast-radius insurance in the design.
- **Flap damping:** post-remediation, the Run writes Facets directly (with Run provenance) rather than waiting on Syncer lag; Findings require N consecutive drifted observations before firing.
- **Orphan Findings** for all withdrawn-but-retained state (§2.4 lifecycle).
- **Compile-diff on Blueprint version bumps**, rolled through rings.

---

## 5. Canonical Flows
1. **Provision→configure:** tofu apply (Gate on plan) → outputs Contract → Normalizer projects Entities → ansible Step against `view://label:run=X` → facts return as Facets → CMDB connector pushes projection to ServiceNow. One provenance chain, zero glue scripts.
2. **Tool-agnostic drift:** check-mode Baseline (servers) and `tofu plan` Baseline (network modules) produce Findings on one dashboard; remediation behind Gates. `tofu plan` on cron *is* drift detection — no special case.
3. **Event-driven:** Falco Emitter → CEL rule with graph lookup → quarantine Workflow (ansible Step + Jamf Action). Cert-expiry Facet threshold → renewal Workflow (the Ansible CLM controller model, landed natively).
4. **Cross-domain patch ring:** Views per ring spanning servers + endpoints; Workflow fans out through per-capability channels (ansible / Graph Actions / Jamf Actions); success-rate Gate between rings; maintenance-window guard from a calendar Facet.
5. **Agent-operated:** an agent Principal queries Findings via MCP, proposes remediation, hits a human Gate, executes — audited and cost-accounted identically to a human.
6. **AWX exodus:** importer (an AWX Syncer + transform — fittingly) maps templates→Step presets, inventories→Views, workflows→Workflows; `/api/v2` façade keeps existing tooling alive during cutover. The import target is frozen at 24.6.1 forever — the friendliest migration in software.

---

## 6. The Onboarding / Power Gradient
Day one, a new team ships ~15 lines: reuse a View, one Intent, one Assignment; `stratt plan` shows compiled effect (devices, mechanisms, changes) before merge. As needs grow, teams descend rungs that all remain first-class and diagnosable (§1.8):

**Intent → (granted) team Blueprint → raw Baseline → raw Workflow/Step.**

Progressive disclosure, never a ceiling. The quiet payoff: one `Intent/Application` on a View spanning servers, Macs, and Intune laptops fans out through three management channels with one drift dashboard — cross-domain consolidation in a document a junior engineer can read.

---

## 7. Positioning & Strategy

### 7.1 Landscape
| | AAP | AWX | Semaphore | TACOS | System Initiative | ServiceNow | **Stratt** |
|---|---|---|---|---|---|---|---|
| Config mgmt | ✅ | frozen | ✅ | ❌ | partial | ❌ | ✅ |
| Provisioning | ❌ | ❌ | basic | ✅ | ✅ | ❌ | ✅ |
| Estate graph | ❌ | ❌ | ❌ | ❌ | ✅ core bet | ✅ passive | ✅ projection |
| Typed seams | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ✅ Contracts |
| Intent layer / routing | ❌ | ❌ | ❌ | ❌ | partial | catalog only | ✅ Blueprints |
| Cross-tool drift | ❌ | ❌ | ❌ | tofu only | ✅ | observe | ✅ |
| Endpoint/MDM bridge | ❌ | ❌ | ❌ | ❌ | ❌ | partial | ✅ |
| Event-driven | add-on | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ native |
| Any-K8s self-host | ❌ OCP | frozen | VM-first | SaaS | SaaS | SaaS | ✅ |
| Open, no tier, DCO | ❌ | frozen | open-core | ❌ | ❌ | ❌ | ✅ |

### 7.2 Governance (the moat)
Apache-2.0 all; DCO; public-everything from first tag; maintainer ladder with ≥2 external maintainers pre-v1.0; foundation donation (CNCF Sandbox) with trademark transfer as terminal rug-pull-proofing, intent stated publicly early. Sustainability without sales: plugin surfaces carry breadth (the Ansible-Galaxy scaling model), boring substrate, SemVer with N-1 upgrade tested every release, Sponsors/OpenCollective for infra; third-party support businesses welcome (the Postgres model).

### 7.3 Supply chain
Signed releases (cosign), SBOM, SLSA provenance from release one; SECURITY.md + disclosure process; pinned-digest images; community-tier plugins sandboxed by default; MCP outputs screened for tool-description injection wherever LLM-adjacent.

### 7.4 Prerequisite: employer clearance
Written OSPO/legal clarity on IP (personal vs. sponsored) *before* first public push; strictly personal time/equipment until obtained. Highest-severity, cheapest-fix risk in the charter.

### 7.5 Non-goals restated for the community
No MDM protocol, no imaging, no new config language, no writable CMDB, no paid tier. Saying no in public, kindly, is a sustainability feature.

### 7.6 The strangler-fig posture (consolidation thesis)
An abstraction over legacy sprawl risks becoming a mausoleum for it — hide six overlapping systems well enough and the pressure to retire them disappears. Stratt's answer is accounting: every Intent routes through an explicit Blueprint route with per-route cost, latency, and failure-rate metrics. "Certs via channel A cost 4× and fail 3× vs channel B" becomes a dashboard, not an opinion. The front end holds intent stable while the accounting builds the decommissioning case; retiring a backend is a Blueprint route edit no Intent author ever notices. The abstraction amortizes debt rather than hiding it — and the same per-Principal accounting extends naturally to agent-initiated automation (token cost as a first-class operational metric).

---

## 8. Roadmap
(2–4 engineers, heavily AI-assisted; parity features lead every phase so the platform is *useful* before it is *novel*.)

**Phase 0 — Spike (3–5 wks).** The thesis slice: Entity/Facet/Provenance store → one native Syncer (vCenter-class) → View query → Temporal Workflow → K8s Job (ansible-runner) against the View → facts projected back with provenance → live SSE tail. **Go/no-go:** projection freshness; pod-spawn p95 < 5 s; View query < 200 ms @ 50k Entities. If the graph spine doesn't prove out, the project retreats to job-runner scope — explicitly, at this gate, not by drift.

**Phase 1 — Usable core (mo. 1–4).** Ansible Actuator complete (EEs, per-target results, slicing), `script` Actuator, Git desired-state sync + `stratt apply`/`plan`, Views UI, Workflows + Gates, Schedules, CredentialRefs (Vault + K8s), OIDC + basic OpenFGA, Helm chart, MS Graph + cloud-instance Syncers. **Promote:** Nebulae daily-driver, 30 days zero data loss. **OSS gate:** OSPO clearance; repo public with DCO/ADRs/10-minute quickstart before Phase 2.

**Phase 2 — Seams + intent layer (mo. 4–7).** OpenTofu Actuator (plan/apply Gates, HTTP state backend w/ encryption, output-derived Contracts); Trigger engine (webhook + Alertmanager Emitters, CEL); **Intent/Assignment/Blueprint compiler with claim types, ownership registry, membership-delta plan, max-delta gate**; Baselines + Findings v1 (check-mode + tofu plan) with flap damping; MCP actuator/Action adapter + platform MCP server; AWX importer + `/api/v2` façade; notifications. **Promote:** shadow AAP on non-prod; one team migrated green 2 wks.

**Phase 3 — Enterprise + fleet (mo. 7–11).** Sites (NATS leaf); full OpenFGA (View-scoped execution, use-without-read); HA + DR runbook; audit→Splunk sink; SCIM; pull agent + Bundles; Evidence store (object-lock) + CIS pack (ansible-lockdown/OpenSCAP); Jamf + ConfigMgr AdminService Connectors; `Intent/Certificate` + `Intent/FileSet` + `Intent/Access` kinds GA. **Promote:** production for a bounded service class (certificate lifecycle as first tenant); 99.9 % 30-day SLO; security review. **OSS gate:** v1.0; ≥2 external maintainers; ≥3 community plugins; CNCF Sandbox application; vocabulary freeze.

**Phase 4 — Consolidation (11+).** Cross-domain patch rings/maintenance windows; self-service portal (Contracts→forms, generated); per-route and per-Principal cost analytics (§7.6); Helm/Packer Actuators; ServiceNow push connector; CRD interface; verified-plugin registry; ACP-addressable actuation for agent ecosystems.

**Environment promotion cadence:** trunk-based; merge→dev auto-sync; weekly staging cut with N-1 upgrade test; prod promotes gated on soak + clean desired-state diff; the platform's own config promotes through the same Git overlay flow. The evergreen upgrade train (§1.7) runs quarterly and is a release-blocking checklist item.

---

## 9. Risks
| Risk | Mitigation |
|---|---|
| Ontology creep (the category's graveyard) | Facet-level schemas only; every schema demanded by a shipping Contract; "no whole-Entity schemas" enforced in review. |
| Graph becomes a second truth | Rebuildable projections; only Normalizers and Run provenance may write Entity attributes — data-layer-enforced. |
| Blueprint layer becomes the new ticket queue | Trust-tiered, delegated Blueprint authorship bound to facet ownership; platform team stewards, never gatekeeps. |
| Blast radius via live Views / Syncer relabels | Versioned CaC Views; membership-delta plans; max-delta execution gates. |
| Remediation flapping | Direct post-run Facet writes; N-observation Finding damping. |
| Withdrawal ambiguity | `onRemove` lifecycle on every Intent kind; orphan Findings always. |
| Abstraction hides failure → users route around it | Discipline §1.8; one-click descent Intent→Run→task event; diagnosis is a product surface. |
| Mausoleum effect (debt preserved under the abstraction) | §7.6 route accounting; decommissioning as a measured, routine Blueprint edit. |
| System Initiative convergence | Different bets: seams vs. full fidelity, self-host vs. SaaS, foundation vs. vendor. Governance can't be copied by a venture-backed company. |
| MCP spec shifts | One transport behind sovereign contracts; Syncer promotion gated on spec maturity; schemas pinned, drift blocks. |
| Plugin quality variance | Trust tiers, signing, sandbox defaults, blocking contract drift. |
| Dependency fossilization | Discipline §1.7, CI-enforced; quarterly train; upgrade-record as adoption criterion. |
| Scope (this is three products) | Phase-0 go/no-go; parity-first phasing; non-goals in the charter. |
| Employer IP | §7.4, resolved before first push. |
| Red Hat unfreezes AWX-next | Orthogonal thesis (graph + intent layer); compat façade hedges both directions. |
| Community never materializes | Plugin surfaces lower contribution cost; stranded AWX users are a motivated audience; frictionless quickstart; docs as product. Floor case: best-in-class internal platform — still the goal. |
| Support burden, no revenue | Issue templates, repro-required bugs, boring ops story, public kind "no." |

---

## 10. Naming
"Stratt" (the coordinator) fits the coordination-layer product; trademark/collision check before public repo. The §2 vocabulary matters more than the brand: frozen at v1.0, deprecation policy thereafter — renaming kinds after a community exists is its own kind of rug-pull.
