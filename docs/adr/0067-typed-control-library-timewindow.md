# ADR 0067 — The typed Control library: primitives as data, starting with TimeWindow

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout)
- **Charter sections:** §1.1, §1.4, §2.4, §5 (Flow-4 maintenance-window)
- **Implements:** ADR-0061 §7.3 (increment 1) · builds on ADR-0062/0063

## Context

Until now a `Control` was a raw CEL predicate (`When`) → outcome. ADR-0061 §7.3 calls for the **typed Control library** — Approval, SoD, TimeWindow, Waiver, Quorum, BreakGlass as first-class typed data, so a governance author *parameterises a primitive* rather than hand-writing CEL for each control (guardrail 1: parameterisation, not a DSL). This increment establishes the typed-control pattern and ships the first primitive, **TimeWindow** (the charter's §5 Flow-4 change-freeze / maintenance-window). TimeWindow is chosen first because it is a built-in must-have and, unlike SoD (which needs committer enrichment), it is **runtime-functional immediately** — it judges the run's logical time.

## Decision

**1. A `Control` is exactly one KIND** (validated at load, fail-closed): a raw CEL `When`, or one typed primitive (a nested optional spec pointer, the same house pattern as the mutually-exclusive Step shapes). Today: `When` or `TimeWindow`; SoD/Waiver/Quorum/BreakGlass add their own spec pointers as they land. The evaluator dispatches by kind (`controlFires`): a typed primitive is evaluated by **dedicated deterministic Go logic**, a raw control by the CEL engine. Typed primitives are **not lowered to author-visible CEL** — the framework owns their evaluation, keeping the surface typed and the vocabulary closed.

**2. `TimeWindowSpec` v1** is a recurring weekly window in UTC — `Mode` (`deny` blackout | `allow-only` maintenance), optional `Days` (sun..sat; empty = every day), and `[StartHourUTC, EndHourUTC)`. **No RRULE dependency yet** (dependency-scout deferral): days-of-week + an hour range covers the freeze/maintenance case without a calendar library; full recurrence is a later spec + its own dependency review. A blackout fires (applies its Outcome) when the run's time is **inside** the window; a maintenance window fires when **outside** it.

**3. TimeWindow judges the run's logical time deterministically.** `runPolicyStep` stamps `ChangeContext.ScheduledAt = workflow.Now(ctx)` — Temporal's replay-safe clock — so a freeze control evaluates against the real decision time without breaking workflow determinism. **An unset `scheduled_at` fails closed** (a window cannot be judged without a time — ADR-0061 M4).

## Charter alignment

Upholds §1.1 (a typed primitive at the seam — not a global ontology), §1.4 (the primitive is data the boring spine evaluates; no new dependency), §2.4 (the fixed most-restrictive lattice is unchanged — TimeWindow is just another control feeding it), and §5 (the maintenance-window guard the charter's Flow-4 named). No new Named Kind (`TimeWindowSpec` is a Control-payload shape). The closed-vocabulary discipline holds: a new primitive is a typed field + an ADR, never an inline escape hatch.

## Consequences

- **Positive:** governance authors can declare a change freeze / maintenance window as typed data (`{timeWindow: {mode: deny, days: [sat, sun]}, outcome: deny}`) instead of hand-writing time CEL; the typed-control dispatch pattern is established and unit-tested, so SoD/Waiver/Quorum/BreakGlass slot in the same way; TimeWindow is functional at runtime via `workflow.Now`.
- **Negative / trade-offs:** v1 windows are weekly-recurring UTC hour ranges — no arbitrary calendars/holidays/timezones until an RRULE spec + dependency review; the other five primitives are not built yet (this is increment 1 of §7.3).
- **Follow-ups:** the remaining §7.3 primitives — **SoD** (needs `ChangeContext.Committers` enrichment), **Waiver** (mandatory expiry, ADR-0061 guardrail 4), **Quorum** (M-of-N, which the ADR-0064 gate does not yet enforce), **BreakGlass** (bypass + mandatory post-review); RRULE/timezone windows behind a dependency-scout pass; ChangeContext enrichment (committers, blast-radius) is §7.6.

## Alternatives considered

- **Lower every typed primitive to CEL** — rejected for TimeWindow: recurrence/day-of-week logic is awkward and error-prone in CEL, and lowering would leak the primitive into author-visible predicates (guardrail 1). Dedicated deterministic Go is clearer and keeps the vocabulary closed. (CEL remains the raw-control escape hatch and the predicate sub-language.)
- **Adopt an RRULE library now** — deferred: a calendar dependency needs a dependency-scout pass; the weekly-window v1 covers the common freeze/maintenance case without it.
- **Evaluate the window against `time.Now()` in the activity** — rejected: the decision time must be the run's logical time and deterministic; `workflow.Now` is the replay-safe source, stamped on the workflow goroutine.
