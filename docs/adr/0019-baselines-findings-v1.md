# ADR 0019 — Baselines + Findings v1: check-mode + tofu plan, flap damping

- **Status:** Accepted
- **Date:** 2026-07-12
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.2, §1.8, §2.4 (Baseline, Finding, Evidence), §3
  ("Baseline cadences"), §4.3 (flap damping), §5 Flow 2, §8 (Phase 2)

## Context

Charter §8 Phase 2: "Baselines + Findings v1 (check-mode + tofu plan) with
flap damping." §2.4 defines Baseline as compiled **or hand-written** (§6
ladder); this slice ships the hand-written rung — the Intent/Blueprint
compiler (a separate Phase-2 item) will emit into the same object. Flow 2 is
the spec: check-mode and `tofu plan` Baselines on one dashboard; "**`tofu
plan` on cron *is* drift detection — no special case.**"

## Decision

1. **Baseline (CaC, `baselines/*.yaml`):** `{name, viewName, actuator,
   params, slices, credentialRefs, principal, cron, paused, severity,
   dampingObservations, remediationWorkflow, framework}` — the §2.4 shape:
   View selector + check Step + remediation Workflow **ref** + cadence.
   Remediation is never auto-launched (Flow 2: behind Gates); the ref rides
   the Baseline for the UI/API to offer. CaC-only; `principal` is the
   ADR-0010 impersonation-via-Git posture unchanged.
2. **Checks are read-only by structure, not convention.** Validation accepts
   only Actuators with check semantics (ansible, opentofu); opentofu is
   pinned to `mode: plan`; ansible's `check` flag is **not declarable** —
   the platform forces it at launch (`checkRunInput`), and validation
   re-rejects any declaration that tries. Enforced at the CaC seam AND at
   launch.
3. **One drift signal:** a check Run's target reporting `changed`. Ansible
   check mode (`--check --diff`, params `check` — new pinned Contract
   version `ansible.input.v2`, v1 pin untouched) already means would-change;
   OpenTofu plan now escalates its workspace target to `changed` when the
   plan's `change_summary` (operation=plan) carries changes — semantically
   right everywhere, and exactly "no special case."
4. **Drift detail:** actuators emit per-event fragments
   (`Interpreted.Drift`) — ansible: task + tool diff; tofu: resource
   address/action and counts, **never planned values** (those stay only in
   the redacted plan-json event). The dispatcher accumulates per target,
   capped at 16KB with a visible `{"truncated":true}` marker (§1.8:
   truncation is never silent). `FinishRun` persists drifted targets under
   `summary.drift`; `RunAgainstView` now returns a compact `RunOutcome`
   (per-target statuses, entity resolution, drift) to parent workflows.
5. **Cadence:** `graph.baseline` is the projection of the Git declaration;
   a reconciler (the ADR-0010 pattern: `stratt-baseline-` prefix, hash memo,
   overlap-skip, prunes out-of-band schedules) projects it onto Temporal
   Schedules — §3's "Baseline cadences". The Schedule fires
   `RunBaselineCheck`: load (pinned into workflow state) → child
   `RunAgainstView` → `EvaluateBaseline`.
6. **Findings (`graph.finding`, §4.3 damping state machine):** one live row
   per (baseline, target) via a partial unique index; drifted observations
   increment `consecutive_drifted` (row born `pending`), reaching
   `dampingObservations` opens it; a clean observation deletes a pending row
   (never fired — no record owed) and **resolves** an open one (kept — the
   audit history; a re-drift opens a fresh row). Failed/unreachable checks
   record nothing: a broken check is evidence of neither. Severity and
   framework are pinned onto the row at observation time. Findings are
   derived records off the projector write path — rebuildable from Runs, no
   second truth (§1.2).
7. **Evidence v1** = the Finding's redacted, capped diff snapshot + `runId`
   (Finding → Run → task events, §1.8). The object-locked Evidence store is
   Phase 3 as charted. Runs gain a `baseline` column — the Baseline → Run
   descent rung, queryable at creation (the `triggered_by` pattern).
8. **API:** `GET /baselines`, `/baselines/{name}`, `GET /findings`
   (?baseline=&status=), `/findings/{id}` — the Flow-2 one-dashboard feed;
   the Findings UI screen (§3.1 center of gravity) is the recorded next UI
   slice. **CLI-parity fix:** the DesiredState wire schema had silently
   omitted `emitters` (slice-4 gap — a CLI apply would have pruned every
   Emitter); emitters and baselines are now both on the wire and in the
   PlanEntry kind enum.

## Consequences

- Flow 2 is live end-to-end (verified on the dev harness): a `tofu plan`
  Baseline opened a critical Finding on its second consecutive drifted plan
  (damping 2), a check-mode Baseline opened 50 cis-tagged per-Entity
  Findings with real before/after diffs, both on one `GET /findings` list;
  aligning the declared module resolved the Finding with the clean Run as
  evidence.
- Non-Entity targets (an opentofu workspace) produce Findings with an empty
  Entity ref — the §2.4 shape holds; the workspace→Entity binding follows
  the parametrized-Views design.
- Targets that leave the View between checks cause no transition (their live
  Findings persist until a check observes them again); a stale-membership
  sweep is a recorded follow-up.
- charter-guardian finding, fixed in-slice: ansible drift fragments
  originally persisted raw `--diff` before/after bodies — rendered file
  content that can carry secrets — into `finding.diff` (§2.5). Fragments now
  carry structure only (task + changed objects' headers), symmetric with the
  tofu posture; full diffs stay on the Run's event stream, where `no_log`
  remains the play author's control as for all ansible output. Noted,
  accepted: `finding.run_id` is `ON DELETE SET NULL` — if Run summaries are
  ever pruned the Evidence ref nulls rather than blocks (revisit with the
  Phase-3 object-locked Evidence store); workspace Findings carry no Entity
  ref until the workspace→Entity binding lands with parametrized Views.
- Follow-ups: Findings UI screen; Alertmanager-style notification Emitters
  on Finding open/resolve; the Intent/Blueprint compiler emitting compiled
  Baselines (claim types, ownership registry); stale-membership sweep;
  per-Baseline check metrics.
