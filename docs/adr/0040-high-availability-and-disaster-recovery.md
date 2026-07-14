# ADR 0040 — High Availability & Disaster Recovery architecture

- **Status:** Accepted (Commit 1 — leader election + migration lock + readiness; Commit 2 — Helm HA
  hardening + this ADR + the runbook)
- **Date:** 2026-07-14
- **Deciders:** Project steward (dstout)
- **Charter sections:** §7 (promote gate: prod for a bounded service class + **99.9% 30-day SLO** +
  security review), §3 (Go control plane, K8s-native operator posture, client-go/controller-runtime),
  §1.4 (boring spine, few boring deps), §2.1/§2.4 (Provenance is exactly one answer; no implicit
  precedence), §1.2 (projections; enforced in the data layer), §1.8 (never hide diagnosis), §2.3 (Site
  = remote execution locus); ADR-0013 (Helm; the deferred leader-election rider), ADR-0032 (Sites),
  ADR-0034 (audit sealer — the single hash-chain writer)

## Context

HA/DR is on the promote-gate critical path (§7). The brief was explicit: rigorous, not "a flimsy
backup"; ideally **live/live/live** (active/active/active). Three research streams (industry HA/DR
standards, the boring-spine substrate's real capabilities, the current codebase posture) fixed both the
target and an honest verdict on live/live/live.

### Definitions (the dials)

- **RPO** (Recovery Point Objective) — max acceptable *data loss*, in time. Set by the replication
  mechanism: synchronous ≈ 0; async = replication lag; backup = backup interval.
- **RTO** (Recovery Time Objective) — max acceptable *time to restore service*. Set by
  detect+decide+cutover speed. RPO and RTO are **independent dials** (AWS Well-Architected REL13).
- **HA** = surviving component/node/AZ failure **within a region**, automatically, RPO≈0 / RTO seconds.
  **DR** = surviving loss of a **whole region** (or a *logical* disaster: bad deploy, corruption,
  ransomware), a deliberate failover with a larger, defined RPO/RTO. They need different mechanisms and
  different testing; a multi-AZ cluster is HA, a tested cross-region cutover is DR.
- Availability budgets: **99.9% = 43.8 min/month**, 99.99% = 4.38 min/month, 99.999% = 26 s/month —
  each nine leaves no room for a human in the loop at the tighter end. ISO 22301 (BIA/MTPD) is the
  governance vocabulary: derive RTO/RPO from impact analysis and **prove the plan by test**.

### The live/live/live verdict

Stateless tiers are trivially active/active. The **stateful/data tier is the whole difficulty**, and
"live/live/live" is a claim about its *write path*. For Stratt's deterministic core it is **not
achievable on the boring spine, and is the wrong goal**:

- **Temporal is the hardest wall.** A Global Namespace is **active in exactly one cluster**; cross-DC
  replication (XDC) is **asynchronous and experimental** — so self-hosted OSS Temporal cannot be
  live/live/live for a namespace *regardless* of the database under it. This is a design invariant of
  the workflow engine, not a storage choice.
- **Multi-master Postgres** (Spock/pgactive/BDR) is async, conflict-resolving, extension-land — **not
  boring** (§1.4 violation) and **semantically wrong** for a deterministic control plane: it breaks the
  linearizable single-writer the orchestration engine and the Provenance model depend on. §2.1
  ("exactly one answer") and §2.4 (no implicit precedence) *forbid* last-writer-wins across writers —
  which is exactly what async multi-master reintroduces.
- **CAP/PACELC** guarantee you pay one of two currencies, always, *even on a healthy network*: either
  cross-region quorum **latency** (tens–100+ ms/commit; the speed of light is not negotiable) or
  **silent data loss** (LWW) / a CRDT-only data model. Stratt's correctness model cannot spend the
  second currency, and the deterministic core should not spend the first on every write.

Adopting CockroachDB/Spanner/pgEdge to force multi-master would each violate §1.4 and, for Temporal,
still wouldn't deliver active-active. **So we do not chase multi-master.** What we deliver instead is
more honest than vendors who say "active-active" but mean async LWW.

## Decision

