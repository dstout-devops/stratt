# ADR 0043 — Cert-renewal Finding-GC (resolve Findings for tombstoned Entities)

- **Status:** Accepted
- **Date:** 2026-07-15
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.8 (the abstraction must never hide diagnosis), §2.4 (Finding/Evidence;
  abandoned state is never silent), §4.3 (Finding flap-damping), §1.2 (Findings are derived,
  rebuildable, off the projector write path); resolves the disclosed fast-follow in **ADR-0030
  §Consequences** ("resolve facet-baseline Findings whose target left the View — the highest-value
  follow-up"); builds on the cross-source liveness of ADR-0042.

## Context

A cert Entity's only identity key is `cert.serial` (certissuer normalizer), so **renewal mints a
brand-new Entity** — no key is stable across renewal (commonName is a mutable label, not correlation).
On the certissuer full-sync, `TombstoneAbsent("cert.serial", seen)` (ADR-0042) tombstones the old
serial's Entity, but its open drift/expiry **Finding lingered `open` forever**:

- `graph.finding.entity_id` is `text`, nullable, with **no FK/cascade** to `graph.entity` — a tombstone
  touches nothing.
- Finding resolution requires a *clean observation of the same (baseline, target)*, but
  `EvaluateFacetBaseline` resolves its target set via `ResolveSelector`, which gates `deleted_at IS
  NULL`. A tombstoned Entity is never observed, so its Finding is never resolved (ADR-0019: "an absent
  target causes no transition — an unreachable target must not flap-resolve").

A perpetually-open Finding about a cert that no longer exists is itself a §1.8 problem: a false live
signal that erodes trust in the compliance surface.

## Decision

**An idempotent, self-healing sweep resolves any open Finding whose Entity is tombstoned, stamping an
explicit reason** (§1.8). `Store.ResolveFindingsForTombstonedEntities`:

```sql
UPDATE graph.finding f
SET status='resolved', resolved_at=now(), last_observed=now(),
    consecutive_drifted=0, resolved_reason='entity-tombstoned'
FROM graph.entity e
WHERE f.entity_id = e.id::text
  AND e.deleted_at IS NOT NULL
  AND f.status <> 'resolved'
```

- **Estate-wide** (steward decision): resolves *any* entity-anchored Finding — cert drift AND check-Run
  compliance — whose Entity is tombstoned. Orphan (`assignment:*`) and workspace (entity-less) Findings
  are untouched (null `entity_id` never equals `e.id::text`); co-managed-still-live Entities
  (`deleted_at IS NULL`, ADR-0042) are untouched.
- **`resolved_reason` column** (migration `00025`, nullable) distinguishes `'entity-tombstoned'` from
  the normal `'observed-clean'` resolve (also stamped now, at the `RecordBaselineObservations`
  open→resolved branch). A NULL on a legacy resolved row reads back as observed-clean. Surfaced on
  `types.Finding.ResolvedReason`, `GET /findings`, and the findings UI so a tombstone-GC is visibly
  distinct.
- **A sweep, not an atomic write inside `TombstoneAbsent`.** Three reasons: (1) the tombstone tx's
  retracted-presence set includes co-managed Entities that *stay live* — keying resolution off it would
  wrongly resolve their Findings (the ADR-0042 union case); (2) a facet-baseline eval can commit a fresh
  open Finding *just after* the tombstone tx (a plausible renewal-time race), which a transition-only
  write would never catch — the sweep heals it by construction; (3) Findings are derived records **off**
  the projector write path (§1.2) — writing them inside the projector tx would cross that layering. The
  sweep runs once per desired-state reconcile cycle (leader-only, ADR-0040), beside the compiler's
  orphan-finding sweep; zero projector/syncer change.

**Evidence is untouched.** Resolving flips status + reason only; the sealed WORM Evidence bundle and its
`graph.evidence` manifest survive as the audit truth (ADR-0029). The resolved Finding row is kept.

## Charter posture

- **§1.8** nothing is hidden: the Finding resolves *with an explicit reason*, keeping its audit row and
  sealed Evidence fully descendable. What's fixed is the stale-forever-open false signal — itself a §1.8
  violation.
- **§2.4** the per-serial expiry Finding is a proposition about *that* cert Entity; once no Source
  observes it, the proposition is moot. "Does commonName X still have a valid cert" is a separate
  coverage concern owned by an Intent/coverage Baseline + the orphan mechanism — not this Finding. No
  successor heuristic is built on the mutable commonName label (it would be unsound — commonName is not
  unique and not a correlation key).
- **§1.2** Findings stay derived and rebuildable; the sweep is idempotent, so a rebuild converges
  (strictly more rebuild-friendly than a transition-only write).

## Alternatives considered

- **Atomic resolve inside `TombstoneAbsent`.** Rejected — wrong id set (co-managed live Entities),
  no self-healing for the post-tombstone race, and a §1.2 layering violation.
- **Successor-aware resolve** (resolve only if a live Entity with the same `cert.commonName` exists;
  else keep visible). Rejected — commonName is a mutable, non-unique label, so "successor exists" would
  be unsound; and a resolved-with-reason Finding + sealed Evidence already hides nothing.
- **Read-time gate** (treat a tombstoned Entity's Finding as resolved via a join at query time).
  Rejected — loses the durable `resolved_at`/reason audit stamp and silently breaks the materialized
  `status` reads (`OpenFindingCountsByFramework`, the `finding.open` Notice).
- **Drift-only scope** (leave check-Run compliance Findings open when their host vanishes). Considered;
  steward chose estate-wide — a resolved-with-reason Finding retains the full audit trail, and the sweep
  stays a single predicate.

## Reviews

- **charter-guardian:** _(recorded in the slice commit)_ — §1.8 resolve-with-reason + kept audit/Evidence
  hides nothing; §2.4 no unsound successor heuristic; §1.2 idempotent/rebuildable.
- **vocabulary-linter:** `resolved_reason` / `entity-tombstoned` / `observed-clean` clean — no banned
  term, no Named-Kind misuse.
- **No dependency-scout** — zero new dependencies (a column + a sweep query + wire + one reconcile call).

## Honest deferrals

- **Coverage Baselines** — that a cert *deleted without renewal* should surface a gap is a separate
  Intent/coverage + orphan concern, not this sweep.
- **Estate-wide legibility of compliance rollups (charter-guardian flag).** Estate-wide scope resolves
  a tombstoned host's open CIS/compliance Finding too, so a control failure drops out of
  `OpenFindingCountsByFramework` (which counts `open` only). This is correct for a decommissioned host,
  but a host tombstoned because *every* Source lost sight of it (union-over-Sources, so all Sources must
  drop it — ADR-0042) would have a real, unremediated failure resolved. It is **auditable, not hidden**
  (`resolved_reason='entity-tombstoned'` on the kept row + sealed Evidence), but the only *live-surface*
  signal is the reconcile-cycle log count. The systemic catch is the deferred **coverage Baseline**
  (a host/name that *should* exist and be compliant but doesn't); surfacing a "resolved-by-tombstone"
  count on the compliance rollup is a cheap future legibility add. Estate-wide is the steward's v1 choice.
- **Per-baseline opt-out** of tombstone-GC (estate-wide in v1).
- **`entity_id` index** on `graph.finding` — deferred until the sweep shows hot (the table is bounded).

## Consequences

A renewed cert's stale drift Finding now resolves within a reconcile cycle, stamped
`entity-tombstoned` and fully descendable (audit row + sealed Evidence retained), rather than lingering
open forever. The fix is estate-wide and self-healing, with zero projector or connector change, and it
closes the ADR-0030 fast-follow. The compliance surface stops reporting live drift about Entities that
no longer exist.
