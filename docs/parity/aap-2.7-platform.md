# AAP 2.7 parity — what we have, what we need

**Target:** Red Hat **Ansible Automation Platform 2.7** (GA 2026-05-28) as the feature bar for the
"structurally-open successor" thesis (charter §0, §7.1). This is the **full platform** — six
components, not just the AWX job-runner — plus the 2.7-specific deltas.

**Framing (charter §8):** _"parity features lead every phase so the platform is useful before it is
novel."_ Parity is the **floor**; the estate-graph + typed-seams + intent layer is the **differentiator**
AAP structurally lacks (charter §7.1: estate graph ❌, typed seams ❌, intent/routing ❌, cross-tool
drift ❌, provisioning ❌, any-K8s ❌ — all ✅ for Stratt). This doc tracks the floor.

> Evidence base: a 5-way codebase inventory (2026-07-19) cross-referenced against the AAP 2.7
> release notes. Component verdicts below are grounded in files + ADRs, not aspiration.

**Level, and its sibling audits.** This doc scores AAP by **component and capability**. Two
finer-grained audits sit beside it in [this folder](README.md) and refine — in places sharply — what its
rows mean:
[**awx-object-model.md**](awx-object-model.md) takes the Automation Controller row down to objects and
fields, and finds the projection much thinner than 🟢 code-complete suggests at this altitude (the
capability ships; the graph's view of it is 3–5 fields per object). [**ansible-tool.md**](ansible-tool.md)
audits Ansible itself and surfaces the reach gap none of these rows expose — the Actuator speaks SSH with
a key and nothing else, so Windows and network devices are unsupported. Read a 🟢 here as "the capability
exists," never as "the depth is audited."

## Scorecard

| AAP 2.7 component                              | Verdict                                    | One-line                                                                                                                              |
| ---------------------------------------------- | ------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------- |
| **Automation Controller** (job runner)         | 🟢 **at parity** (2026-08-03)              | Every core capability shipped. The gap this row named — `/api/v2` route breadth — closed 2026-07-31; **launch semantics** closed by [ADR-0160](../adr/0160-the-same-job-possibly-a-different-hand.md) (all ~16 `ask_*_on_launch`, derived not hardcoded; a View, a credential subset and an EE selectable within declared permitted sets), **cancellation** by [ADR-0157](../adr/0157-cancelling-a-workflow-run.md), and **inventory groups + `group_vars`** by [ADR-0161](../adr/0161-the-graph-is-the-inventory-and-it-has-no-groups.md), which is what lets a migrated playbook targeting `hosts: webservers` run unmodified. Remaining edge: custom credential-type **injectors** are a fixed map, not a user-definable DSL. |
| **Policy-as-code** (OPA gate, 2.6+)            | 🟢 **ahead**                               | 4-valued lattice + typed Control library + dual PEPs + obligations vs AAP's thin OPA allow/deny                                       |
| **Platform Gateway** (unified UI/API/RBAC/SSO) | 🟢 **code-complete core**, 🟡 UI/analytics | Unified UI, OIDC, OpenFGA, SCIM, one Principal, one audit stream, platform MCP; gaps are analytics/org/admin UI                       |
| **Automation Mesh** (distributed exec)         | 🟢 **code-complete**, one gap              | Sites (push+pull) + signed Bundles + Cells (a partitioning story AAP lacks); gap = multi-hop relay nodes                              |
| **Event-Driven Ansible** (rulebooks)           | 🟢 **task parity** — two mechanisms declined | Ingest→CEL→launch+dedup, plus patterns over events (ADR-0162: `count`/`within`, `allOf`/`correlateBy`) and durable cross-replica throttling. Declined by decision, not missing: a `set_fact` working memory (§1.2) and the rulebook FILE format |
| **Automation Hub** (content/EE/supply-chain)   | 🟡 **partial** — the registry half remains | EE-build factory ships (ADR-0124/0170: an AAP `execution-environment.yml` builds, with byte-pinned content ansible-builder does not do). Signing/SBOM/SLSA ship (ADR-0165), with digest enforcement at the chart (ADR-0168) and the dispatcher (ADR-0169). **Gap: no content registry / catalog / version resolution, and nothing published yet.** |

**Bottom line (rewritten 2026-08-04 — every clause of the previous one had gone stale):** the
AWX-successor **job-runner + governance + distributed-execution + identity** surface is built and in
places ahead. Of the four things this line used to name as the remaining work:

1. **Hub-class content/supply-chain** — the EE-build and signing halves ship (ADR-0124/0170,
   ADR-0165/0168/0169). What remains is a **content registry**: no catalog, no discovery, no version
   resolution — and nothing published yet, which is a decision rather than a coding task.
2. **EDA rulebook depth** — closed. Patterns over events, durable cross-replica throttling, declared
   payload shapes and signed sources all ship (ADR-0162/0163/0164/0167); the rulebook FILE format and
   a `set_fact` working memory are **declined by decision**, not missing.
3. **`/api/v2` + notification breadth** — route breadth closed 2026-07-31, launch semantics by
   ADR-0160, sink drivers by ADR-0125.
4. **The live-cluster proof** — shipped as E2E-1: the whole demo library runs against a real cluster
   and its exit code is the gate.

**The honest remaining list is now short and specific**: a content registry, an EE distribution
service, multi-hop mesh relay, in-cluster admission verification, and analytics/org-admin UI.

---

## Per-component detail

### 1. Automation Controller — 🟢 code-complete core

The AWX object model is deliberately collapsed onto Named Kinds (charter §2 mapping): job template →
Step preset / single-Step Workflow, inventory/smart/constructed → **View**, survey → input Contract,
job → **Run**, credential → **CredentialRef**, project → SCM content-ref. AWX-shaped objects exist only
in the `awxfacade` wire layer.

| Capability                                | Status | Evidence                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| ----------------------------------------- | ------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Job Templates                             | 🟢     | [orchestrate/](../../core/internal/orchestrate/), ansible Actuator [plugins/ansible/](../../plugins/ansible/), ADR-0051                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| Workflow DAG + approval nodes             | 🟢     | [orchestrate/workflow.go](../../core/internal/orchestrate/workflow.go) (`RunDAG`, gates), ADR-0011                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| Projects (SCM content)                    | 🟢     | in-EE git clone [plugins/ansible/shim.go](../../plugins/ansible/shim.go), ADR-0025                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| Inventories (+ dynamic/smart/constructed) | 🟢     | Views [graph/reader.go](../../core/internal/graph/reader.go) + Syncers; façade `viewToInventory`, ADR-0012/0024. Host connection vars are the closed `mgmt.address` coordinate — `ansible_host` **and `ansible_port`** (ADR-0084, completed by ADR-0117 D5a). **Inventory groups are deliberately not rendered**: a View _is_ the group (ADR-0055 G3), imported AWX groups land as `awx.group.name` labels (ADR-0025), and run-time narrowing is `params.limit` (ADR-0117 D1/D5b)                                                                                                                                                                                            |
| Credentials + injectors                   | 🟢     | CredentialRef + SecretBroker [sdk/secretbroker/](../../sdk/secretbroker/), ADR-0052. The **machine credential** reaches the connection from its MOUNT (ADR-0126 D1) — AWX's inventory-host-vs-machine-credential split, done properly: `connection.credentialRef` names a ref already on the Step, the shim renders `ansible_ssh_private_key_file` from it, and connection keys in `extraVars` are refused. Until ADR-0126 every Workflow hand-wrote the path, so the authorized credential and the file read were two facts nothing reconciled (§2.4)                                                                                                                       |
| Execution Environments                    | 🟢     | [ee/Dockerfile](../../ee/Dockerfile) (ansible-runner `/runner` contract), ADR-0051. **Content (collections + roles) is a pinned build-time declaration** verified against the resolved set ([ee/content.py](../../ee/content.py), ADR-0117 D3) and byte-locked per artifact (follow-up i), and **which EE a Step runs in is the Actuator's declaration** — not a run-time parameter (D3a). The default EE carries a declared **content floor** every variant must be a superset of ([platform.requirements.yml](../../ee/content/platform.requirements.yml), [ADR-0149](../adr/0149-the-execution-environment-content-floor.md)), so `ansible.builtin.package` works on apk/zypper/pacman targets and not only apt/yum ones — it did not, until it was run (ANS-012). Proven end to end by the [app-cert demo](../../demos/app-cert/README.md). The **factory** to build them from an `execution-environment.yml` is still P5 below                                                                                                                                                                |
| Schedules                                 | 🟢     | [triggers/reconcile.go](../../core/internal/triggers/reconcile.go) (Temporal Schedules), ADR-0010                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| Surveys                                   | 🟢     | **`Workflow.inputs`** — a JSON Schema launch interface validated at one chokepoint below every launch path (ADR-0118 D2), plus `ansible.input.v5` + parametrized Views (ADR-0024/0117 D1). An **imported** AWX survey lands there directly, with each answer bound into the Step's `extraVars` from `{{.launch.<var>}}` (ADR-0025 follow-up, closed) — so a `/api/v2` launch is validated against the survey rather than merged blindly. **One deliberate departure from AWX:** a survey **password** question is refused, not imported — secret material is brokered as a CredentialRef, never passed as a launch input (§2.5), and the migration report blocks until it is |
| Job slicing / per-target results          | 🟢     | `RunOutcome.PerTarget`, `Slices` [orchestrate/across.go](../../core/internal/orchestrate/across.go), ADR-0054                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| Fact caching                              | 🟢     | facts → governed Facets w/ Provenance, ADR-0054                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| Webhooks                                  | 🟢     | Emitter receiver [emitters/](../../core/internal/emitters/), ADR-0018                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| **Notifications**                         | 🟢     | A Sink's `kind` names its delivery Action, so core holds no list of drivers (ADR-0125). **webhook** + **smtp/email** ship; **Slack** works today over webhook (its incoming-hook url IS the credential); **PagerDuty/Teams** are a plugin driver with no core change — the routing key rides the brokered credential, never a Git-declared body (§2.5). ADR-0027                                                                                                                                                                                                                                                                                                             |
| **`/api/v2` façade**                      | 🟡     | [awxfacade/](../../core/internal/awxfacade/): job_templates/jobs/inventories(+launch/cancel/stdout), **schedules** and **workflow_job_templates + workflow_jobs**, **projects** and **credentials + credential_types (all 2026-07-31)** shipped — **the four named route families are complete**.  **14 of the 21 Workflows the reference estate ships were invisible on this surface until the WFJT family landed** — `job_templates` presents only single-Step, Gate-free Workflows, so every DAG, every gated Workflow and every policy-checkpointed one had no representation at all, which is the compat door failing at exactly the shapes an adopter migrates FOR. The partition is now exact and total (a Workflow is one family or the other, never both and never neither, pinned against the shipped estate). Launch calls the SAME `orchestrate.LaunchWorkflowRun` the native door does — extracted rather than copied, because a second launch path grows its own authz and its own drift (§1.6) — and authorizes EVERY actuation Step's View, so the compat surface is not the weaker path. `workflow_nodes` renders the DAG with Stratt's backward `needs` edges inverted into AWX's forward success/failure/always lists; a node's `unified_job_template` is **null** unless the Step nests a real declared Workflow, because a Step is not independently launchable and a synthesized id would be a dangling reference. What AWX has no concept for (policy checkpoints, capability-class Steps) is named plainly in a `summary_fields.stratt` block rather than flattened into "job" — an AWX field either carries its AWX meaning faithfully or is absent (§1.8). **`projects` needed no inferring:** ADR-0134 D2 already declares an Actuator's `contentDir` to be "one project: playbooks, roles/, group_vars/", one Actuator per project, so the family is that mapping rather than a synthesized grouping over Step params (which would have been core reading a tool's params by name to invent an object — the §1.4 trap that ADR warns implementers about). `scm_type` is manual because Stratt resolves content at estate PARSE and carries it in the JobSpec — nothing clones at run time by design — and `scm_revision` is empty because core tracks none per content root (AWX-001), not because none exists. `job_templates.project` is no longer null. No `POST /update/`: an update means "clone the SCM again" and there is nothing to clone, so offering a no-op would tell an operator their content refreshed. **`credentials` is where §2.5 is easiest to erode and is not:** an AWX credential CONTAINS material, a CredentialRef contains a POINTER, and no graph-store method returns material — not redacted, not encrypted, none. `inputs` carries the declared injection KEY NAMES with AWX's `$encrypted$` sentinel, which asserts "a secret stands here" (true) and not "Stratt holds it" (false); the key names are Git-declared and already on /api/v1, so hiding them would hide diagnosis while protecting nothing. The LOCATOR is deliberately absent — not material, but the address of it, and a compat listing is not the place to widen who reads it. One synthetic `credential_type` for every ref, because AWX's type says what a credential is FOR while Stratt's `backend` says WHO BROKERS IT — mapping one onto the other is a category error that would read as fact. Attaching a credential at launch stays in `ignored_fields`: a Step's credentialRefs are declared and reviewed in Git (ADR-0009), and a launch-time swap would make the compat surface the one door that skips that review. ADR-0026. ADR-0026. **`schedules` is served from schedule-kind Triggers, and its one hard part is recorded rather than smoothed over:** AWX carries an iCal RRULE and Stratt carries cron, and the two are not interconvertible in general — cron ORs day-of-month against day-of-week where RRULE cannot. So the façade converts ONLY the subset that round-trips faithfully (minutely/hourly intervals, fixed-minute hourly, daily, and named-weekday weekly — which covers every Trigger the estate ships) and for anything else **omits `rrule` entirely**, stating the real cron in `description`. A wrong RRULE is the worst available output here: awxkit and the AWX UI would render a firing time different from the one actually in force with nothing anywhere to contradict them, which is hiding a discrepancy rather than hiding mechanism (§1.8). READ-ONLY like every other family — a schedule is a DECLARATION reconciled from Git, and a POST door would make the compat surface a second write path into desired state (§2.2/§2.3)                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| Custom credential **types**/injectors     | 🟢 **the task is served; the abstraction differs** (row was wrong, corrected 2026-08-04) | The row cited `injectionFor` as Stratt's mechanism. It is not: that map lives in the AWX IMPORT plugin and picks a sensible policy per AWX credential kind at MIGRATION time. Stratt's actual mechanism is per-CredentialRef and operator-declared in Git — `injection: [{key, as: env|file, name}]` — which is AWX's `env` and `file` injectors as DATA, and shipped estates use it (`aws-dev`, `crossplane-dev`). What genuinely differs, each for a reason: no `extra_vars` injector, because material as extra_vars would put a secret through the CORE (§2.5); no Jinja-composed file, because that is the §1 no-new-languages line — a composed artifact is one backend key holding the composed blob, which is exactly what the shipped kubeconfig and AWS-credentials refs do; and no first-class credential TYPE, because a type is a second place to declare a shape the ref and the Contract already declare |

