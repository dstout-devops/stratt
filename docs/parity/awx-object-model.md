# AWX object-model parity — object by object, field by field

**Subject:** the AWX **24.6.1** `/api/v2` object model — the API AAP's Automation Controller exposes and
the version charter §5.6 freezes the migration target at.

**Scope:** what the `ansible-automation` plugin **reads**, **projects**, and **transforms**, against what
AWX actually holds. The [platform audit](aap-2.7-platform.md) scores AAP by _component_ and finds the
Controller 🟢 code-complete; this one goes a level down and asks whether the graph can answer questions
about an AWX estate. Those are different questions and they have different answers.

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
| **Object coverage** (which objects at all) | 🟡 5 projected, 7 more read-only | The projection covers the orchestration spine; people, RBAC, and run history are absent          |
| **Field depth** on projected objects       | 🔴 **thin** — 3–5 fields each    | `ansible.template` projects 5 of ~50 job-template fields; no run state, no credentials, no knobs |
| **Workflow topology**                      | 🔴 **name only**                 | `ansible.workflow` is a name + description; the node graph is read by adopt and never projected  |
| **Facet schema coverage**                  | 🔴 **2 of 9**                    | Only `ansible.playbook` and `ansible.schedule` are pinned; 7 namespaces write unvalidated        |
| **Read-path symmetry**                     | 🟡 divergent by accident         | Projection reads 5 endpoints, adopt reads 9; nothing states which asymmetries are deliberate     |
| **`stratt adopt` transform**               | 🟢 deep and honest               | Reads what it needs, refuses what it must (secrets, password surveys), reports what it drops     |

