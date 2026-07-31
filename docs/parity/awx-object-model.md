# AWX object-model parity — object by object, field by field

**Subject:** the AWX **24.6.1** `/api/v2` object model — the API AAP's Automation Controller exposes and
the version charter §5.6 freezes the migration target at.

**Scope:** what the `ansible-automation` plugin **reads**, **projects**, and **transforms**, against what
AWX actually holds. The [platform audit](aap-2.7-platform.md) scores AAP by _component_ and finds the
Controller 🟢 code-complete; this one goes a level down and asks whether the graph can answer questions
about an AWX estate. Those are different questions and they have different answers.

**Verification (2026-07-26).** Everything ADR-0128…0132 shipped is now proven end to end against a real
graph store, not only at the plugin layer: `core/internal/pluginhost/awx_estate_integration_test.go`
projects the full `ansible.*` estate through the real host path — grant gating included, because that is
where a projection bug hides — and then resolves the **committed** estate declarations against it. It
covers the topology-selecting `awx-prod-templates` View and asserts each boolean Baseline's facet
predicate addresses a field that exists, since a path typo makes a Baseline silently never fire rather
than error (§1.8). Both properties were mutation-checked.

**Evidence base (2026-07-26).** Stratt column: read directly from
[plugins/ansible-automation/](../../plugins/ansible-automation/) at commit time, every row linked. AWX
column: the AWX 24.6.1 `/api/v2` surface from vendor documentation and the browsable API — **not** verified
against a live instance in this pass, so treat a field-presence claim as a documentation reading. Where
that would change a verdict, the row says so.

**Why this audit exists:** ADR-0127's research deep-read exactly one AWX object (the Project) and found
four distinct concerns inside it. That was one object out of ~30, and the follow-ups it booked were sized
from that single sample. Everything below is the rest of the sample.

---

## Scorecard

| Area                                       | Verdict                          | One-line                                                                                         |
| ------------------------------------------ | -------------------------------- | ------------------------------------------------------------------------------------------------ |
| **Object coverage** (which objects at all) | 🟡 10 projected                  | Spine + credentials + accounts + labels + EEs; **role grants** and **instance groups** remain, both declined with an argument |
| **Field depth** on projected objects       | 🟡 template deepened, rest thin  | `ansible.template` now carries run state, run knobs + a credential edge (**ADR-0128**); the other four are still 1–3 fields |
| **Workflow topology**                      | 🟢 invocations + approval gate   | `invokes` edges + `hasApprovalGate`/`nodeCount` (**ADR-0129**); the node graph stays adopt's job |
| **Facet schema coverage**                  | 🟡 **8 of 13**                   | +template +credential (0128) +workflow (0129) +user (0130) +label (0132) +EE (0133)             |
| **Read-path symmetry**                     | 🟡 divergent by accident         | Projection reads 5 endpoints, adopt reads 9; nothing states which asymmetries are deliberate     |
| **`stratt adopt` transform**               | 🟢 deep and honest               | Reads what it needs, refuses what it must (secrets, password surveys), reports what it drops     |

**Bottom line.** The transform half is in good shape — `stratt adopt` reads deeply and its migration report
blocks rather than guesses. The **projection** half is a spine, not a mirror: it can answer "what
automation exists and what runs what," and it cannot answer "which templates use this credential," "which
are failing," "who is on this team," or "what does this workflow actually do." The single highest-value
fix was field depth on `ansible.template` (**AWX-010**) — most governance questions an operator brings to
an AWX mirror are field-level questions about job templates — and that is **done**
([ADR-0128](../adr/0128-ansible-template-projection-depth.md)). The next is workflow topology
(**AWX-002**) — also **done** ([ADR-0129](../adr/0129-workflow-topology-projection.md)) — leaving the
authorization slice, which needs a decision before code.

---

## 1. Object coverage

**Coverage vocabulary** — four distinct states, deliberately not collapsed:

| Coverage     | Meaning                                                                                                                       |
| ------------ | ----------------------------------------------------------------------------------------------------------------------------- |
| `projected`  | Becomes an `ansible.*` Entity + Facet in the graph, tombstoned per-Source                                                     |
| `adopt-only` | Read by the deep-read for the CaC transform ([awxapi/](../../plugins/ansible-automation/controller/awxapi/)), never projected |
| `mapped`     | Deliberately becomes a **Named Kind** at adopt, by the charter §2 migration mapping — never mirrored as itself                |
| `none`       | No path reads it                                                                                                              |

