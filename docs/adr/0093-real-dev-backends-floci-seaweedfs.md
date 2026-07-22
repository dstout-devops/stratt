# ADR 0093 — Real dev backends: Floci (EC2) replaces moto; SeaweedFS bump (S3)

- **Status:** **Accepted** (2026-07-22) — steward approved Slice A of the two-plugin plan;
  charter-guardian PASS (two flags folded); dependency-scout verdicts folded.
- **Date:** 2026-07-22
- **Deciders:** steward (dstout), dependency-scout, charter-guardian
- **Charter sections:** §1.3 (rug-pull-proof), §1.4 (boring spine), §1.7 (evergreen), §3 (dev harness),
  §7.1 (any-Kubernetes / self-hostable)
- **Frames:** the AWS full-featured work (Slice C full EC2, Slice D S3 connector) — this slice gives
  those a **real** backend to develop/test against, not a mock. Builds on ADR-0014 (dev EC2 stand-in),
  ADR-0029 (evidence store S3).

## Context

The AWS connector work needs **real hosts and real storage** to develop against — the steward was
explicit: *"we can't just sim this."* Today the EC2 dev backend is **moto** (`motoserver/moto:5.2.2`) —
a pure API **mock** with no real VMs/hosts. That is fine for exercising API call shapes but cannot test
what a full-featured connector actually does: SSH/Ansible provisioning, real lifecycle state
transitions, real object durability.

**LocalStack — the obvious "real-ish" option — rug-pulled**: on 2026-03-23 it moved core services
(S3/SQS/Lambda) behind a $39/mo paid tier. That is exactly the §1.3 governance failure the charter is
built to preempt, so any replacement must be **rug-pull-safe**, not just functional.

dependency-scout (2026-07-22) surveyed the field for a real, self-hostable, AWS-compatible,
governance-safe backend. In scope: EC2 (real hosts) + S3 (real storage).

## Decision

**1. EC2 → Floci** (`floci-io/floci`, MIT), replacing moto in the dev harness. Floci serves the AWS EC2
API over **real Docker containers** — SSH-able, cloud-init/UserData/IMDS, real lifecycle state — so the
connector dev-loop (RunInstances/Describe/lifecycle/tags + SSH-based provisioning) runs against genuine
Linux hosts. Pin `floci-io/floci:1.5.33` by **digest**; verify N-1 in CI.
- **Explicit tradeoff (steward-accepted): container, not hypervisor VM.** No kernel-module/nested-virt/
  boot-level fidelity. For workloads needing that, a thin EC2-API shim over **KubeVirt** (matches the
  kind substrate) or **Incus** is a **deferred follow-up ADR** — not this slice.
