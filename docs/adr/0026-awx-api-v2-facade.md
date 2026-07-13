# ADR 0026 — AWX-compatible `/api/v2` façade (+ native cancel & ansible extraVars)

- **Status:** Accepted
- **Date:** 2026-07-13
- **Deciders:** Project steward (dstout)
- **Charter sections:** §5.6 (AWX exodus, Flow 6), §1.5 (sovereign contracts —
  no external protocol load-bearing / no second source of truth), §1.6 (one
  Principal / authz / audit), §2.5 (secrets), §1.8 (never hide diagnosis), §2
  (vocabulary), §8 (Phase-2 promote enabler); ADR-0025 (importer), ADR-0009
  (authz), ADR-0010 (Run lifecycle)

## Context

Second half of the Phase-2 **promote enabler**. ADR-0025 shipped the importer
(AWX definitions → Stratt desired state); this slice ships the **runtime** half:
an AWX-24.6.1-compatible REST surface so existing tooling (awxkit, the
`ansible.controller`/`community.awx` collections, terraform-provider-awx, CI
scripts) keeps launching and polling jobs while pointed at Stratt during a
cutover (Flow 6: "keeps existing tooling alive during cutover").

Per §1.5 the façade is a **thin, stateless compat transport, never
load-bearing**: the native `/api/v1` stays the sovereign contract; the façade
presents Stratt objects as AWX objects and **stores no new truth**. Two native
gaps blocked a *useful* façade — AWX launches carry `extra_vars` (no home on the
ansible Contract) and AWX tooling cancels jobs (Stratt had the status but no
cancel path) — so both land here as first-class platform capabilities.

## Decision

