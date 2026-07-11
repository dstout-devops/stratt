# ADR 0004 — Postgres tooling: pgx queries, goose migrations

- **Status:** Accepted
- **Date:** 2026-07-11
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.4, §1.7, §3
- **Fulfils:** the ADR-0002 follow-up "pick the Go migration/query tooling (e.g. `pgx` +
  goose/atlas or sqlc) in a later ADR."

## Context

Phase 0 builds the graph plane on Postgres 18 (charter §3): Entities/Relations/Facets as
JSONB with GIN indexes and recursive CTEs, per-attribute Provenance, and the data-layer
write enforcement of §1.2. ADR-0002 fixed the driver family (`pgx` is named in
`.claude/rules/backend-go.md`) but deferred the migration/query tooling. The graph's
load-bearing queries (View selectors over JSONB, ownership-registry checks) are dynamic
and hand-tuned — exactly where query-codegen tools help least — so the real choice is the
migration runner.

dependency-scout evaluated `pressly/goose/v3` against `golang-migrate/migrate`
(2026-07-11): both MIT, both `database/sql`-based, equivalent pgx-v5 and `go:embed`
support. goose: releases monthly-to-bimonthly with no gap > ~4 months since 2021, no
breaking change inside the v3 line in 5 years, library-first API (`goose.NewProvider`
takes an `fs.FS` directly). golang-migrate: a documented 13-month release stall
(v4.15.2→v4.16.0), latest release ~7.5 months old, 476 open issues across 25+ database
drivers Stratt will never use — unused attack surface, and the stalled-cadence pattern
§1.7 screens for. Caveat on goose: effectively a single active maintainer (`mfridman`);
no cosign/SBOM on releases (build-time dep; Go module checksum DB covers tamper-evidence).

## Decision

1. **Queries are hand-written SQL against `pgx/v5`** (native `pgxpool` at runtime). No
   query-codegen layer; no ORM.
2. **Migrations are hand-written SQL run by `pressly/goose/v3`**, embedded via `go:embed`
   and executed programmatically (startup/CLI), pinned at v3.27.2. goose sees the pool
   through `pgx/v5/stdlib.OpenDBFromPool` — migration-run time only; runtime query code
   stays native pgx.
3. **The data-layer write enforcement of §1.2 lives in the SQL itself** (constrained
   write paths, constraints, ownership checks in the schema), so it survives any future
   tooling swap.

## Charter alignment

- **§1.4 boring spine:** two small, single-purpose libraries; no codegen step, no DSL.
- **§1.7 evergreen:** goose's cadence and clean in-major track record are the evidence;
  pinned version rides the quarterly train with `govulncheck`.
- **§1.2 (via rules):** enforcement in the schema/SQL, not in a tool's abstraction.
- No Founding Discipline or non-goal is touched.

## Consequences

- **Positive:** migrations are plain SQL files a reviewer reads directly; the runner is
  embedded (single static binary, no sidecar tool in production); native-pgx hot path.
- **Negative / trade-offs:** no compile-time query checking (accepted: the hard queries
  are dynamic anyway; integration tests against real Postgres are the check). Single-
  maintainer bus factor on goose (accepted for a startup-time dep; see follow-ups).
- **Follow-ups:** add goose + pgx to the quarterly evergreen train; never auto-merge a
  goose major bump — manual N-1 migration-set test first; run the migration suite in CI
  on every PR touching `migrations/`; revisit sqlc only on measured pain with hand-written
  static queries.

## Alternatives considered

- **golang-migrate/migrate** — rejected: stalled-cadence history (13-month gap), large
  unused driver surface, no advantage in pgx or embed support.
- **sqlc (+ goose)** — rejected for now: the graph's center-of-gravity queries are
  dynamic JSONB/CTE constructions sqlc cannot express; a codegen step bought little.
- **atlas** — rejected: declarative-diffing power Stratt doesn't need yet, heavier tool
  with a commercial steward — weaker §1.4 fit than plain SQL files.