### 2. Policy-as-code — 🟢 ahead of AAP 2.7

AAP 2.7's policy feature is a thin, new, OPA-only **binary allow/deny** gate on job execution. Stratt
reproduces that (external OPA/Kyverno via [policy/exec.go](../../core/internal/policy/exec.go), ADR-0074) as
_one pluggable provider behind a port_, then goes well beyond: a **four-valued Decision lattice**
(allow/require-approval/escalate/deny, ADR-0062), enforced **obligations** that become tracked Findings
(ADR-0075), a **typed Control library** — TimeWindow/SoD/Waiver/BreakGlass/Quorum (ADR-0067–0071), **dual
PEPs** (execution gate _and_ config-admission at every door incl. the imperative API, ADR-0073/0076),
**un-bypassable mandatory floors** (ADR-0066), and hash-chained durable decision recording (ADR-0065).
Self-imposed TODOs only (shape unification, live-refresh, gRPC transport for external engines).

### 3. Platform Gateway — 🟢 code-complete core, 🟡 UI/analytics

| Capability                                 | Status                | Evidence                                                                                                                                                                                          |
| ------------------------------------------ | --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Unified UI (single-pane)                   | 🟢                    | [ui/src](../../ui/src) TanStack SPA, rebuilt greenfield as a pure `/api/v1` client (ADR-0090/0091): Graph/Views/Entities · Runs/Workflows/Gates · Intents · Findings · Connectors · Fleet · Admin |
| SSO / OIDC                                 | 🟢                    | [authz/oidc.go](../../core/internal/authz/oidc.go), Zitadel, ADR-0012                                                                                                                             |
| Unified RBAC (OpenFGA)                     | 🟢                    | [authz/openfga.go](../../core/internal/authz/openfga.go), ADR-0028                                                                                                                                |
| User/team lifecycle (SCIM 2.0)             | 🟢                    | [scim/](../../core/internal/scim/), group→team authz, ADR-0035                                                                                                                                    |
| One Principal (UI/CLI/CI/agent)            | 🟢                    | `types.Principal`, one `ResolvePrincipal` seam for `/api/v1` + MCP, §1.6                                                                                                                          |
| One audit stream + SIEM                    | 🟢                    | hash-chained [audit/](../../core/internal/audit/) + [forwarder/](../../core/internal/forwarder/), ADR-0034                                                                                        |
| Platform MCP server (agent door)           | 🟢 **differentiator** | [mcpserver/](../../core/internal/mcpserver/), ADR-0021 — AAP has no first-class MCP door (theirs is 2.7 tech-preview)                                                                             |
| **Cost/usage accounting**                  | 🟡                    | MCP-call counts only (`types/usage.go`); **no run-minute/monetary/federated cost**, no dashboard (§7.6)                                                                                           |
| **Multi-tenancy**                          | 🟡                    | Cells (ADR-0044) + Environments (ADR-0057) + authz scoping; **no first-class Organization container**                                                                                             |
| CLI query verbs                            | 🟡                    | `stratt plan/apply/import/bundle`; no `get/describe` estate-query UX                                                                                                                              |
| In-app activity-stream / org / settings UI | 🔴                    | audit is backend/SIEM only; no admin/settings/analytics screens                                                                                                                                   |