**Target: region-local quorum HA (RPO≈0) + active/active stateless serving + async *tested* cross-region
DR + "live/live/live in aggregate" via cells/Sites.** Availability target **99.9% single-region-HA**
(the promote gate); 99.99% is the designed multi-region path.

### 1. Region-local HA (RPO≈0), per substrate — *deploy, endpoints already externalized*

Every substrate endpoint is already an env-configured string (`STRATT_DATABASE_URL`, `STRATT_NATS_URL`,
`STRATT_TEMPORAL_ADDRESS`, `STRATT_OPENFGA_URL`, `STRATT_EVIDENCE_*`, …), so substrate HA is a
deployment change:

| Component | In-region HA | RPO=0? | Cross-region DR | Multi-region active-active writes? | Boring? |
|---|---|---|---|---|---|
| PostgreSQL 18 | primary + quorum sync standbys across 3 AZs, Patroni/CloudNativePG **fenced** failover | **Yes** (`synchronous_commit=remote_apply`, `ANY k`) | async streaming replica + PITR/WAL to WORM store | No (multi-master = non-boring + wrong for a deterministic core) | Core boring; multi-master not |
| NATS JetStream | R3/R5 RAFT streams across 3 AZs | **Yes** (majority-acked) | super-cluster+gateways, mirrors/sources, or stretch (sync) | Partial — stretch (sync) or virtual streams (eventual) | Yes (most multi-region-native) |
| Temporal (self-host) | stateless services + HA persistence store | inherited (=0 on quorum PG) | XDC / global-namespace (async) | **No** — active-in-one-cluster by design | Core boring; XDC not-really |
| Object store (Garage/SeaweedFS/S3) | zone-redundant replicas + versioning + Object-Lock WORM | Yes | CRR / multi-DC layout | Reads yes; writes vendor-specific | Yes |
| OpenFGA | N replicas over the HA datastore | inherited | datastore's story; `HIGHER_CONSISTENCY` for must-be-fresh checks | engine yes, tuples = datastore | Yes |
| Zitadel | ≥3× components over HA Postgres | inherited | Postgres replica (active-passive) | only with a global DB (non-boring) | Yes on Postgres |
| Kubernetes | 3 control-plane nodes, etcd quorum, spread across AZs | Yes | **separate cluster per region** (don't stretch etcd) | multi-*cluster* federation, not one stretched CP | Yes |

### 2. strattd control-plane HA — *the code this ADR ships (Commit 1)*

The REST API is stateless (per-request OIDC, no server sessions) and Temporal workers are natively
multi-worker — both run **active/active on every replica**. The singleton control loops must not
double-run (the audit sealer would corrupt the hash-chain; syncers double-load; the trigger-engine
cooldown is per-replica). So:

- **Leader election** via `client-go/tools/leaderelection` + a coordination.k8s.io **Lease** (charter
  §3 K8s-native operator posture — what kube-controller-manager uses; **zero new dependency**). The
  **audit sealer, all syncers, the desired-state/tuple/trigger/baseline reconcilers, the trigger
  engine, the notifier, and the Salt emitter** run **only on the elected leader**, under a
  leader-scoped context cancelled the instant leadership is lost → automatic sub-minute failover to a
  standby replica. Running the trigger engine leader-only also **fixes** its in-memory-cooldown
  limitation (single instance → reliable storm damping). Gated on `STRATT_LEADER_ELECTION`; off =
  single-replica dev/compose, unchanged. Multi-replica requires the **OpenFGA server** backend (the
  in-process authz evaluator is single-replica; the ongoing tuple-reload cadence is leader-only).
- **Migration lock** — `graph.Migrate` uses goose's Postgres advisory **session lock**, so N replicas
  racing `Up()` at boot serialize safely.
- **Real readiness** — `GET /readyz` verifies Postgres+NATS reachability (distinct from the
  liveness-only `/healthz`), so a pod only takes load-balancer traffic once its substrate is up (§1.8:
  probes report truthfully). Helm splits the probes, adds a PDB, pod anti-affinity, the Lease RBAC, the
  downward-API identity, and a surge-then-drain rollout.

### 3. Cross-region DR — *async, bounded, tested (runbook)*

Async replication for every stateful component (Postgres async replica + **PITR/WAL to immutable WORM**
object storage; Temporal XDC standby; NATS mirror; object CRR) into a warm/pilot-light second region.
Failover is **runbook-driven and human-authorized** for the disaster tier — you do **not** auto-flip
regions on a transient partition. DR is proven by **tested restore + scheduled drills**, never assumed
(§1.8): an untested backup is Schrödinger's backup. 3-2-1-1-0 doctrine; PITR defends against *logical*
disasters that replication faithfully copies. See `docs/runbooks/ha-dr.md`.

### 4. "Live/live/live in aggregate" — cells + Sites

Partition into **cells** keyed by tenant/site/region, each **region-local single-writer**: the fleet is
active/active/active across cells with **no datum multi-master**, blast-radius bounded per cell (the AWS
cell-based-architecture doctrine). Stratt's natural cell is the **Site** (§2.3), and the Site leaf/
pull-agent model (`sitegw`/`siteproto`, ADR-0032) **already gives geo-distributed, partition-tolerant
*execution*** — Run slices execute at remote loci and survive a control-plane partition. That is the
genuine live/live/live story: at the execution edge and across cells, not via a multi-master database.

## Charter posture

- **§3** leader election is the named K8s-native operator pattern; controllers on the leader, API/
  workers scale out. **§1.4** zero new dependency; the verdict *rejects* non-boring multi-master.
- **§2.1/§2.4** single-writer / one-active-write-region is a **correctness choice** — multi-master LWW
  would reintroduce the silent precedence the charter forbids.
- **§1.2/§1.8** DR = tested restore + drills; PITR + WORM backups defend against logical disasters;
  readiness/liveness split so probes never hide substrate failure.
- **§7** 99.9% via single-region HA now; 99.99% via multi-region DR is the designed path.

## Alternatives considered / rejected

- **Multi-master Postgres (pgEdge/BDR) or CockroachDB/Spanner for live/live/live writes** — rejected:
  §1.4 (non-boring) and §2.1/§2.4 (breaks single-writer determinism / reintroduces LWW). And it still
  wouldn't make Temporal active-active.
- **`pg_advisory_lock` for leader election** — viable and portable, but the charter names client-go/
  controller-runtime (§3); the Lease is the idiomatic K8s-native choice.
- **Automated cross-region failover** — a deliberate non-goal for the *disaster* tier: a false failover
  on a transient partition is itself an incident. In-region failover *is* automatic (Patroni fencing +
  the strattd lease).

## Honest deferrals

- Cross-region DR *wiring* and **cells / control-plane multi-region** are **designed here, deployed
  later** (substrate does the replication; the Site model already makes execution cell-shaped —
  control-plane cell-awareness is a future build); Temporal XDC standby + Postgres cross-region replica
  + object CRR are deploy/runbook. A **live kind failover e2e** (kill the leader → a standby resumes
  the controllers within a lease TTL) is scripted in the runbook but not run this slice — the guard is
  unit-proven against the fake clientset. Persisted trigger cooldown is **resolved** by leader-only
  (not deferred). A dedicated `/metrics` SLO surface and chaos-engineering automation are follow-ups.
  Leader election bounds but does not absolutely eliminate a brief two-leader overlap during a
  pathological renewal pause; a DB-side expected-prev-hash CAS on the audit sealer's append (ADR-0034)
  would make hash-chain integrity independent of lease timing — a belt-and-suspenders follow-up. Adding
  OpenFGA/Temporal reachability to `/readyz` (today Postgres+NATS) is a considered follow-up (weighed
  against probe flap).

## Consequences

strattd becomes a real N-replica HA control plane: the API and Temporal workers active/active, the
singleton controllers leader-elected with automatic sub-minute failover and no SPOF, safe concurrent
migrations, and a truthful readiness gate — targeting the promote gate's 99.9% SLO on region-local
quorum substrate. True multi-region live/live/live writes are **deliberately not built** and documented
as the wrong goal for a deterministic core; live/live/live is delivered where it is real — active/active
serving, cells/Sites, and geo-distributed partition-tolerant execution. No new engine, no new
dependency, no new graph write-path. Of the live/live/live picture, what ships **today** is the
execution edge — geo-distributed, partition-tolerant Run execution via the Site leaf/pull-agent model
(ADR-0032); control-plane **cells** (region-local single-writer shards) are designed here and built
later.
