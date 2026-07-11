# Competitive UX Teardown — Estate-Automation Control Planes

**Status:** Research input for the Stratt UX design foundation (pre-Phase-0).
**Method:** multi-source fan-out research, per-claim adversarial verification
(3-vote), sources tagged primary / blog / forum. 88 claims extracted; 70 upheld
high/medium confidence, 5 refuted and dropped. **Goal:** find exactly where these
"UX-last" tools fail so Stratt's schema-driven, descent-first UI can do better.

**Coverage note (no silent gaps):** AWX, HCP Terraform / Terraform Enterprise,
Backstage, Port, Spacelift, and Chef Automate yielded strong, sourced findings.
**Intune and Jamf Pro came back thin** — only a practitioner sluggishness complaint
for Intune and no verifiable Jamf UX specifics survived. Treat MDM-console findings
as under-researched, not absent; a follow-up pass should target them directly.

---

## AWX / Ansible Automation Platform

The most instructive case: **excellent descent granularity crippled by an
unreliable, capped, expensive streaming layer.**

- **IA / descent (good).** Clicking a job lands on its Job Output View — output is
  the default diagnostic surface. Three display modes (Stdout / Event-filtered /
  Advanced search); clicking any event opens a Host Events dialog (host, play, task,
  module + args, with JSON/Stdout/Stderr tabs); a Host Summary tallies the seven
  Ansible states. Users can expand plays/tasks and search by host/status/failure to
  isolate the exact failing task event. [AWX Jobs docs]