### 4. Automation Mesh — 🟢 code-complete, one gap

Sites (push+pull, [sitegw/](../../core/internal/sitegw/), ADR-0032), the pull **agent** as an authenticated
transport relay (ADR-0049), cosign-verified **Bundle** pull ([bundle/](../../core/internal/bundle/)), and
**Cells** (control-plane partitioning, ADR-0044/0045) together give receptor-class remote execution _plus_
a partitioning story AAP has no equivalent for. **Genuine gap:** **multi-hop / relay (hop) nodes** — Stratt
is a flat hub↔leaf NATS model; Receptor's arbitrary routable mesh (control/hop/execution chains for deep
DMZ traversal) is not modeled. _(Note: `plugins/mesh/` is the service-dependency connector — a false
friend, unrelated to automation mesh.)_

### 5. Event-Driven Ansible — 🟢 task parity (two mechanisms declined by decision)

The Trigger engine (Emitter × CEL → Workflow/View launch, ADR-0018) covers the **spine**: event ingest,
condition eval, at-least-once durable launch (JetStream), content-hash dedup, and full authz/descent
parity. It is **not a rulebook engine**. Gaps:

- ~~**Source breadth** — 3 kinds vs AAP's dozens~~ — **the comparison was wrong** (2026-08-03), and
  the residue it left is now **paid**. The original row measured Stratt's `kind` ENUM against AAP's
  source LIBRARY. A `stream` Emitter is a PLUGIN that outbound-connects and publishes onto the
  emitter stream itself (salt does exactly this, ADR-0039), so a Kafka or SQS source needs NO core
  change — plugin-authoring, which is how §1.4 says breadth arrives. What core still held was the
  `explode` for webhook-shaped sources, with `alertmanager` hardcoded;
  [ADR-0163](../adr/0163-one-post-many-events-and-the-shape-is-not-cores.md) makes the fan-out a
  declaration and takes the vendor's name out of core AND out of the published OpenAPI enum.
  **Authentication was the remaining half and is now paid**
  ([ADR-0164](../adr/0164-a-source-signs-and-the-core-does-not-hold-the-key.md)): the header a source
  presents its token in is declared (D1 — GitLab's `X-Gitlab-Token` and the long tail behind it),
  and a source that SIGNS its body is verified by delegating to the key's holder over the port (D2),
  because the core may not hold the secret that would let it check a MAC itself (§2.5, ADR-0052).
  **Timestamped schemes and freshness now ship too**
  ([ADR-0167](../adr/0167-a-replay-is-a-valid-signature-at-the-wrong-time.md)): a `kv` header shape,
  a declared signature/timestamp pair, `signedPayload: timestamp.body`, and a tolerance window that
  refuses a stale request BEFORE consulting the verifier. Two corrections to what this row used to
  say: retry dedup always existed (`EventHash` excludes `ReceivedAt`, so a retried POST collides on
  both the JetStream publish and the derived workflow id) — it is a CORRECTNESS control, never a
  replay defence, because an attacker picks the moment. And freshness only protects sources that
  sign a timestamp; the shared-token half still cannot bound replay, and ADR-0167 D3 says so rather
  than implying coverage.
- **Rulebook format** — a Trigger is `1 Emitter + 1 CEL → 1 target`; no ordered multi-rule ruleset.
  A PACKAGING difference rather than a capability gap: the engine evaluates every Trigger against
  every event and fires every match, which is what a ruleset does. AAP binds sources and rules in one
  file; Stratt has reusable Emitters plus Triggers. Declined in ADR-0162 D6 rather than left open.
- ~~**Stateful / meta conditions** — CEL sees one event; no `count > N within Ms`, no cross-event
  correlation.~~ — **shipped** ([ADR-0162](../adr/0162-a-trigger-decides-on-more-than-one-event.md),
  live-proven). CEL still sees one event and still answers one question — that is what keeps §1
  intact — and the PATTERN sits beside it as data: `within` + `count` for "when this keeps
  happening", `allOf` + `correlateBy` for "when both of these have happened". Two differences from
  AAP's working memory, both deliberate: there is **no `set_fact`/`retract_fact` fact store** (D6 —
  it would be a second truth about the estate with no provenance, §1.2), and **`correlateBy` is
  mandatory with `allOf`**, so "a deploy finished somewhere and a health check failed somewhere"
  cannot fire. AAP's `all()` leaves that hazard to the author.
