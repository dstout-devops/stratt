# Evidence — 99.99% multi-region availability path

Traces the charter §7 availability ladder from the 99.9% single-region SLO ([ADR-0040](../adr/0040-high-availability-and-disaster-recovery.md))
to the **99.99% multi-region** target, showing which built machinery discharges each requirement. The
availability *number* is claimable only once the drills in
[the cell-failover runbook](../runbooks/cell-failover-drill.md) pass on a real multi-Cell fleet; this
document is the design-completeness map, not a measured SLO report.

## The ladder

| Tier | SLO | Mechanism | Status |
|---|---|---|---|
| In-region HA | 99.9% | quorum sync + fenced failover (Patroni) + strattd lease, per Cell | ADR-0040, shipped |
| Multi-region | **99.99%** | many Cells, one logical estate; per-Cell DR; fenced re-home | ADR-0044 slices 1–7, code shipped; drill-gated |

## Requirement → evidence

Each row is a property the 99.99% claim depends on, and the shipped code/test that discharges it.

| Requirement (§) | Discharged by | Evidence |
|---|---|---|
| A region is an independent single-writer shard (§2.1) | Cell as a modeled concept + collision-safe control names | ADR-0044 slice 1; `graph.cell`, `STRATT_CELL_ID`, leader/Temporal/NATS scoping (slices 1/6) |
| Each datum has exactly one home Cell (§2.1/§2.4) | `home_cell`/`source.cell`/`run.cell` residency + placement Finding on mismatch | slice 2; `WriteCellPlacementFindings`, `entity_homing_test.go` |
| One logical estate across regions (§0/§1.6) | `cellrouter` scatter-gather reads + deterministic merge + partial-result honesty (206) | slice 3; `cellrouter_test.go` |
| One identity / authz / audit / cost model (§1.6) | global OIDC + global OpenFGA (authz-home leader) + federated audit/cost | slice 4 |
| Cross-region orchestration survives a Cell hop (§1.8) | `RunAcrossCells` scatter + `partial` status + descent federation | slice 5; `across_test.go` |
| Region-local execution, no work-queue widening (§1.4) | Cell-scoped NATS dispatch plane + Site→Cell binding | slice 6; `scope_test.go`, `SiteCellMisroute` |
| **A datum can move regions with NO double-writer (§2.1)** | **fenced Source re-home** (seal fence as a DB constraint) | slice 7; `TestSealFenceRejectsNormalizerWrite` (real-DB), `rehome_test.go` |
| A region loss is recoverable within RTO/RPO | per-Cell DR replica promotion (env-string repoint) | ADR-0040 substrate + cell-failover runbook §B |
| No silent availability-over-correctness trade | no multi-master anywhere; unreachable home = loud 503/206, never failover-to-a-second-writer | slices 3/5/7; `forwardWriteToPeers` 503 path |

## What still gates the measured number

- **Drills, not code.** The failover drill and the re-home drill (runbook §§B/C) must pass on a real
  two-Cell fleet with RPO/RTO recorded. Multi-region infra is a deploy exercise, not in this repo.
- **Shared-fate residuals (accepted, ADR-0044 residual tension 2):** global OpenFGA + global OIDC are the
  price of §1.6 "one model", mitigated by per-Cell read-replicas + active-passive DR — they cap the
  theoretical ceiling and are a deliberate correctness-over-availability choice.
- **Full re-home auto-cutover** ([ADR-0045](../adr/0045-db-driven-syncer-home-gate.md)) removes the one
  manual Connector-deploy step; not required for the 99.99% claim (the fence holds regardless), but it
  shortens re-home RTO.