**Bottom line.** The transform half is in good shape — `stratt adopt` reads deeply and its migration report
blocks rather than guesses. The **projection** half is a spine, not a mirror: it can answer "what
automation exists and what runs what," and it cannot answer "which templates use this credential," "which
are failing," "who is on this team," or "what does this workflow actually do." The single highest-value
fix is field depth on `ansible.template` (**AWX-010**), because most governance questions an operator
brings to an AWX mirror are field-level questions about job templates.

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
| `job_templates`                  | `projected` 🟢  | [types.go:91](../../plugins/ansible-automation/controller/types.go#L91) → `ansible.template`; field depth is **AWX-010**                                    |             |
| `workflow_job_templates`         | `projected` 🟡  | [types.go:94](../../plugins/ansible-automation/controller/types.go#L94) → `ansible.workflow`, name + description only                                       |             |
| `schedules`                      | `projected` 🟢  | [types.go:97](../../plugins/ansible-automation/controller/types.go#L97) → `ansible.schedule` + the `schedules` edge                                         |             |
| `organizations`                  | `projected` 🟢  | [types.go:100](../../plugins/ansible-automation/controller/types.go#L100) → `ansible.org`                                                                   |             |
| `teams`                          | `projected` 🟡  | [types.go:103](../../plugins/ansible-automation/controller/types.go#L103) → `ansible.team`; **membership is not read**                                      | **AWX-004** |
| `workflow_job_template_nodes`    | `adopt-only` 🔴 | [adopt_read.go:63](../../plugins/ansible-automation/controller/awxapi/adopt_read.go) builds the DAG for the transform; the projection never sees it         | **AWX-002** |
| `projects`                       | `adopt-only` 🔴 | ADR-0127 D4 already books `ansible.project` + `scm_revision`; this audit confirms the sizing                                                                | **AWX-001** |
| `inventories`                    | `mapped` ⚪     | → **View** ([materialize/views.go](../../plugins/ansible-automation/controller/materialize/views.go)); smart inventories reduce their `host_filter`         |             |
| `inventory_sources`              | `mapped` ⚪     | → points at the native Syncer for that cloud, never re-implemented as an AWX plugin                                                                         |             |
| `hosts` (inventory members)      | `mapped` ⚪     | Deliberately never re-projected — that is the writable-CMDB anti-pattern (§1.2); hosts come from their own Syncers                                          |             |
| `credentials`                    | `mapped` ⚪     | → **CredentialRef**, name + kind only, material never read ([credentials.go](../../plugins/ansible-automation/controller/materialize/credentials.go), §2.5) |             |
| `job_templates/{id}/survey_spec` | `mapped` ⚪     | → `Workflow.inputs` (ADR-0118 D2); a **password** question is refused, not imported                                                                         |             |
| `users`                          | `none` 🔴       | The mirror has teams and orgs but **no people**; "who can launch this" is unanswerable                                                                      | **AWX-003** |
| `roles` (RBAC grants)            | `none` 🔴       | No principal→object grant is read, so no AWX permission is visible or migratable                                                                            | **AWX-005** |
| `jobs` / `job_events`            | `none` ⚪🟠     | Run **history** is deliberately not mirrored — §3 forbids replicating AWX's job-events-table pathology. But **current/last status is not history**          | **AWX-011** |
| `labels`                         | `none` 🟠       | AWX labels are the operator's own grouping vocabulary; they would map to graph labels cleanly and nobody has looked                                         | **AWX-006** |
| `execution_environments`         | `none` 🟠       | Which EE a template runs in is invisible in the mirror; on our side EE is an Actuator declaration (ADR-0117 D3a), so the mapping is not obvious             | **AWX-007** |
| `instance_groups`                | `none` 🟠       | AWX's execution placement; our equivalent is Sites/Cells, so this is a real mapping question nobody has asked                                               | **AWX-008** |
| `notification_templates`         | `none` 🟠       | ADR-0125 made Sinks driver-shaped; importing AWX's notification config is now cheap and unexamined                                                          | **AWX-009** |
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

### `ansible.template` ← `job_templates` — 5 fields of ~50 · **AWX-010**

Projected ([normalize.go:50-56](../../plugins/ansible-automation/controller/normalize.go#L50-L56)):
`name`, `jobType`, `playbook`, `surveyEnabled`, `description` · labels `ansible.name`, `ansible.org` ·
relations `owned-by` → org, `runs` → playbook.

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

### `ansible.workflow` ← `workflow_job_templates` — name + description · **AWX-002**

Projected: `name`, `description` · `owned-by` → org. **That is all of it.**

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

Correctly-ordered consequence: closing **AWX-010** (template field depth) creates the demander that has
been missing for `ansible.template`, so the schema should land _with_ the fields rather than ahead of them.

---

## 4. The read-path asymmetry

Two paths read AWX, with different breadth, and nothing reconciles them:

| Path                                                                                         | Endpoints                                                                                                               |
| -------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| **Projection** ([controller/types.go](../../plugins/ansible-automation/controller/types.go)) | `job_templates`, `workflow_job_templates`, `schedules`, `organizations`, `teams` — **5**                                |
| **adopt deep-read** ([awxapi/](../../plugins/ansible-automation/controller/awxapi/))         | those + `projects`, `inventories`, `credentials`, `survey_spec`, `workflow_nodes`, `inventory_sources`, `hosts` — **9** |

Most of the difference is correct and intended: `inventories`/`credentials`/`hosts`/`survey_spec` are
`mapped` — they become Views, CredentialRefs, and `Workflow.inputs` at adopt and were never meant to be
mirrored as themselves. **Two are not explainable that way**: `projects` (**AWX-001**) and `workflow_nodes`
(**AWX-002**). Both are estate structure the graph should hold, both are already being fetched and parsed
by code in the same module, and neither absence appears to have been decided — it reads as the projection
having been built first and never revisited when the transform grew deeper.

---

## Prioritized gaps

**Tier 1 — the mirror cannot answer questions an operator will ask on day one:**

- **AWX-010 · Job-template field depth.** Credentials, run status, and the run knobs. Highest value per
  unit of work in this document, and it creates the missing demander for the `ansible.template` schema
  (AWX-014).
- **AWX-002 · Workflow topology.** The data is already fetched and parsed for adopt; the projection just
  never emits it. Until then `ansible.workflow` is not a governable Entity.
- **AWX-001 · `ansible.project` + `scm_revision`.** Already booked by ADR-0127 D4 and unchanged by this
  audit — it repairs ADR-0085's soundness, and it deserves its own ADR.

**Tier 2 — the authorization picture:**

- **AWX-003 / AWX-004 / AWX-005 · Users, team membership, role grants.** One coherent slice, not three.
  Needs a decision first, not code: AWX RBAC facts are exactly the kind of thing that must **not** become
  a second authorization truth beside OpenFGA (§1.2, §2.4). Project as observed foreign facts for
  visibility and migration planning, or deliberately don't — but decide it in an ADR.
- **AWX-011 · Last-run status.** Note the distinction the row makes: charter §3 forbids replicating AWX's
  job-events table, and this is not that. `last_job_failed` on the template is one boolean and it answers
  "what is broken."

**Tier 3 — mapping questions nobody has asked (🟠, and that is the finding):**

- **AWX-006** labels · **AWX-007** execution environments · **AWX-008** instance groups →
  Sites/Cells · **AWX-009** notification templates → Sinks (cheap since ADR-0125) ·
  **AWX-012** custom credential types · **AWX-013** schedule `extra_data` + timezone.

**Not gaps, recorded so they are not re-litigated:** inventories → View, hosts never re-projected,
credentials name-and-kind-only, survey passwords refused, activity stream not mirrored, ad-hoc commands
not an object. Each is a charter position with an ADR behind it.