`mapped` is the one that must not be misread as a gap: an AWX Inventory **should not** become an
`ansible.inventory` Entity, because a View _is_ the inventory (charter §2, ADR-0055 G3). Rows below say
which is which.

| AWX object                       | Coverage        | Evidence / note                                                                                                                                             | ID          |
| -------------------------------- | --------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------- |
| `job_templates`                  | `projected` 🟢  | [types.go](../../plugins/ansible-automation/controller/types.go) → `ansible.template`; deepened by ADR-0128 (~~AWX-010~~)                                   |             |
| `workflow_job_templates`         | `projected` 🟢  | → `ansible.workflow` + `invokes` edges and the approval-gate fact (ADR-0129)                                                                                |             |
| `schedules`                      | `projected` 🟢  | [types.go:97](../../plugins/ansible-automation/controller/types.go#L97) → `ansible.schedule` + the `schedules` edge                                         |             |
| `organizations`                  | `projected` 🟢  | [types.go:100](../../plugins/ansible-automation/controller/types.go#L100) → `ansible.org`                                                                   |             |
| `teams`                          | `projected` 🟢  | → `ansible.team` + `has-member` edges (~~AWX-004~~, ADR-0130 D2) — an estate fact, never an authz one                                                       |             |
| `workflow_job_template_nodes`    | `derived` 🟢 + `adopt-only` ⚪ | The projection now reads them and derives `invokes` + `hasApprovalGate` (~~AWX-002~~, ADR-0129); the DAG's SHAPE stays adopt's, because fidelity is the transform's job and not the mirror's. Nodes as entities booked as **AWX-016** |  |
| `projects`                       | `adopt-only` 🔴 | ADR-0127 D4 already books `ansible.project` + `scm_revision`; this audit confirms the sizing                                                                | **AWX-001** |
| `inventories`                    | `mapped` ⚪     | → **View** ([materialize/views.go](../../plugins/ansible-automation/controller/materialize/views.go)); smart inventories reduce their `host_filter`         |             |
| `inventory_sources`              | `mapped` ⚪     | → points at the native Syncer for that cloud, never re-implemented as an AWX plugin                                                                         |             |
| `hosts` (inventory members)      | `mapped` ⚪     | Deliberately never re-projected — that is the writable-CMDB anti-pattern (§1.2); hosts come from their own Syncers                                          |             |
| `credentials`                    | `projected` 🟢 + `mapped` ⚪ | **Both, and deliberately** (ADR-0128 D2): projected as `ansible.credential` (name+kind, never material, §2.5) so credential usage is a traversal, AND mapped → **CredentialRef** at adopt. Mirror vs Named Kind, the same pair as `ansible.template` ↔ Workflow |             |
| `job_templates/{id}/survey_spec` | `mapped` ⚪     | → `Workflow.inputs` (ADR-0118 D2); a **password** question is refused, not imported                                                                         |             |
| `users`                          | `projected` 🟢  | → `ansible.user` (~~AWX-003~~, ADR-0130 D1) — AWX's LOCAL ACCOUNT table, deliberately **not** `identity.subject`, which has a single write-owner (§2.1)     |             |
| `roles` (RBAC grants)            | `none` 🔴       | No principal→object grant is read, so no AWX permission is visible or migratable                                                                            | **AWX-005** |
| `jobs` / `job_events`            | `none` ⚪🟠     | Run **history** is deliberately not mirrored — §3 forbids replicating AWX's job-events-table pathology. But **current/last status is not history**          | **AWX-011** |
| `labels`                         | `none` 🟠       | AWX labels are the operator's own grouping vocabulary; they would map to graph labels cleanly and nobody has looked                                         | **AWX-006** |
| `execution_environments`         | `none` 🟠       | Which EE a template runs in is invisible in the mirror; on our side EE is an Actuator declaration (ADR-0117 D3a), so the mapping is not obvious             | **AWX-007** |
| `instance_groups`                | `none` 🟠       | AWX's execution placement; our equivalent is Sites/Cells, so this is a real mapping question nobody has asked                                               | **AWX-008** |
| `notification_templates`         | `projected` 🟢  | → `ansible.notification` + `owned-by` edge (2026-07-31). Name, DRIVER and config KEY NAMES only — no configuration VALUE is ever projected, because AWX returns non-secret fields in the clear and for the commonest driver the cleartext field IS the credential (a Slack webhook URL is a bearer secret). `notificationType` is a Sink's `kind` on cutover (ADR-0125). ATTACHMENTS deliberately absent: 3 sub-reads per job template | ~~**AWX-009**~~ |
| `credential_types`               | `none` 🟠       | Custom credential types + injectors — the platform audit already scores this 🟡 (`injectionFor` is a fixed map)                                             | **AWX-012** |
| `ad_hoc_commands`                | `none` ⚪       | An imperative one-shot; Stratt's equivalent is a Run against a View, not an object to mirror                                                                |             |
| `workflow_approvals`             | `adopt-only` ⚪ | Approval **nodes** are transformed into Workflow gates; pending approval _instances_ are run state, not config                                              |             |
| `activity_stream`                | `none` ⚪       | We keep our own hash-chained audit (ADR-0034); mirroring AWX's is not a graph concern                                                                       |             |
| `settings`, `ping`, `metrics`    | `none` ⚪       | Controller-internal operations surface, not estate                                                                                                          |             |
| `applications` / `tokens`        | `none` ⚪       | AWX's OAuth app registry; Stratt has one Principal model (§1.6)                                                                                             |             |
| `system_job_templates`           | `none` ⚪       | AWX housekeeping (cleanup jobs); not estate automation                                                                                                      |             |

---

## 2. Field depth on the five projected kinds

This is the section the audit was written for. Each table lists **what lands in the graph** against what
AWX holds on that object.

### `ansible.template` ← `job_templates` — ~~5 fields of ~50~~ → 18 · ~~**AWX-010**~~ done (ADR-0128)

Projected: the original `name`, `jobType`, `playbook`, `surveyEnabled`, `description`, **plus** run state
(`lastRunStatus`, `lastRunFailed`, `lastRunAt`, `nextRunAt`) and the run knobs (`forks`, `limit`,
`jobTags`, `skipTags`, `timeout`, `verbosity`, `diffMode`, `becomeEnabled`, `scmBranch`) · labels
`ansible.name`, `ansible.org` · relations `owned-by` → org, `runs` → playbook, **`uses-credential` →
`ansible.credential`**. Validated against a pinned closed schema.

The table below is kept as the record of what was missing and why each row mattered; ✅ marks what
ADR-0128 closed.

| AWX field group                                                                                                                    | Projected? | Why it matters                                                                                                              |
| ---------------------------------------------------------------------------------------------------------------------------------- | ---------- | --------------------------------------------------------------------------------------------------------------------------- |
| `summary_fields.credentials[]`                                                                                                     | ❌         | **"Which templates use this credential?"** is unanswerable — the single most-asked AWX audit question                       |
| `status`, `last_job_run`, `last_job_failed`, `next_job_run`                                                                        | ❌         | **"Which automation is failing / dead?"** — a governance question the mirror exists to answer                               |
| `forks`, `limit`, `job_tags`, `skip_tags`, `timeout`, `verbosity`, `diff_mode`                                                     | ❌         | The run knobs. We model every one of these in `ansible.input.v6` — so the mirror is thinner than our own execution Contract |
| `ask_*_on_launch` (~15 booleans)                                                                                                   | ❌         | Prompt-on-launch surface; determines what a launch may override. Needed for a faithful cutover                              |
| `become_enabled`                                                                                                                   | ❌         | Privilege escalation — reviewable on our side (ADR-0117 D1), invisible on the mirrored side                                 |
| `extra_vars`                                                                                                                       | ❌         | May contain secrets; **should stay unprojected** unless deliberately screened (§2.5) ⚪                                     |
| `execution_environment`                                                                                                            | ❌         | See **AWX-007**                                                                                                             |
| `scm_branch`                                                                                                                       | ❌         | Which ref the template runs — the same soundness thread as `scm_revision` (**AWX-001**)                                     |
| `job_slice_count`, `allow_simultaneous`, `use_fact_cache`, `force_handlers`, `start_at_task`, `host_config_key`, `webhook_service` | ❌         | Lower value; listed for completeness                                                                                        |
| `instance_groups`, `labels`                                                                                                        | ❌         | **AWX-008** / **AWX-006**                                                                                                   |

### `ansible.workflow` ← `workflow_job_templates` — ~~name + description~~ · ~~**AWX-002**~~ done (ADR-0129)

Projected: `name`, `description`, `nodeCount`, `hasApprovalGate` · `owned-by` → org, **`invokes` →
`ansible.template` / `ansible.workflow`** (one edge per distinct target). Validated against a pinned closed
schema. Governed by `awx-workflow-covered`, the orphan-workflow analogue of `awx-template-covered`.

The original finding is kept below as the record of what was wrong.

The workflow's actual content — its nodes, its success/failure/always edges, its approval gates — is read
by the adopt deep-read ([types.go `WorkflowNode`](../../plugins/ansible-automation/controller/awxapi/types.go))
and transformed faithfully into a Stratt Workflow DAG, but is **never projected**. So the graph knows a
workflow exists and knows nothing about what it does. An `ansible.workflow` Entity cannot be reasoned
about, related to the templates it invokes, or governed by a Baseline that means anything.

This is also the sharpest instance of the read-path asymmetry in §4: the transform sees the topology, the
mirror does not, and no document says that was a decision.

### `ansible.schedule` ← `schedules` — 3 fields · pinned schema 🟢

Projected: `name`, `rrule`, `enabled` · relation `schedules` → template/workflow. Matches
[the pinned Facet schema](../../contracts/facets/ansible.schedule.schema.json) exactly.

| AWX field                                                                                                | Projected? | Note                                                                                                                                       |
| -------------------------------------------------------------------------------------------------------- | ---------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| `extra_data`                                                                                             | ❌         | A schedule's per-fire variable overrides. **Two schedules of one template with different `extra_data` are indistinguishable in the graph** |
| `timezone`, `dtstart`, `dtend`, `until`                                                                  | ❌         | `rrule` alone is under-determined without the timezone                                                                                     |
| `next_run`                                                                                               | ❌         | "When does this next fire" — the obvious question of a schedule                                                                            |
| `limit`, `job_tags`, `skip_tags`, `job_type`, `verbosity`, `diff_mode`, `scm_branch`, `forks`, `timeout` | ❌         | Per-schedule launch overrides, same family as the template knobs                                                                           |

Tracked as **AWX-013**.

### `ansible.org` ← `organizations` — 2 fields 🟢

Projected: `name`, `description`. AWX also holds `max_hosts` and `default_environment`; neither is
load-bearing for the mirror. Adequate as-is — the notable absence is not on the org, it is that its
members are unread (**AWX-003**/**AWX-005**).

### `ansible.team` ← `teams` — 1 field · **AWX-004**

Projected: `name` · relation `member-of` → org. AWX `description` is dropped (trivial). What matters:
**a team's users are never read**, so an `ansible.team` is an empty container. Combined with **AWX-003**
(no users) and **AWX-005** (no role grants), the entire AWX authorization picture is invisible to the
mirror — which is the half a customer scrutinizes hardest before trusting a cutover.

---

## 3. Facet schema coverage — 2 of 9 · **AWX-014**

`contract.ValidateFacet` returns `covered=false` for an unregistered namespace
([contract.go:399-408](../../core/internal/contract/contract.go#L399-L408)) and the write proceeds
**unvalidated**. That is the accepted "owned-but-uncovered" state of §1.1 progressive hardening — a
namespace earns a schema when a Contract demands it, not before — but it should be _visible_, and until
now it was not written down anywhere.

| Namespace            | Pinned schema                                                    | Demander                                                                                        |
| -------------------- | ---------------------------------------------------------------- | ----------------------------------------------------------------------------------------------- |
| `ansible.playbook`   | 🟢 [schema](../../contracts/facets/ansible.playbook.schema.json) | the content half's projection Contract                                                          |
| `ansible.schedule`   | 🟢 [schema](../../contracts/facets/ansible.schedule.schema.json) | the `awx-schedule-enabled` Baseline + cutover                                                   |
| `ansible.template`   | 🔴 none                                                          | — (the `awx-template-covered` Baseline reads relations, not content, so it does not demand one) |
| `ansible.workflow`   | 🔴 none                                                          | —                                                                                               |
| `ansible.org`        | 🔴 none                                                          | —                                                                                               |
| `ansible.team`       | 🔴 none                                                          | —                                                                                               |
| `ansible.role`       | 🔴 none                                                          | —                                                                                               |
| `ansible.collection` | 🔴 none                                                          | —                                                                                               |
| `ansible.inventory`  | 🔴 none                                                          | —                                                                                               |

**Done for `ansible.template` (ADR-0128)** — and the ordering held: the schema landed _with_ the fields and
_with_ the Baseline that reads them. The remaining six are unchanged, and the same rule applies to each:
the schema lands when something consumes the namespace, not before.

---

## 4. The read-path asymmetry

Two paths read AWX, with different breadth, and nothing reconciles them:

| Path                                                                                         | Endpoints                                                                                                               |
| -------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| **Projection** ([controller/types.go](../../plugins/ansible-automation/controller/types.go)) | `job_templates`, `workflow_job_templates`, `schedules`, `organizations`, `teams`, `credentials`, `users`, `labels`, `execution_environments`, `notification_templates` — **10**                                |
| **adopt deep-read** ([awxapi/](../../plugins/ansible-automation/controller/awxapi/))         | those + `projects`, `inventories`, `credentials`, `survey_spec`, `workflow_nodes`, `inventory_sources`, `hosts` — **9** |

**Update (2026-07-26):** proving this seam found the asymmetry had also reached the **test harness**.
`awxsim` — the shared dev/test stand-in — served only the adopt deep-read's endpoints, so the projection
path could not run against it at all (`Enumerate` fails the whole Observe on any one collection's 404).
The two halves of one module were exercised by two different simulators of different breadth. `awxsim` now
serves `/schedules/`, `/organizations/`, `/teams/` and the `summary_fields.organization`/`.project` the
projection reads, and `controller/awxsim_projection_test.go` runs the real projection against it. A third
defect fell out: the projection client assumed `next` page links are root-relative, while the deep-read
client tolerated absolute or relative — so an absolute link (a Controller behind a proxy, or the sim)
produced `http://host:portt://host:port/...` and an "invalid port" error naming neither the endpoint nor
the cause. Both clients are now defensive.

Most of the difference is correct and intended: `inventories`/`credentials`/`hosts`/`survey_spec` are
`mapped` — they become Views, CredentialRefs, and `Workflow.inputs` at adopt and were never meant to be
mirrored as themselves. **Two are not explainable that way**: `projects` (**AWX-001**) and `workflow_nodes`
(**AWX-002**). Both are estate structure the graph should hold, both are already being fetched and parsed
by code in the same module, and neither absence appears to have been decided — it reads as the projection
having been built first and never revisited when the transform grew deeper.

---

## Prioritized gaps

**Tier 1 — the mirror cannot answer questions an operator will ask on day one:**

- ~~**AWX-010 · Job-template field depth.**~~ — **done, [ADR-0128](../adr/0128-ansible-template-projection-depth.md).**
  Run state + run knobs on the facet, credential usage as an `uses-credential` edge onto a new
  `ansible.credential` mirror, and the pinned closed schema the namespace never had — which also closed
  `AWX-014` for it. Shipped **with** its §1.1 consumer (`awx-template-failing`), because the reason the
  schema was missing is that nothing consumed the projection: thin projection → no consumer → no schema
  was one loop, and deepening without shipping a consumer would have left it.
- ~~**AWX-002 · Workflow topology.**~~ — **done, [ADR-0129](../adr/0129-workflow-topology-projection.md).**
  `invokes` edges (so blast radius is a reverse traversal, matching the credential question) plus the
  approval-gate fact. The node graph's SHAPE is deliberately still not projected: that is cutover
  fidelity, which adopt reads from AWX directly, and the mirror exists for governance. Costs an **N+1
  read** — one request per workflow, every poll — recorded in the ADR's consequences rather than hidden.
- **AWX-001 · `ansible.project` + `scm_revision`.** Already booked by ADR-0127 D4 and unchanged by this
  audit — it repairs ADR-0085's soundness, and it deserves its own ADR.

**Tier 2 — the authorization picture:**

- ~~**AWX-003 / AWX-004 · Users and team membership.**~~ — **done,
  [ADR-0130](../adr/0130-awx-local-accounts-and-team-membership.md).** It turned out **not** to be one
  slice of three: reading the prior art converted the caution into hard constraints. `identity.subject`
  has a **single write-owner** (the SCIM projector, §2.1 / ADR-0079 slice-3 gate), so AWX users could not
  be projected as `user` Entities even in principle — and should not be, because an AWX local account and
  an IdP identity are facts from two systems of record. They project as `ansible.user`, membership is a
  `has-member` edge, and INV-3 keeps all of it structurally out of the authz path.
- **AWX-005 · Role grants** stays open, now with a stated reason rather than an omission (ADR-0130 D3).
- ~~**AWX-011 · Last-run status.**~~ — **done by [ADR-0128](../adr/0128-ansible-template-projection-depth.md) D3**,
  which is where the distinction this row drew was cashed in: four scalars of current state on the template,
  never an event table.

**Tier 3 — mapping questions nobody has asked (🟠, and that is the finding):**

- ~~**AWX-006** labels~~ — **done, [ADR-0132](../adr/0132-awx-labels-and-schedule-shape.md) D1**, and it
  was not the mechanical copy this row assumed: an AWX label **cannot** be a graph label key, because a
  plugin's label keys are a static grant allowlist registered per key (ADR-0041 single-owner) and a key
  discovered at read time is ungrantable. Labels are Entities with `has-label` edges, so an operator's AWX
  grouping vocabulary becomes Stratt Views by topology selection — see `estate/views/awx-prod-templates.yaml` ·
  **AWX-007** execution environments · **AWX-008** instance groups →
  Sites/Cells · ~~**AWX-009** notification templates → Sinks~~ — **done (2026-07-31)**, and the
  §2.5 line is the whole design: AWX encrypts `token`/`password` and returns the REST IN THE CLEAR,
  so "project what AWX did not encrypt" would have imported working webhook credentials into the
  graph — a Slack incoming-webhook URL carries its token in the path. `configKeys` keeps the key
  NAMES (what a Sink declaration has to restate) and the projecting Go type has **no field the
  values could live in**, so the property is structural rather than a habit in the normalizer.
  Attachments (which template notifies through which, on started/success/error) are absent BY
  BUDGET: AWX exposes them only as three sub-resources per template, so the edge costs
  3×len(job_templates) per poll — the different-order-of-cost ADR-0131 refuses by default ·
  ~~**AWX-007** execution environments~~ — **done, [ADR-0133](../adr/0133-execution-environments-and-instance-groups.md) D1** ·
  ~~**AWX-008** instance groups~~ — **declined, D4**, and the declining is the decision: it stays 🔴/⚪ rather
  than 🟠, because "nobody looked" and "we looked and said no" must never render the same ·
  **AWX-012** custom credential types · ~~**AWX-013** schedule `extra_data` + timezone~~ — **done,
  [ADR-0132](../adr/0132-awx-labels-and-schedule-shape.md) D3**: timezone/next-run/window plus the
  per-schedule launch overrides, and `extraDataKeys` — **key names, never values**, which distinguishes
  two schedules of one template while holding the §2.5 line ADR-0128 D4 drew for `extra_vars` ·
  **AWX-015** the ~15 `ask_*_on_launch` booleans (deferred out of ADR-0128 D4: cutover fidelity rather
  than governance) · **AWX-016** workflow nodes as entities (deferred out of ADR-0129 D3 — it earns a
  namespace when a consumer needs the DAG, the obvious one being a UI rendering of a mirrored workflow
  beside a Stratt Workflow) · **AWX-017** correlating `ansible.user` to the SCIM identity — the AWX
  analogue of ADR-0079 4a's leaver-credential Finding, where **a local AWX account matching no known
  identity** is the account nobody offboards; it needs a username-resolvable identity key on `user`
  Entities, which is a decision about the identity plane · ~~**AWX-018** a **poll-cost budget**~~ — **done,
  [ADR-0131](../adr/0131-controller-poll-cost-budget.md)**, settled before a fourth N+1 landed. The
  finding was that three ADRs had each added an N+1 read against a Controller we do not own, each
  individually justified, with no decision owning the total — and the compounding half was worse than
  the traffic: because one failed read lost the whole Observe, **the mirror got less reliable the richer
  it got**. Expensive sub-reads now run on their own cadence (7 requests/poll steady-state instead of
  7+N+M), and a failed sub-read declines the full-sync boundary rather than losing the cycle. It also
  corrected ADR-0129, which had claimed partial-success needed a spine change: the port already had
  `full_sync_complete`, and this plugin's own empty-snapshot guardrail was already using it.

**Not gaps, recorded so they are not re-litigated:** inventories → View, hosts never re-projected,
credentials name-and-kind-only, survey passwords refused, activity stream not mirrored, ad-hoc commands
not an object. Each is a charter position with an ADR behind it.