- ~~**Throttling / debounce / rate-limit** — dedup only.~~ — **false** (2026-08-03), and then
  **fixed**. `cooldownSeconds` was already declared, enforced and shipped; the real limitation was
  narrower and worse — the bookkeeping was an in-memory map, so it RESET ON RESTART and did not hold
  across replicas, meaning the storm damping an estate declared was not the one it got and nothing
  said so. ADR-0162 D2 moves it to Postgres, shared and durable. It also makes it **readable**, which
  a rules engine's working memory is not: "why did this Trigger not fire?" is now a row (§1.8).
- **Inline meta-actions** — can only launch a Workflow/View; no `set_fact`/`post_event`/`run_module`.

### 6. Automation Hub — 🟡 partial (the registry half remains)

AAP's hosted, signed, versioned **collection** distribution + **EE registry** + **collection-signing** trust
story. Stratt covers the **trust + execution half** convincingly — boot-time hash-**pinned** Contracts that
refuse-to-boot on drift (ADR-0015), core-side **verify-don't-trust** of plugin outputs (ADR-0047),
cosign-verified OCI **Bundles** (ADR-0032), digest-pinned images throughout — and reframes "content breadth"
as independently-shipped **plugin images**, each its own CI unit (ADR-0046). What's genuinely missing:

- **Content registry / index** — no collection hosting, no plugin catalog/discovery/version-resolution
  (relies on external OCI + hand-pinned Helm refs).
- ~~**EE build tooling** — no `ansible-builder` / `execution-environment.yml` factory~~ — **the row
  was stale and is now closed**. ADR-0124 D1 already read an `execution-environment.yml` and resolved
  its Galaxy content through the pinned, hash-verified pipeline (which `ansible-builder` itself does
  not do); [ADR-0170](../adr/0170-from-a-definition-to-an-image.md) turns that reading into a BUILD —
  `task ee:factory:build EE=…` — and carries `dependencies.python` into the image, which is
  ADR-0159's third axis and the difference between an EE that connects and one that fails at connect
  time. `ansible-builder` is deliberately NOT adopted (D3): compatibility belongs at the declaration
  boundary, not in adopting a second build graph. Still open: an EE **distribution** service.
- ~~**Supply-chain pipeline** — … implements only DCO. Image signing + SBOM + SLSA attestation are
  **unbuilt**.~~ — **built** (2026-08-04), and the row understated the problem: there was no release
  pipeline at all, so "from release one" was unmet in a more basic way than missing signatures.
  [ADR-0165](../adr/0165-there-has-never-been-a-release-to-sign.md) adds a tag-triggered workflow that
  signs **keyless** (Sigstore — identity-bound to the workflow, no long-lived key), attaches an SPDX
  SBOM and SLSA provenance, and **verifies its own output**; `task supply:verify` runs exactly what CI
  runs. Enforcement followed in two places the register never separated:
  [ADR-0168](../adr/0168-a-warning-is-not-a-gate.md) makes an unpinned image a chart **render
  failure**, and [ADR-0169](../adr/0169-the-last-door-before-something-runs.md) refuses one at the
  **dispatcher** — a different set of images entirely, since an EE-Job image is named by an Actuator
  in the estate and the chart cannot see it. **Still open, stated plainly**: nothing is published yet
  (ADR-0165 D5 — that is a decision, not a coding task), a digest is not a signature, and in-cluster
  admission verification remains booked. _(enterprise-crack SEC-5/SUP-1.)_
