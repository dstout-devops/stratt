# ADR 0010 — Triggers v1: the `schedule` kind on Temporal Schedules

- **Status:** Accepted
- **Date:** 2026-07-11
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.2, §1.8, §2 (Trigger), §2.5, §3, §8 (Phase 1 "Schedules")

## Context

The Phase-1 board lists **Schedules**. The charter's vocabulary already settles the
object: **Trigger** is the Named Kind — "anything that starts a Run: Temporal Schedule,
Emitter event × CEL rule, manual, API/MCP" (§2) — and "Temporal owns all lifecycle —
… schedules" (§3). Slice 5 delivered real service identities (OIDC) and server-side
authorization (OpenFGA), which scheduled Runs need to execute as someone.

## Decision

**Trigger v1 ships one kind, `schedule`, actuated as a Temporal Schedule.** The Trigger
is the umbrella object: Phase-2 event-driven kinds (Emitter × CEL) join it; nothing here
is schedule-shaped in the core model except the `schedule` kind's own fields.

1. **CaC-only, read-only API.** A Trigger declaration names the service **Principal**
   its Runs execute as. That binding is an impersonation grant, and StartRun itself is
   not yet authz-gated (View-scoped execution is the Phase-2/3 extension, ADR-0009). So
   Triggers are declared only in the Git desired-state repo (`triggers/*.yaml`) — review
   is the grant's authorization — and the API is read-only (`GET /triggers`,
   `GET /triggers/{name}`). API-declared Triggers arrive with View-scoped execution
   authz; this is a conscious deferral, not drift. Parse-time guard: `credentialRefs`
   without a `principal` fails the declaration (it could never pass the dispatch-time
   `use` check, §2.5).
2. **Two projections, one truth (§1.2).** Git declares → the desired-state engine
   projects into `graph.trigger` (plan/apply/prune, per-kind max-prune guard, same as
   Views and CredentialRefs) → a reconciler projects rows onto Temporal Schedules
   (id prefix `stratt-trigger-`). Both projections are desired-state diffs: creates
   *and* deletes, so an out-of-band schedule under our prefix is removed exactly like an
   out-of-band OpenFGA tuple.
3. **Drift detection via action-memo hash.** Cron does not round-trip through
   `Describe` (the server compiles it to a structured calendar), so the reconciler
   stamps a canonical-JSON declaration hash on the schedule's *workflow action* memo —
   the action is replaced whole on update, so the hash can never go stale (the
   schedule-level memo is create-only and would). Hash or paused-state drift → update.
4. **EnsureRun: schedule-fired Workflows own their Run row.** API launches pre-create
   the Run summary in the handler; a Schedule starts `RunAgainstView` directly with
   `RunID: ""`, and the Workflow's first activity creates the row — stamping
   `triggered_by` (a queryable column, the §1.8 Trigger → Run descent rung, also
   surfaced as `Run.triggeredBy` and in the terminal summary). The declared Principal
   rides `RunInput.Principal`; `ResolveCredentials` and the `use` check are unchanged.
5. **Policies:** `Overlap = SKIP` — an estate Run must never stack on itself; skips are
   visible in the schedule info (§1.8: hide mechanism, never diagnosis). Default catchup
   window (1 min). `paused:` is declarative, reconciled both directions.
6. **No new dependencies.** Temporal SDK v1.46.0 has first-class Schedules
   (`ScheduleClient`, `handle.Trigger()` for immediate fire — used by the e2e).

## Consequences

- Scheduled automation fires even while strattd is down (Temporal owns the schedule);
  Runs then wait for a worker. Missed fires beyond the catchup window are dropped and
  counted (`NumActionsMissedCatchupWindow`), visible in `GET /triggers/{name}`.
- `GET /triggers/{name}` merges the declaration with the observed schedule state
  (paused, next fire times, recent workflow ids). Temporal unreachable degrades the read
  to declaration-only rather than 5xx — the declaration is never hostage to the
  projection.
- Schedule List is eventually consistent (visibility store); convergence of prunes can
  take an extra reconcile cycle. Acceptable at reconcile cadence.
- The wire `DesiredState` carries `triggers` too: the `stratt` CLI sends the same Git
  checkout the controller reads, so plan/apply see one document shape (found in review —
  without it, a CLI apply would have pruned every Trigger it couldn't express).
- Follow-ups: claims→team/`principal` validation against known Principals (today any
  string is accepted; the `use` check still gates credentials at dispatch); Trigger
  pause via API as the first authz-gated write once View-scoped execution lands; a
  blast-radius cap on the schedule-prune loop itself (today the row layer's max-prune
  guard is the only cap — a store bug emptying `graph.trigger` would mass-delete
  schedules unguarded, charter-guardian flag).