- moto is **retired** from the harness (it may return later as a mock-OK backend for non-EC2 AWS
  services — SQS/DynamoDB — where a real backend isn't needed).

**2. S3 → SeaweedFS, bump `3.97 → 4.40`** (`chrislusf/seaweedfs`, Apache-2.0). Already in the harness for
the evidence store (ADR-0029); the bump keeps it current (4.40, released 2026-07-20; N-1 `4.39`). The
core S3 API, replication, and bucket versioning remain in the Apache-2.0 core today. Pin by digest.
- **Unchanged caveat (ADR-0029):** SeaweedFS accepts object-lock config but does **not** enforce WORM;
  dev evidence immutability rests on sha256 tamper-evidence + write-once, never backend WORM. Real WORM
  needs a compliant production store. The 4.40 bump does not change this.

**3. Evergreen gates (§1.7)** — wired in CI for both:
- Pin by **digest**, not floating tag; run the S3-path + EC2-connector tests against **both** the
  pinned and N-1 image on every bump.
- **Floci tripwire (with a concrete trigger — charter-guardian Flag A):** solo-maintainer,
  ~4-month-old project, zero major-version track record — so N-1-in-CI alone is thin (N-1 is days old,
  no upgrade record to evaluate, §1.7). Pin by digest, re-validate on every bump, AND add an
  **upstream-liveness check**; **execute the MIT fork/vendor decision when** either fires: no upstream
  release for **> 6 months**, or an unpatched CVE open **> 30 days**. This makes §1.7's
  "evaluated-before-adoption" auditable rather than aspirational.
- **Dev-harness-only boundary (charter-guardian Flag B):** Floci is accepted (steward, 2026-07-22)
  strictly as a **dev/CI backend reached only via `STRATT_AWSEC2_ENDPOINT`** — no code-level
  dependency. If it ever creeps toward load-bearing for anything a user runs, it needs a fresh charter
  pass.
- **SeaweedFS tripwire:** single-vendor with a commercial Enterprise arm. If any release moves the
  **core S3 verbs, basic versioning, or lifecycle** behind the Enterprise license, treat it as a hard
  rug-pull and **fail over to Garage** (AGPLv3 — admissible only as an unmodified subprocess/sidecar
  service, the Ansible posture; never imported).

**4. Harness wiring** — `deploy/dev/docker-compose.yml` (floci service replaces moto; seaweedfs 4.40),
`Taskfile.yml` (`dev:stack:up` subset moto→floci + add `openbao`; `dev:install` endpoint injection
points at floci), and a `deploy/dev/floci-bootstrap.sh` if pre-seeded AMIs/keys/SGs are needed
(idempotent, modeled on `openbao-bootstrap.sh`). The awsec2 plugin already takes
`STRATT_AWSEC2_ENDPOINT` (→ `ec2.Options.BaseEndpoint`), so pointing it at floci is an endpoint change,
no plugin code.

## Charter alignment

- **§1.3 rug-pull-proof:** the whole point — both picks are permissively licensed (MIT / Apache-2.0),
  and the LocalStack failure is the anti-pattern we route around. The SeaweedFS-Enterprise and
  Floci-solo-maintainer risks are named with a concrete failover (Garage / fork), not hand-waved.
- **§1.7 evergreen:** digest pins + N-1 CI gate + the two tripwires are the evergreen contract applied.
- **§1.4 boring spine:** these are dev-harness backends (not the production spine — Postgres/NATS/
  Temporal are untouched); a real dev backend that is self-hostable and permissive is the boring choice
  over a paywalled SaaS mock.
- **§7.1 self-hostable:** both run in the compose harness with a reasonable footprint.
- **Non-goals:** none crossed — this is dev/test infrastructure, not a product feature.

## Consequences

- **Positive:** the AWS connector (Slices C/D) develops/tests against real hosts + real S3; SSH/Ansible
  provisioning and real lifecycle are exercisable; the dev harness stays fully self-hostable and
  governance-safe.
- **Negative / trade-offs:** Floci is young + solo-maintained (mitigated by MIT fork-ability + digest
  pinning + N-1 gate); container-not-VM fidelity ceiling (mitigated by the deferred KubeVirt/Incus
  shim); SeaweedFS single-vendor risk (mitigated by the Garage failover tripwire).
- **Follow-ups:** the KubeVirt/Incus VM-fidelity shim ADR (if/when needed); the CI evergreen gate job
  (digest + N-1 + tripwires); moto's possible return for mock-OK non-EC2 AWS services.

## Alternatives considered

- **Keep moto** — rejected: a mock, fails the "real hosts" requirement outright.
- **ministack** (`ministackorg/ministack`, MIT) — rejected for EC2: its EC2 is an in-memory mock (no
  real VMs); viable later for other mock-OK AWS services, not for `awsec2`.
- **LocalStack** — rejected: rug-pulled core services behind a paid tier (the §1.3 anti-pattern).
- **OpenStack ec2-api / CloudStack / Eucalyptus** — rejected: retired / decade-stalled / dead, and far
  too heavy for a dev harness.
- **KubeVirt / Incus VM shim now** — deferred: true VM fidelity but heavier + a real adapter to own;
  its own follow-up ADR when container fidelity proves insufficient.
- **Garage for S3 now** — rejected as the default: AGPLv3 (sidecar-only, not the primary dev store);
  kept as the SeaweedFS-rug-pull failover.

## Reviews

- **dependency-scout (2026-07-22):** Floci = CAUTION-adopt (real containers-as-hosts; solo maintainer,
  no upgrade track record → digest-pin + N-1 gate; MIT fork-able). SeaweedFS 4.40 = RECOMMEND
  (Apache-2.0 core; single-vendor Enterprise-arm risk → Garage failover tripwire). ministack = REJECT
  for EC2 (mocked). Full report folded into the Decision + tripwires above.
- **charter-guardian (2026-07-22): PASS — with two judgment-call flags (folded), no hard violations,
  no non-goals crossed.** Correctly dev-harness-scoped (production spine untouched); routes around the
  §1.3 LocalStack anti-pattern rather than into it; residual risks carry pre-declared failovers.
  Folded: (A) the Floci fork tripwire now has a concrete trigger + upstream-liveness check; (B) the
  acceptance is dated and bounded dev-harness-only. Precision note (applied): dependency-selection
  governance lives most precisely in **§1.4** (boring/huge-community) + **§1.7** (upgrade record) +
  **§3** (the MinIO single-vendor warning; SeaweedFS/Garage are charter-*named* reference S3 impls) —
  §1.3 proper is Stratt's own licensing. The Garage-AGPL-as-unmodified-sidecar reading was affirmed
  correct (mirrors the §3 Ansible subprocess boundary).
- **vocabulary-linter:** _n/a_ (no new core-model identifiers; `floci`/`seaweedfs` are tool/image names).