- **Live streaming (broken).** Live output stops updating / disconnects on longer
  jobs (10–20 min, ~200 hosts) with 10-min gaps ~20–30% of the time; the backend job
  keeps running — the failure is in the websocket UI layer — and refresh doesn't fix
  it. Persists 22.3.0 → 24.6.1, filed 2024-07-08, unassigned. A separate report:
  running-job logs don't follow stdout without a manual refresh, across FF/Chrome/
  Safari. [AWX #15342, #1831]
- **Scale (structural).** ~50 MB of output that takes 30 s raw took **30 min–1 hr**
  through AWX (tight 2 KB PTY read loop). The UI **truncates job events past a 4000
  default cap**, throttles websocket delivery to **30 events/sec**, and broadcasts
  every event to all control nodes regardless of subscription. AWX's own docs
  **recommend disabling live updates** to reduce overhead. [AWX #417, AWX Performance docs]

> **STEAL:** output-as-landing-surface; per-event host/play/task dialog; event-type
> filtering + host status summary; expand/collapse + search to isolate a task.
> **AVOID:** a streaming layer that silently freezes; **truncating the event log**
> (the 4000 cap literally hides the failing task — a §1.8 violation); throttles/
> broadcast fan-out that make "follow a running job at scale" the *expensive* path;
> pushing real diagnosis to the API/CLI because the UI isn't authoritative.

## HCP Terraform / Terraform Enterprise

The **structured-diff and drift gold standard** — undercut by out-of-product error
diagnosis and a paywall on drift.

- **Plan/diff/gate (excellent).** Default run plans first and waits for explicit
  **Confirm & Apply / Discard** (unless auto-apply). Refresh-only and speculative
  (read-only, policy-checked) modes treat drift/preview as a reviewable diff. A
  workspace serializes runs until the pending plan is resolved. [HCP run docs]
- **Machine-readable UI (the key lesson).** Terraform emits **structured JSON**
  (`-json`, one message/line) with typed messages: `planned_change`,
  `resource_drift`, `change_summary`, plus per-resource lifecycle events
  (`apply_start/progress/complete/errored`). Each change carries an **action enum**
  (create/update/replace/delete/…) **and a `reason`** (tainted, cannot_update…), and
  the format is **semver-versioned**. A UI renders diffs, drift, and live progress —
  and explains *why* — from typed data, never text scraping. [Terraform machine-readable UI]
- **Drift (strong, but gated).** Health assessments unify drift + continuous
  validation; estate-wide filtering by health outcome; per-resource attribute-level
  diff; when drift is found the UI **proposes the corrective change** (actionable,
  not passive). But it's **gated behind paid tiers** and buried behind per-workspace
  Settings enablement. [HCP Health docs, drift tutorial]
- **Diagnosis descent (weak in TFE).** Plan failures surface as **raw error strings**
  (`Killed`, `unexpected EOF`, `rpc error … context canceled` for OOM); guidance is
  literally "scroll up and read the log." Post-plan waits add perceived latency.
  [HashiCorp TFE troubleshooting]

> **STEAL:** typed, versioned machine-readable plan/drift/progress as the UI's data
> contract; action + **reason** so the UI explains *why*; plan-then-apply gate;
> estate roll-up → per-resource attribute diff; UI that *proposes the fix*.
> **AVOID:** raw-error-string diagnosis (pattern-matching cryptic text = abstraction
> hiding failure); **gating drift/diagnosis behind a paid tier**; burying drift
> behind per-object settings.

## Spacelift

- Drift via **scheduled** proposed runs (per-stack cron), not event-driven.
  Reconciliation reuses the normal run pipeline + approval gates. Drifted resources
  are marked in the Resources view and aggregated at stack/account level. [Spacelift drift docs]
- **AVOID (sharp one):** when drift detection finds **no** changes, there's **no
  proactive surfacing** — users must navigate to Runs and filter by the
  drift-detection parameter to even see it ran. A clean state should still be
  visible, not require a manual query.

> **STEAL:** drift reconciliation reuses the standard gated run pipeline (one state
> machine, not a parallel one). **AVOID:** making "everything's fine" invisible.

## Chef Automate

- **Post-hoc, not live:** nodes appear only after a converge finishes; no real-time
  stream. Three-level IA: estate node table (filtered by an aggregate status chart) →
  Node Details → Run History side panel. Filtered views are **shareable via a
  copyable URL**. [Chef Client Runs docs]
- Descent works: a failed run opens an error-log window with message + backtrace,
  downloadable. But change is shown as a **per-resource status matrix** (coarse
  state), **not a before/after content diff**.

> **STEAL:** linkable/shareable filtered diagnostic URLs; estate-table-filtered-by-
> status-chart IA. **AVOID:** post-hoc-only (no live follow); status-matrix instead
> of a real content diff.

## Backstage — schema-driven forms (precedent + cautionary tale)

- Scaffolder forms are generated from **JSON Schema via RJSF**; widget selection is
  steered declaratively by `ui:*` props (`ui:field`, `ui:options`); forms split into
  wizard steps; a review step **auto-masks sensitive fields**; validation can be
  **async** (call catalog/Utility APIs for live/remote checks). Custom **field
  extensions** (id + React component + validation fn) are the escape hatch, selected
  from inside the schema via `ui:field`. [Backstage templates/custom-field-extension docs]
- **The cautionary tale:** root-level **conditional fields (`allOf` + `if/then`)
  don't render** in Backstage even though the identical schema works in the RJSF
  playground; the acknowledged bug took **>10 months** to fix (#30090 → #31382). The
  `dependencies` workaround is less expressive.

> **STEAL:** schema + `ui:*` hints; declarative escape hatch (registered field
> extension named *in the schema*); async/remote validation; auto-mask secrets;
> multi-step wizards from an array of parameter groups. **AVOID:** assuming pure
> JSON-Schema covers every input — conditional logic is where naive schema-rendering
> breaks; design the escape hatch and conditional model up front.

## Port — presentation-in-schema

- Each **blueprint is a JSON Schema**; fields live under `properties`, each carrying
  **presentation metadata in the schema itself** (title, icon, description) plus a
  type. Typed property kinds (Object, Team, Enum, Timer, User) render from the type.
  **Relations declared declaratively** under `relations`. Schemas are even
  NL-generatable by AI. [Port blueprint docs]

> **STEAL:** put UI labels/icons/descriptions **in the schema**, so a plugin that
> ships a schema gets a labeled UI for free — exactly Stratt's "plugins ship schemas,
> not React." Declare relations in-schema, not in UI wiring.

---

## Synthesis — 10 highest-leverage principles for Stratt

Each maps to a charter discipline and to a concrete "avoid this."

1. **Live streaming is a reliability feature, not a nicety.** AWX proves a flaky/
   capped/expensive stream gets *disabled* — killing descent. Stratt SSE must be
   robust, backpressure-safe, and default-on at scale. *(→ §3.1, §1.8)*
2. **Never truncate the descent.** AWX's 4000-event cap hides the failing task.
   Virtualize the full event stream; the exact failing task event is always
   reachable. *(→ §1.8)*
3. **Render diffs from typed data, never scraped text.** Adopt Terraform's typed,
   versioned model (`planned_change` / `resource_drift` / `change_summary`, action +
   **reason**). Stratt's `PlanDiff` renders from Contract data and explains *why*.
   *(→ §1.1 type-the-seams, §3.1)*
4. **Diagnosis lives in-product.** TFE's raw-error-strings and AWX's API-only
   troubleshooting push users out. Descent to the failing task event is a first-class
   product surface. *(→ §1.8 "diagnosis is a product surface")*
5. **Drift = Findings: estate roll-up → per-Entity diff, and propose the fix.**
   Steal HCP's roll-up + attribute diff + corrective proposal; avoid Spacelift's
   invisible clean-state. *(→ §2.4 Finding/Evidence, §1.2 drift-is-the-diff)*
6. **Gate changes: plan-then-apply with explicit confirm.** Universal good pattern
   (Confirm & Apply). Maps to Workflows + Gates. *(→ §2.3)*
7. **Presentation metadata belongs in the schema.** Port/Backstage: title/icon/hints
   in the Contract/Facet schema → every Connector gets a labeled UI for free. *(→ §3.1)*
8. **Schema-driven forms need a declarative escape hatch and a conditional model.**
   Backstage's 10-month conditional-field bug is the warning. Ship `ui:*`-style hints,
   registered field extensions named *in the schema*, async validation, secret
   masking, and a designed conditional-field story from day one. *(→ §3.1, §1.5 pinned schemas)*
9. **No paywalled diagnosis or drift — ever.** HCP gates drift behind paid tiers;
   Stratt's no-gated-tier rule turns this into a differentiator. *(→ §1.3 rug-pull-proof)*
10. **Perceived latency is a trust surface.** TFE post-plan waits, Intune sluggishness,
    AWX's 50 MB collapse. Hold the charter's Phase-0 budgets (View < 200 ms @ 50k,
    pod p95 < 5 s), virtualize everything, and make every diagnostic state
    URL-addressable (Chef's shareable links). *(→ §8 gates, §3.1)*

---

### Sources

AWX: [#15342](https://github.com/ansible/awx/issues/15342) · [#1831](https://github.com/ansible/awx/issues/1831) · [#417](https://github.com/ansible/awx/issues/417) · [Jobs docs](https://docs.ansible.com/projects/awx/en/24.6.1/userguide/jobs.html) · [Performance docs](https://docs.ansible.com/projects/awx/en/24.6.1/administration/performance.html) · [troubleshooting](https://oneuptime.com/blog/post/2026-02-21-how-to-troubleshoot-awx-job-failures/view).
HCP Terraform / TFE: [run/UI](https://developer.hashicorp.com/terraform/cloud-docs/run/ui) · [run modes](https://developer.hashicorp.com/terraform/cloud-docs/workspaces/run/modes-and-options) · [health](https://developer.hashicorp.com/terraform/cloud-docs/workspaces/health) · [drift tutorial](https://developer.hashicorp.com/terraform/tutorials/cloud/drift-detection) · [machine-readable UI](https://developer.hashicorp.com/terraform/internals/machine-readable-ui) · [TFE plan failures](https://support.hashicorp.com/hc/en-us/articles/360059831653-Troubleshoot-Plan-operation-failures-in-Terraform-Enterprise) · [TFE post-plan slowness](https://support.hashicorp.com/hc/en-us/articles/4408247976723).
Spacelift: [drift](https://docs.spacelift.io/concepts/stack/drift-detection.html) · [TF Cloud drift](https://spacelift.io/blog/terraform-cloud-drift-detection).
Chef Automate: [Client Runs](https://docs.chef.io/automate/client_runs/).
Backstage: [templates](https://backstage.io/docs/features/software-templates/writing-templates/) · [field extensions](https://backstage.io/docs/features/software-templates/writing-custom-field-extensions/) · [#30090](https://github.com/backstage/backstage/issues/30090) · [Red Hat tips](https://developers.redhat.com/articles/2025/03/17/10-tips-better-backstage-software-templates).
Port: [blueprints](https://docs.port.io/build-your-software-catalog/customize-integrations/configure-data-model/setup-blueprint/) · [Backstage vs Port](https://medium.com/callibrity/internal-developer-portals-backstage-vs-port-do-you-really-need-one-d312d8f2797e).
Rundeck: [executions](https://docs.rundeck.com/docs/manual/07-executions.html). Intune: [sluggishness](https://yomotherboard.com/question/is-intunes-sluggishness-normal/).