- **Remote/upstream sync** — no Galaxy mirror. **Air-gap content seeding SHIPPED** (ADR-0124 D2):
  `task ee:content:pull` downloads the declared collections on a connected machine, and an EE built
  with `EE_OFFLINE=<dir>` reaches NO registry — with the pin check and the lockfile check unchanged,
  so an air-gapped build is verified by the same hash a connected one is rather than trusted for
  where its bytes came from. What remains is the MIRROR (a hosted upstream), not the seeding.
  Historically this bullet read "no air-gap content seeding"; `requirements.yml`
  resolution now exists but only at **EE build time**, pinned and verified (ADR-0117 D3), which is
  deliberately not a run-time resolver. The registry is **no longer the checksum authority**: each
  artifact's content SHA-256 is recorded in an in-repo lockfile beside its declaration
  ([ee/content/](../../ee/content/)) and every EE build fails on a mismatch, so a republished version at the
  same version number is caught rather than silently changing what a Run executed (ADR-0117 follow-up i —
  which also closes the roles half, where there had been no checksum step at all). (git-sync covers
  SCM-project delivery only.)

---

## 2.7-specific deltas

| AAP 2.7 new feature                                           | Stratt posture                                                                                                                                                                  |
| ------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **MCP server integration** (tech preview)                     | 🟢 **ahead** — platform MCP server is GA-in-repo (ADR-0021), agent-native is a founding discipline (§1.6)                                                                       |
| **No RPM installer** (containers/OpenShift only)              | 🟢 **advantage** — any-K8s Helm (charter §7.1: any-K8s ✅ vs AAP ❌ OCP-only)                                                                                                   |
| **OIDC IdP for Vault** (short-lived job-scoped JWTs)          | 🟡 **divergent** — SecretBroker resolves per-call, never-persisted creds (ADR-0052); we don't literally issue Vault JWTs, but the "no long-lived creds" goal is met differently |
| **Self-service portal / visual EE builder / content catalog** | 🔴 missing (ties to Hub gap + no EE-build UI)                                                                                                                                   |
| **Automation dashboard / ROI analytics**                      | 🔴 missing (cost/usage is MCP-calls only)                                                                                                                                       |
| **AI assistant / BYOK (Lightspeed)**                          | ⚪ **not our thesis** — agent-native via MCP is our answer, not AI content-generation                                                                                           |
| **Dev workspaces** (browser VS Code)                          | ⚪ **out of scope** — a dev-tooling product, not the platform layer                                                                                                             |

