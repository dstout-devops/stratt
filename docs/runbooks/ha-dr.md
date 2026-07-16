# Runbook — High Availability & Disaster Recovery

Operational companion to [ADR-0040](../adr/0040-high-availability-and-disaster-recovery.md). This is the
"not a flimsy backup" half: the deploy topology, backup/PITR, tested failover, and DR drills. **An
untested plan does not count** — every procedure here has a rehearsal step.

> **Scope:** this runbook is **in-region HA + single-Cell DR**. For **multi-region** Cell failover and the
> fenced cross-Cell Source re-home, see the companion [cell-failover-drill.md](cell-failover-drill.md)
> ([ADR-0044](../adr/0044-control-plane-cells.md)).

## Availability targets

| Tier | Scope | RPO | RTO | Mechanism |
|---|---|---|---|---|
| **In-region HA** (the 99.9% SLO) | node/AZ loss | **≈ 0** | seconds–1 min | quorum sync + automatic fenced failover (Patroni + strattd lease) |
| **Cross-region DR** | region loss / logical disaster | seconds (async lag) | minutes | async replicas + human-authorized cutover |
| **Backup & Restore** (last resort) | data corruption / ransomware | ≤ backup interval | measured restore time | PITR from WORM backups |

## Deploy topology (in-region HA)

- **strattd:** `replicas: 3`, `leaderElection.enabled: true`, PodDisruptionBudget `minAvailable: 1`,
  pod anti-affinity across nodes, readiness `/readyz` / liveness `/healthz` (the Helm defaults when
  `leaderElection.enabled=true`). API + Temporal workers serve on all 3; the controllers run on the
  elected leader; kill the leader → a standby acquires the Lease within one `LeaseDuration` (~15 s).
  **Requires the OpenFGA server backend** (in-process authz is single-replica only).
- **Postgres:** primary + ≥2 sync standbys across 3 AZs, Patroni (or CloudNativePG) with a 3/5-node
  DCS (etcd) for **fenced** automatic failover; `synchronous_commit = remote_apply`,
  `synchronous_standby_names = 'ANY 1 (…)'` → RPO 0 in-region.
- **NATS JetStream:** R3 streams across 3 AZs.
- **Temporal:** ≥2 of each service (frontend/history/matching) over the HA Postgres.
- **Object store (Evidence):** zone-redundant, versioning + **Object-Lock (compliance mode) WORM**.
- **Kubernetes:** 3 control-plane nodes, etcd quorum, one cluster per region.

## Backup & PITR (3-2-1-1-0)

3 copies, 2 media, 1 off-site, **1 immutable/offline, 0 verified errors**.

- **Postgres:** continuous WAL archiving + periodic base backups (pgBackRest / CloudNativePG Barman) to
  the **WORM** object store in a second region. PITR gives arbitrary-second recovery — the only defense
  against a *logical* disaster (a bad `DELETE`/deploy that replication faithfully copies to every
  standby). **Replication is not a backup.**
- **Object store (Evidence):** already object-locked (ADR-0029) + cross-region replication.
- **NATS/Temporal:** durable state is in Postgres (Temporal) / re-derivable (NATS streams rebuild from
  the graph + Git); the graph itself is a **projection** (§1.2) rebuildable from Sources + Run
  provenance, so the irreplaceable state is Postgres + the Git declarations repo + the Evidence store.
- **Desired state** lives in Git (§1.2) — back up the declarations repo like any Git remote.

### Restore test (scheduled — the measured RTO)

Monthly, to a scratch namespace: restore the latest base backup + replay WAL to a target time, run
`strattd` against it read-only, verify `/readyz` + row counts + `VerifyAudit` (ADR-0034 hash chain).
**The measured restore duration IS your Backup&Restore RTO** — record it. A restore that isn't tested
is unknown.

## Failover procedures

### In-region (AUTOMATIC — no human)

- **Postgres primary loss:** Patroni fences the old primary (STONITH) and promotes a sync standby.
  Acknowledged commits survive (RPO 0). strattd reconnects via the VIP/service.
- **strattd leader loss:** the Lease expires (~`LeaseDuration`), a standby replica acquires it and
  `OnStartedLeading` restarts the controllers. API traffic never paused (served by all replicas).
- **AZ loss:** quorum survives on the remaining 2 AZs (Postgres, NATS, etcd). No action.

### Cross-region (RUNBOOK — human-authorized)

Do **not** automate this — a false failover on a transient partition is itself an incident. On a
declared regional loss:

1. **Declare** the disaster (on-call + a second approver — the authorization step).
2. **Fence** the old region (stop its ingress / scale strattd to 0) to prevent split-brain.
3. **Promote** the region-B Postgres async replica to primary (accept the bounded RPO = last
   replication lag). Promote the Temporal standby cluster (XDC). Point NATS at region-B streams.
4. **Repoint** strattd's `STRATT_*` endpoints (or fail over the VIP/DNS) to region B; scale up.
5. **Verify:** `/readyz` green on all replicas; `VerifyAudit` clean; a canary Run succeeds end-to-end;
   Sites reconnect (their pull agents survive the gap).
6. **Record** actual RPO (data-loss window) and RTO (declare→serving) against target.

## DR drills / game days (prove it, on a schedule)

- **Quarterly regional failover drill** in staging: execute the cross-region runbook end-to-end,
  measure actual RPO/RTO, file gaps. This is the only thing that makes the RTO number real (DiRT).
- **Monthly restore test** (above).
- **Chaos (continuous):** kill a random strattd pod (leader failover), a Postgres standby, an AZ; assert
  the SLO holds. Wire into CI/staging so failover paths are exercised before a real outage needs them.

## Monitoring / SLO

- Alert on: Postgres replication lag, sync-standby count < quorum, NATS stream under-replicated, the
  strattd Lease age / leader flaps, `/readyz` failures, WAL-archive failures, backup-age, and
  restore-test failures.
- Track the **99.9% 30-day SLO** (§7 promote gate: 43.8 min/month error budget) per bounded service
  class; burn-rate alerts on the budget.
