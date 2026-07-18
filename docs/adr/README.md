# Architecture Decision Records

Every decision of consequence is captured here (charter workflow; run **`/new-adr`** to add one from
[0000-template.md](0000-template.md)). ADRs are immutable once Accepted — supersede, don't rewrite. The
**[charter](../../stratt-charter.md) supersedes every ADR**; where an ADR touches §1/§2 it carries a
charter-guardian review.

Status is `Accepted` unless noted. See **[../roadmap.md](../roadmap.md)** for how these map to phases.

| # | Title | Status |
|---|---|---|
| [0001](0001-charter-as-design-authority-and-claude-control-plane.md) | Charter as design authority; Claude Code as the initial build surface | Accepted |
| [0002](0002-go-control-plane-python-in-pods-s3-generic-storage.md) | Go control plane; Python confined to pods & SDK; S3-generic storage | Accepted |
| [0003](0003-ux-design-principles-schema-driven-rendering-and-descent.md) | UX design principles: schema-driven rendering and one-click descent | Proposed |
| [0004](0004-db-tooling-pgx-goose.md) | Postgres tooling: pgx queries, goose migrations | Accepted |
| [0005](0005-phase0-monorepo-layout-multi-module-workspace.md) | Phase-0 monorepo layout: multi-module Go workspace | Accepted |
| [0006](0006-openapi-tooling-oapi-codegen.md) | OpenAPI tooling: spec-first with oapi-codegen | Accepted |
| [0007](0007-phase0-syncer-sdk-and-dev-harness.md) | Phase-0 Syncer SDK and dev/test harness: govmomi + vcsim, kind | Accepted |
| [0008](0008-phase0-go-no-go-measurements.md) | Phase-0 go/no-go gate measurements: graph spine proves out | Accepted |
| [0009](0009-identity-authz-credential-brokering.md) | Identity, authorization, and credential brokering | Accepted |
| [0010](0010-triggers-v1-schedules.md) | Triggers v1: the `schedule` kind on Temporal Schedules | Accepted |
| [0011](0011-workflows-gates-v1.md) | Workflows + Gates v1: Step DAGs with human approval | Accepted |
| [0012](0012-views-ui-v1.md) | Views UI v1: React shell, OIDC login, descent screens | Accepted |
| [0013](0013-helm-packaging.md) | Helm packaging: any-Kubernetes self-host | Accepted |
| [0014](0014-connector-breadth-msgraph-ec2.md) | Connector breadth: MS Graph and EC2 cloud-instance Syncers | Accepted |
| [0015](0015-contracts-v1.md) | Contracts v1: pinned, hash-verified JSON Schema at the seams | Accepted |
| [0016](0016-opentofu-actuator.md) | OpenTofu Actuator: plan/apply behind Gates, encrypted HTTP state backend | Accepted |
| [0017](0017-tofu-outputs-to-entities.md) | tofu outputs → Entities: the provision→configure seam | Accepted |
| [0018](0018-trigger-engine.md) | Trigger engine: Emitters × CEL → launches | Accepted |
| [0019](0019-baselines-findings-v1.md) | Baselines + Findings v1: check-mode + tofu plan, flap damping | Accepted |
| [0020](0020-findings-ui.md) | Findings UI: the estate drift screen | Accepted |
| [0021](0021-platform-mcp-server.md) | Platform MCP server: the agent surface | Accepted |
| [0022](0022-mcp-actuator.md) | mcp Actuator: consuming external MCP servers | Accepted |
| [0023](0023-intent-compiler.md) | Intent/Assignment/Blueprint compiler | Accepted |
| [0024](0024-templating-parametrized-views.md) | Payload templating + parametrized Views | Accepted |
| [0025](0025-awx-importer-and-ansible-scm-content-ref.md) | AWX importer + ansible SCM content-ref | Accepted |
| [0026](0026-awx-api-v2-facade.md) | AWX-compatible `/api/v2` façade (+ native cancel & extraVars) | Accepted |
| [0027](0027-notifications.md) | Notifications (outbound Run/Finding/Gate alerts) | Accepted |
| [0028](0028-view-scoped-execution-authz.md) | View-scoped execution authz (full OpenFGA) | Accepted |
| [0029](0029-evidence-store-object-lock.md) | Evidence store (object-locked audit bundles) | Accepted |
| [0030](0030-intent-certificate-ga.md) | `Intent/Certificate` GA (certificate lifecycle as first tenant) | Accepted |
| [0031](0031-action-execution-framework.md) | Action-execution framework (+ provision→configure seam) | Accepted |
| [0032](0032-sites-remote-execution-loci.md) | Sites: remote execution loci (NATS-leaf push; cosign/OCI pull Bundles) | Accepted |
| [0033](0033-cis-pack-compliance-as-data.md) | CIS pack: compliance frameworks as data over a reusable projection | Accepted |
| [0034](0034-audit-stream-and-siem-forwarder.md) | The one audit stream + vendor-neutral SIEM forwarder | Accepted |
| [0035](0035-scim-service-provider.md) | SCIM 2.0 Service Provider: IdP-driven Principal lifecycle + group→team authz | Accepted |
| [0036](0036-intent-fileset-access-ga.md) | `Intent/FileSet` + `Intent/Access` GA (file distribution + host-access governance) | Accepted |
| [0037](0037-chef-infra-server-syncer.md) | Chef Infra Server node-API Syncer (first config-mgmt SoR ingest) | Accepted |
| [0038](0038-openvox-puppetdb-syncer.md) | OpenVox/PuppetDB node Syncer + source-scoped config-mgmt facets | Accepted |
| [0039](0039-salt-connector-syncer-and-emitter.md) | Salt Connector: grains Syncer + event-bus Emitter | Accepted |
| [0040](0040-high-availability-and-disaster-recovery.md) | High Availability & Disaster Recovery architecture | Accepted |
| [0041](0041-per-key-entity-label-ownership.md) | Per-key Entity-label ownership (the label mirror of facet_owner) | Accepted |
| [0042](0042-cross-source-entity-liveness.md) | Cross-source Entity liveness (per-Source presence) + observedBy | Accepted |
| [0043](0043-cert-renewal-finding-gc.md) | Cert-renewal Finding-GC (resolve Findings for tombstoned Entities) | Accepted |
| [0044](0044-control-plane-cells.md) | Control-plane Cells / multi-region (partitioned single-writer, one logical estate) | Accepted |
| [0045](0045-db-driven-syncer-home-gate.md) | DB-driven Syncer instantiation & Connector home-ownership gate (full re-home auto-cutover) | Proposed |
| [0046](0046-stratt-as-substrate.md) | Stratt as Substrate: the dark-matter re-centering and the sovereign plugin port | Accepted |
| [0047](0047-plugin-port-v1-full-surface.md) | Plugin port v1 full surface: write-back, relations, the rung ladder, and USB-style growth | Accepted |
| [0048](0048-integration-taxonomy-plugin-tool-transport.md) | Integration taxonomy: connector (plugin) vs migration (tool) vs transport (core port) | Accepted |
| [0049](0049-sites-over-the-plugin-port.md) | Sites over the plugin port: the agent as an authenticated transport relay, never a governor | Accepted |
| [0050](0050-certificate-reconcile-actuator.md) | Certificate lifecycle as a reconcile Actuator (CSR/sign over the port) | Accepted |
| [0051](0051-ee-job-speaks-the-port.md) | The EE Job speaks the port: a subprocess transport, one governor (ansible extraction) | Accepted |
| [0052](0052-secretbroker-port.md) | The SecretBroker port: per-call credential resolution for plugins (§2.5) | Accepted |
| [0053](0053-mcp-transport-generic-connector.md) | MCP as a generic transport: the last domain logic leaves the core | Accepted |
| [0054](0054-per-step-facet-claim.md) | Per-Step facet write-scope: narrow the write-back grant to what a Step declares | Accepted |
| [0055](0055-estate-composition.md) | Estate Composition: what it means to "define the estate" (north-star model + guardrails + gap roadmap) | Accepted |
| [0056](0056-estate-as-code.md) | Estate-as-Code: declaring Sources & Connectors in Git + the `stratt` estate CLI | Accepted |
| [0057](0057-environment-scoped-reconciliation.md) | Environment-scoped reconciliation: one estate repo, N environments | Accepted |
| [0058](0058-provisioning-from-intent.md) | Provisioning from Intent: declare desired infrastructure → gated build → project back (G1/G4) | Accepted |
| [0059](0059-network-topology-primitives.md) | Network & topology primitives: subnet/dmz/az/dns kinds + placement as a Relation | Accepted |