---

## Prioritized gap backlog

**Tier 1 — parity-blocking / migration-blocking (do first):**

- **P1 · Live-cluster e2e** — the actual replacement proof (see below). Nothing here is _new_ code; it's the
  first real integration of importer + façade + EE plugin on kind.
- ~~**P2 · `/api/v2` route breadth** — `workflow_job_templates`, `schedules`, `projects`, `credentials` so
  existing AWX/AAP tooling survives cutover (the strangler-fig front door, §7.6).~~ — **done
  (2026-07-31)**, all four plus `workflow_jobs` and `credential_types`. What remains is not route
  breadth but launch SEMANTICS: `ask_*_on_launch` beyond variables (AWX-015), and attaching a
  credential or inventory at launch — both deliberately refused today rather than partly honored,
  and both a design question about desired state, not a missing endpoint.
- ~~**P3 · Notification sinks** — at least Slack + SMTP/email beyond webhook (a real Controller
  expectation).~~ — **done (ADR-0125)**, and the framing was wrong: the gap was never three missing
  drivers, it was that core named the one driver in three places (a kind switch in the dispatcher, a
  closed set in the validator, a hardcoded `registerPluginAction` in `strattd`). A Sink's `kind` now names
  its delivery Action and core holds no list. **Slack already worked** over webhook — the repo's own
  notify test has declared a Slack-shaped body since ADR-0027 — **smtp/email ships**, and PagerDuty is a
  plugin driver away, which is the only way it could ever have worked: its routing key must ride the
  request body, so reaching it via a Git-declared `bodyTemplate` meant baking a secret (§2.5).

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