1. **Package `core/internal/awxfacade`** — a hand-written `/api/v2` `ServeMux`
   (AWX's shape is fixed, not our OpenAPI) mounted alongside `/api/v1` in
   `Server.Handler()` from the **same** `Server` deps (Store/Bus/Temporal/
   OIDC/Authz) — nothing new constructed. AWX nouns live only in this wire layer
   (§2). Mapping: a single-actuation-Step **Workflow → job_template**, a **View →
   inventory** (`total_hosts` via `CountSelector`), a **Run → job**. Multi-Step
   Workflows → `workflow_job_templates` is a documented fast-follow.
2. **Auth reuses the identity seam — no new token/password store** (§2.5/§1.6).
   `Bearer <oidc-jwt>` → the existing OIDC resolver; `Basic` → the **password is
   verified as the bearer JWT** (username informational) so the collections'
   default Basic auth works without persisting anything; dev `X-Stratt-Principal`.
   The middleware stamps `authz.WithPrincipal`, so downstream identity/authz is
   identical to `/api/v1`. `ping`/`config`/index are unauthenticated (AWX
   contract).
3. **Stateless AWX integer ids.** AWX tooling requires integer ids (terraform
   `strconv.Atoi`, awxkit URL interpolation); Stratt objects are names/uuids.
   `awxID(s) = md5(s)[:4] big-endian & 0x7fffffff`. Names (Workflows/Views, few)
   reverse by list-and-match; uuids (Runs, many) reverse via a **twin IMMUTABLE
   SQL function** `graph.awx_run_id(uuid)` + a **functional index** (migration
   00014) — `GetRunByAWXID` is an indexed query. **No mapping table, nothing
   persisted** (§1.5): the id is a pure function of the uuid; the index stores no
   new datum (drop it and every id is still recomputable). Go↔SQL parity is
   verified live (`TestGetRunByAWXID`). Int31 collisions resolve to the newest
   Run sharing the id — correct for transient job polling.
4. **Launch + extraVars.** `POST /job_templates/{id}/launch/` resolves the
   Workflow's Step, merges AWX `extra_vars` (object **or** yaml/json string) into
   `params.extraVars`, and launches via the **shared `orchestrate.LaunchRun`**
   that `/api/v1 StartRun` now also uses — literally one launch path (§1.6). A
   contract violation on the resolved params surfaces the native pointer detail
   in AWX's error shape (§1.8); unsupported fields (limit/inventory/credentials)
   are echoed in `ignored_fields`, never silently dropped. The ansible Contract
   gains `extraVars` (v4), written to ansible-runner's native `env/extravars`
   (`--extra-vars`) in both the play and scm paths — this is also ADR-0025's
   deferred survey→params landing field.
5. **Native Run cancel.** `POST /runs/{id}/cancel` (native, and wrapped by the
   façade's `/jobs/{id}/cancel/`) calls `Temporal.CancelWorkflow`. The **Workflow
   is the single writer of terminal status** (§ lifecycle integrity): its
   cancellation handler runs cleanup on a **disconnected context** (`CleanupRun`
   → `dispatch.DeleteRunJobs` deletes the K8s Job(s) by the `stratt.dev/run-id`
   label + `FinishRun(canceled)`). The `Execute` activity now **heartbeats** (via
   a dispatch callback) so Temporal delivers cancellation promptly. The handler
   only signals — it never writes status — so there is no dual-writer race.
   **Authorization** matches `StartRun`'s posture exactly: authenticated, but not
   object-gated. Run/View-scoped execution authz (a `run`/`view` type in the
   OpenFGA model, which today defines only `principal`/`org`/`team`/
   `credential_ref`) is the deferred Phase-2/3 extension of the ADR-0009 model.
   Launch and cancel share one posture so the façade cannot be a *weaker* authz
   path than `/api/v1` (§1.6) — the charter-guardian's symmetry requirement — and
   gating on the unmodeled `run` type would fail-closed for every caller.

## Consequences

- **Live-verified (dev harness, real kind + EE):** the full AWX client happy
  path against the façade — `GET /api/v2/` index → `/ping/` (24.6.1, unauth) →
  `/me/` → `/job_templates/?name__icontains=` (name→id) → `POST launch/` with
  `extra_vars:{msg:"hello-from-awx"}` → poll `/jobs/{id}/` to `successful` →
  `/jobs/{id}/stdout/?format=txt`. **The extra_var reached ansible** (stdout
  shows `awx-extra-var=hello-from-awx` per host — a real Run, not a mock). A
  slow job: `running` → `can_cancel:true` → `POST cancel` `202` → `canceled` on
  the next poll, with the K8s Job deleted.
- **Read-only definitions:** no POST/PUT/DELETE of templates/inventories (they
  live in Stratt/Git now); launch + cancel are the only writes. `job_template`
  detail advertises `edit/delete:false`.
- **Deferred / fast-follow (documented, not faked):**
  - `workflow_job_templates` / multi-Step launch — a second object family
    (`workflow_jobs`) + threading launch `extraVars` through `DAGInput`.
  - Incremental stdout beyond `?format=txt`; the `job_events` surface.
  - Step `inputContract` binding to *enforce* imported survey Contracts against
    launch `extraVars` (ADR-0025 runway — extraVars gives them a home now, typed
    enforcement is separate).
  - Prompt cancellation of a play that emits no output for >HeartbeatTimeout: the
    heartbeat fires from the wait/log loops, so a fully-silent long task is the
    edge; a concurrent heartbeat ticker is the follow-up if needed.
- **Charter posture:** the façade holds no state and adds no source of truth
  (§1.5 — the id functional index is derived, not a mapping table); Basic-auth
  passwords are verified as JWTs, never stored (§2.5); one launch path + one
  `authz.WithPrincipal` + one `RunInput.Principal` audit stamp (§1.6); the
  extra_vars merge is launch-time parametrization (AWX semantics), not an
  implicit-precedence field (§2.4). AWX nouns and the `awx.`-prefixed migration
  identifiers (`graph.awx_run_id`, `run_awx_id_idx`) are vendor-prefixed
  compat-shim names, not Stratt core-model identifiers (§2).

## Runway after
Notifications (outbound Finding/Gate alerts); Intent kinds GA + object-locked
Evidence store (Phase 3); the deferred façade surfaces above.
