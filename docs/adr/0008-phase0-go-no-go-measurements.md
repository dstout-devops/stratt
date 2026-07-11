# ADR 0008 — Phase-0 go/no-go gate measurements: graph spine proves out

- **Status:** Accepted (steward declared GO at the §8 gate, 2026-07-11)
- **Date:** 2026-07-11
- **Deciders:** Project steward (dstout)
- **Charter sections:** §8 (Phase-0 gates), §1.2, §1.8, §2.1, §3

## Context

Charter §8 defines the Phase-0 spike as the thesis slice — Entity/Facet/Provenance store →
native vCenter-class Syncer → View query → Temporal Workflow → K8s Job (ansible-runner)
against the View → facts projected back with provenance → live SSE tail — and gates the
project's continuation on three measurements. If the spine had not proven out, the project
was to retreat to job-runner scope *at this gate, explicitly*.

The slice was exercised end-to-end inside the devcontainer harness (ADR-0007: vcsim
`v0.55.1`, kind `v0.32.0`; dev substrate: Postgres 18.1, NATS 2.12.2, Temporal 1.29.1).
`strattd` ran the full pipeline: vcsim full sync (4 hosts, 50 VMs) → `dev-vms` View →
`POST /api/v1/runs` → K8s Job in kind running `ansible-runner` (gather-facts, 50 targets,
local connection) → `os.kernel` Facets written back with Run provenance → SSE tail
replayed and followed the complete event stream to `stream-end`.

## Measurements

| §8 gate | Budget | Measured | Verdict |
|---|---|---|---|
| View query @ 50k Entities | < 200 ms | 23 ms avg / 26 ms worst over 10 rounds, 5,000-row match set (`TestViewQueryGate`) | **PASS** (~8× headroom) |
| Pod-spawn p95 (Job create → pod running) | < 5 s | 765 ms p95 (n = 10 consecutive Runs; session-wide max 768 ms, n = 21) | **PASS** (~6.5× headroom) |
| Projection freshness | — | Full sync 50 VMs ≈ 194 ms; external power-off visible in the graph (provenance-stamped) in **32 ms** via the PropertyCollector delta watch | **PASS** |

Repeatability: after the defect fixes below, 10/10 consecutive Runs succeeded with zero
Temporal activity errors; every Run's facts landed as Facets stamped `writerKind: run`.

## Defects found and fixed by the measurement run

Exercising the slice against real substrate surfaced five defects (none reachable by
unit tests alone):

1. **View re-declare collided in `view_history`** — the BEFORE INSERT OR UPDATE history
   trigger also fired on the insert arm of `ON CONFLICT` upserts. Split into a
   BEFORE UPDATE version-bump trigger and an AFTER INSERT OR UPDATE history trigger.
2. **`DeclareView` version churn** — an unchanged selector still bumped the version;
   the Phase-1 Git sync controller re-declares every reconcile. Now a version-stable
   no-op (`IS DISTINCT FROM` guard); Views stay versioned on *change* (§2.1).
3. **Dispatcher activities were not idempotent** — a Temporal retry of `Execute` failed
   on `AlreadyExists` for the Run's ConfigMap/Job. Both creates now adopt the existing
   object (names derive from the Run id).
4. **Run event stream could exceed NATS max_payload** — a fact-rich `runner_on_ok`
   event killed the publish and failed the Run. Dev NATS `max_payload` raised to 8 MiB
   (matching the dispatcher's event-line buffer); JetStream `MsgID` (`runID/seq`) added
   so retry re-publishes dedup server-side instead of duplicating the stream (§1.8: the
   descent stays complete and exact).
5. **Gate test measured a zero-row query** — the 50k seed's `prod ∧ linux` selector
   matched nothing by construction; the seed now yields a 5,000-row match set so the
   gate measures real result assembly.

## Consequences

- **Positive:** the graph-spine thesis holds with wide margins; Phase 1 can start on a
  spine whose §8 gates are demonstrated, not assumed.
- **Negative / caveats:** vcsim is not the delta-path oracle (ADR-0007) — freshness and
  delta numbers must be re-validated against a real vCenter before the delta path is
  called production-grade; measurements are single-node devcontainer numbers, not a
  loaded-cluster benchmark.
- **Follow-ups:**
  - Oversized fact/artifact payloads offload to the object store (charter §3:
    facts/artifacts → S3) instead of riding the event bus; the 8 MiB dev limit is a
    stopgap, noted in `deploy/dev/nats.conf`.
  - Periodic real-vCenter smoke test for the PropertyCollector delta path (ADR-0007
    follow-up, unchanged).
  - Wire the §8 gate tests into CI against a service container so the gates stay
    regression-guarded.