1. **Import** a real AWX **24.6.1** export via `stratt import awx` (ADR-0025; the importer now lives in
   [plugins/ansible-automation](../../plugins/ansible-automation/) per ADR-0089/0127, with the `/api/v2` compat façade staying in
   [core/internal/awxfacade](../../core/internal/awxfacade/)) → Step presets + Views + Workflows +
   CredentialRefs reconciled into the estate.
2. **Launch** an imported job template via `POST /api/v2/job_templates/{id}/launch` (ADR-0026 façade).
3. **Execute** over the **ansible EE plugin** as an ephemeral K8s Job speaking the sovereign port (ADR-0051),
   against a View-resolved target.
4. **Tail** via `GET /api/v2/jobs/{id}/stdout` and **cancel** via the native cancel path.
5. **Project facts back** as governed Facets (ADR-0054) — the "AWX can't do this" moment.

**What the e2e needs that isn't wired yet:** a live-cluster harness (kind + Helm + the EE image built), a
seeded AWX export fixture. The workflow half is now demonstrable through the compat door: the
`workflow_job_templates` route shipped 2026-07-31. Everything else is shipped.

**This is the single highest-leverage move toward a credible "AAP running on Stratt" claim** — and it will
surface whatever integration gaps the in-repo verification has been hiding (the live-cluster e2e, E2E-1, is
still outstanding).

---

## Sources

- [What's new in AAP 2.7 — Red Hat Developer](https://developers.redhat.com/articles/2026/06/10/whats-new-red-hat-ansible-automation-platform-2-7)
- [What's New in AAP 2.7 — Red Hat blog](https://www.redhat.com/en/blog/whats-new-ansible-automation-platform-2-7)
- [AAP 2.7 release notes — Red Hat Documentation](https://docs.redhat.com/en/documentation/red_hat_ansible_automation_platform/2.7/whats_new-overview_of_redhat_ansible_intro)
