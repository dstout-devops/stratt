# Runbook — Cell failover & fenced Source re-home drill

Operational companion to [ADR-0044](../adr/0044-control-plane-cells.md) (control-plane Cells) and the
slice-7 fenced re-home. This is the multi-region half of [ADR-0040](../adr/0040-high-availability-and-disaster-recovery.md)
one level up: a Cell is a region-local single-writer shard with its own boring-spine substrate; the fleet
is many Cells presenting one logical estate. **An untested plan does not count** — every procedure here has
a rehearsal step, and the 99.99% target is only claimable once the drill below passes.

## What a Cell is (and is not)

- A **Cell** owns its own Postgres / NATS / Temporal / OpenFGA / object store (charter §2.3). Per-Cell
  in-region HA (Patroni fenced failover + the strattd lease) is unchanged from ADR-0040 — that is a
  **within-Cell** DR event, bounded blast radius, and stays automatic.
- **Cell failover is NOT automatic.** Promoting a Cell's cross-region DR replica set is human-authorized: a
  false failover on a transient partition is itself an incident (ADR-0040 doctrine, applied per Cell).
- There is **no datum multi-master.** Every datum has exactly one home Cell that is its sole writer. The
  only tool that moves a datum's home is the **fenced re-home** below.

## A. Per-Cell in-region HA

Each Cell runs the ADR-0040 topology independently. Nothing changes except the collision-safe names a named
Cell uses (ADR-0044 slices 1/6): leader lease `strattd-leader-<cell>`, Temporal namespace `stratt-<cell>` /
queue `stratt-runs-<cell>`, and NATS subjects/streams scoped by the Cell token (`stratt.<cell>.run.>`,
`STRATT_DISPATCH_<cell>`, …). Set `STRATT_CELL_ID` (and, if the Cell declares one, `STRATT_CELL_DISPATCH_PREFIX`)
identically on the Cell's strattd **and every Site agent in that Cell**.

## B. Cross-region DR replica per Cell (endpoints are env strings)

DR is done by **repointing the existing env strings** to the Cell's promoted replica set — there is no
separate `_REPLICA` variable (ADR-0044 decision point 10; ADR-0040 §1):

| Substrate | Primary | DR replica | Cutover |
|---|---|---|---|
| Postgres | in-region quorum sync | async streaming replica + PITR/WAL to WORM | promote replica → repoint `STRATT_DATABASE_URL` |
| Temporal | active cluster | XDC standby | promote standby → repoint `STRATT_TEMPORAL_ADDRESS` |
| NATS JetStream | in-region | mirror | repoint `STRATT_NATS_URL` (agents follow their Cell) |
| Object store | in-region | cross-region replication (CRR) | repoint `STRATT_EVIDENCE_*` |
| OpenFGA / OIDC | **global** (shared-fate, per ADR-0044) | per-Cell read-replica + active-passive DR | — |

Helm exposes these under the chart's existing substrate values; a named Cell sets `cell.id` (+ optional
`cell.dispatchPrefix`, `cell.secretName`) — see `deploy/charts/stratt/values.yaml`.

### Cell failover drill (rehearse quarterly)

1. Announce a maintenance window; confirm the target Cell's DR replicas are caught up (lag within RPO).
2. Fence the failing Cell's writers (stop its strattd, or isolate its substrate) — **never** run two
   primaries for one Cell.
3. Promote the DR replica set (Postgres, Temporal XDC, NATS mirror, object CRR).
4. Repoint the Cell's `STRATT_*` env strings to the promoted endpoints; restart the Cell's strattd.
5. Verify: `GET /readyz` green; a federated `GET /api/v1/runs` from a **peer** Cell returns 200 (not 206),
   confirming the re-promoted Cell rejoined the logical estate; a test Run against a View homed on the Cell
   completes.
6. Record RPO/RTO actuals against the 99.99% target.

## C. Fenced Source re-home (moving an estate partition between Cells)

Re-home moves a **Source** — and thus its Entities' residency — from one Cell to another **without ever
permitting two writers** (ADR-0044 slice 7). The unit is the Source, never a bare Entity: an Entity is a
projection of its Source, so the destination Cell **re-projects** the Entities natively (rebuildable), and
the source Cell tombstones its now-unobserved copies (`resolved_reason='entity-rehomed'`).

### Preconditions

- The caller holds the `rehome` grant on the destination `cell:<dest>` (deny-by-default, §2.5).
- The destination Cell can resolve the Source's `CredentialRef` against **its own** Secrets (material never
  ships — only the CredentialRef name, §2.5). Until [ADR-0045](../adr/0045-db-driven-syncer-home-gate.md)
  lands, **deploy/enable the Source's Connector on the destination Cell first** (env-instantiated Syncers);
  the fence guarantees no double-writer regardless of timing.

### Procedure

1. Trigger: `POST /sources/{name}/rehome  { "destCell": "<dest>" }` against any Cell (it forwards to the
   Source's home Cell). Returns **202** — the durable `RehomeSourceWorkflow` runs on the home Cell.
2. The workflow **seals** the Source (`rehoming_to=<dest>`, `home_epoch++`). Immediately, the home Cell's
   Normalizer projections of that Source are **rejected at the DB** (`enforce_write_path` seal fence) — the
   single-writer fence, a DB constraint. A §1.8 **stuck-seal Finding** opens (`framework='rehome'`,
   `target='source:<name>'`).
3. The workflow **forwards adopt** to the destination over the HMAC-signed, Principal-asserted PeerClient;
   the destination re-checks the `rehome` grant against the **global** OpenFGA and claims the Source
   (`cell=<dest>`, epoch-fenced). Its Connector re-projects the Entities. **Adopt is the point of no
   return** — after it commits, the move is roll-forward-only.
4. The workflow **completes**: tombstones the source Cell's now-unobserved Entities, resolves their
   Findings as `entity-rehomed`, drops the Source row, resolves the stuck-seal Finding. Audited
   `cell.rehome` on **both** Cells' hash chains (seal/complete on the source, adopt on the destination).
5. On a pre-adopt failure the workflow **aborts** (un-seal, `home_epoch++` to fence a stale late adopt);
   the Source resumes on its original Cell. On an adopt-then-complete failure the move is **not** aborted —
   Complete is idempotent; retry it.

### Failure modes & checks

- **Stuck seal** (partition, destination unreachable, Connector not yet deployed): the Source is frozen
  (zero writers — safe), surfaced by the open `rehome` Finding on `GET /api/v1/findings`. Resolve by
  restoring reachability (the workflow retries) or aborting the workflow (un-seal).
- **Verify success:** the Source appears on the destination (`GET /api/v1/sources` federates); a View homed
  via the Source resolves its Entities on the destination Cell; the source Cell's copies are tombstoned;
  two `cell.rehome` audit entries exist (one per Cell).
- **Never** clear `rehoming_to` by hand in the DB — that bypasses the fence. Use the workflow's abort.

### Re-home drill (rehearse quarterly)

1. On a two-Cell test fleet, register a Source on Cell A; let it project a handful of Entities.
2. `POST /sources/{name}/rehome {destCell: B}`; watch the `rehome` Finding open, then resolve on complete.
3. Assert: Entities present+home=B on Cell B; tombstoned on A; both audit chains verify
   (`GET /api/v1/audit/verify`).
4. Repeat with the destination deliberately unreachable to rehearse the stuck-seal → abort path; confirm A
   resumes writing the Source after abort.
