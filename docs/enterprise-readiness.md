# Enterprise-readiness tracker — the cracks

**Purpose.** A living, evidence-backed inventory of the gaps between what Stratt *claims* (charter/ADRs)
and what it *enforces in the shipped artifact* — the cracks an enterprise reviewer would point at. This is
the hardening sibling of **[roadmap.md](roadmap.md)**: the roadmap tracks *capability*, this tracks
*credibility as the dark-matter substrate*. **Maintain it as we go** — add a row when a crack is found,
flip status + record the closing commit when it's eliminated. More will surface; that's expected.

**Origin.** Seeded 2026-07-18 by three grounded code audits (governance completeness, supply-chain/security,
operability/DR). Every row cites real evidence — a doc claim without code backing is itself a crack.

**Legend.** Status: 🔴 open · 🟡 partial · 🟢 fixed. Severity: **BLOCKER** (a reviewer rejects on it) ·
**Serious** · Minor.

---

## The meta-blocker (non-coding — above the whole board)

| ID | Gap | Sev | Status | Note |
|---|---|---|---|---|
| **META-1** | §7.4 OSPO/IP clearance not obtained; repo private. The entire rug-pull-proof / Apache-2.0 / public-ADRs-from-day-one *trust* thesis (§1.3) is unrealized — an enterprise can't audit what it can't see. | **BLOCKER** | 🔴 | **Not a coding task.** Owned by whoever holds the OSPO decision. No code closes it; every technical fix below is necessary-but-not-sufficient until this clears. |

## The through-line
The **deterministic core is genuinely enterprise-grade** (graph, audit hash-chain, cross-Cell federation,
the §4.3 mandatory floors — *proven* in `compiler/integration_test.go:258-272`, the SecretBroker, real OIDC,
real DCO, digest-pinned CI actions, pod hardening for the control plane + persistent plugins). The **unbuilt
half is the enforcement wiring and the operational envelope** — the half an enterprise bets on. Nearly every
crack is small and localized: *the machinery already ships in-repo; it's simply not called on the path that matters.*

---

## GOV — governance enforcement (the fresh work; make it real, not theater)

| ID | Crack | Sev | Status | Evidence | Fix / commit |
|---|---|---|---|---|---|
| **GOV-1** | Obligations recorded-and-inert: only `require_approval` consumed; `notify`/`record_evidence`/`ttl`/`post_review` dropped; break-glass "mandatory post-review" not even in the audit detail | **BLOCKER** | 🟢 | `policystep.go` (was: obligations omitted from audit) | **Fixed** `230ec98` (ADR-0075): obligations on the audit chain; `post_review`→tracked `governance/post-review` Finding |
| **GOV-2** | Admission PEP bypassed by the API: `admitEstate` runs only in `ParseDir` (Git); `POST /desired-state/apply`, `/plan`, `DeclareView` reach the graph with no admission | **BLOCKER** | 🟢 | `desiredstate.go:209` (only caller); `server.go:1136,1150`; `ComputePlan`/`Apply` take no Decider | **Fixed** `bf47367..` (ADR-0076): `AdmitDeclarations` over the port at all three imperative doors → 403 on deny, fail-closed; boot-snapshot estate policy; unit-tested |
| **GOV-3** | Plugin governance rejections (land-grabs, confused-deputy targets) swallowed to a `Warn` log line — never a RunEvent/Finding; the code comment admits "Persisting as Findings is the follow-up" | Serious | 🔴 | `orchestrate.go:893-895,1030-1032`; `action.go:164-166` | Publish each as a typed `RunEvent` (`governance-rejected`) and/or a Finding |
| **GOV-4** | No per-decision WORM Evidence: `SealEvidence` seals drift Findings only; `record_evidence` obligation seals nothing | Serious | 🔴 | `baseline.go:177` (only callers 58,80) | On `record_evidence`, `evidencestore.Seal` a decision bundle (nil-guarded) |
| **GOV-5** | Governance is a demo: policy Step in 1 of 11 estate workflows (`change-review`); SoD/waiver/break-glass in **0** estate declarations; 10 real actuation workflows ungated | Serious | 🔴 | `estate/workflows/*` (policy:0 in 10 files) | Add a policy Step (or admission control) to a genuinely mutating workflow (e.g. `compute-build`) |
| **GOV-6** | Max-delta floor ungated on the *first* compile of an Assignment (large initial membership applies unchecked) | Minor | 🔴 | `compiler.go:160` (`hadPrev && prev.MemberCount>0`) | Cap/document first-compile membership against an absolute ceiling |

## SEC — execution security & supply chain (checked first in procurement)

| ID | Crack | Sev | Status | Evidence | Fix / commit |
|---|---|---|---|---|---|
| **SEC-1** | EE Job (the one pod running arbitrary content) unsandboxed: only `FSGroup`; no limits/deadline/`automountSAToken:false`/nonroot/drop-caps/seccomp | **BLOCKER** | 🟢 | `dispatch.go:482-508` (was) | **Fixed** `0b925e2`: non-root, drop-ALL, no-priv-esc, seccomp, no SA token, CPU/mem limits, 6h deadline. (`readOnlyRootFilesystem` = follow-up) |
| **SEC-2** | Dev trusted-header = one-flag full auth bypass, no structural guard (can be enabled in prod alongside OIDC; attacker omits the Bearer, sets `X-Stratt-Principal: admin`) | Serious | 🟢 | `main.go:290`; `server.go:366-371` | **Fixed** `checkDevPrincipalSafety` refuses boot when `devPrincipal && (oidcIssuer!="" \|\| env∈{production,prod,staging})`; unit-tested |
| **SEC-3** | No `NetworkPolicy` anywhere — EE Jobs + plugins have unrestricted egress | **BLOCKER** | 🔴 | grep: none repo-wide | Default-deny-egress NetworkPolicy on `stratt.dev/run-id` + plugin labels; allow-list DNS + strattd + declared substrate |
| **SEC-4** | No Kyverno / Pod-Security-Admission policy set shipped (§7.1) — nothing enforces the pod hardening in-cluster | Serious | 🔴 | `deploy/policy/` is Stratt's OPA PDP, not K8s admission | Ship a Kyverno (or PSA `restricted`) set under `deploy/policy/kyverno/` |
| **SEC-5** | Floating image tags defaulted despite the digest mechanism (`image.tag: dev`, `golang:1.26`, `python:3.14-slim`); enforcement advisory only | Serious | 🔴 | `values.yaml:12,150`; `ee/Dockerfile:18,28,38` | Pin base-image digests; CI lint failing on floating tags in prod values |
| **SUP-1** | **No release signing / SBOM / SLSA provenance** — §7.3 "from release one" has zero code backing; only dev *Bundle* signing exists | **BLOCKER** | 🔴 | `.github/workflows/ci.yml:100-120` (build only, no push/sign/SBOM); no release workflow | Tag-triggered release: build+push by digest, `syft` SBOM, `cosign sign`+`attest`, SLSA provenance |
| **SEC-6** | Evergreen gate real but narrow — checks toolchain only, not substrate (Postgres/NATS/Temporal) or K8s skew | Minor | 🔴 | `Taskfile.yml:60-86` | Extend the check loop to the substrate/K8s floors (already vars) or scope the claim |

## OPS — observability, DR, proof, operability

| ID | Crack | Sev | Status | Evidence | Fix / commit |
|---|---|---|---|---|---|
| **OBS-1** | Observability effectively absent: slog→stderr only; OTel `// indirect` deps only; no `TracerProvider`, no spans, no metrics, no `/metrics`, no Helm `OTEL_*`/ServiceMonitor — the SLOs can't be measured from a running system | **BLOCKER** | 🔴 | `main.go:66`; `go.mod:112-114`; `server.go:142-216` | OTel SDK init (OTLP env), `otelgrpc` on plugin dials, 4 SLO instruments + `/metrics`, Helm `OTEL_*`+PodMonitor; ship the forwarder as an optional Deployment |
| **DR-1** | No backup path in the Helm artifact for the non-rebuildable state; **Evidence store not even Helm-configurable** → a prod deploy silently never seals WORM; SIEM forwarder has a Dockerfile but no Helm template | **BLOCKER** | 🔴 | `deploy/charts/stratt/**` (no CronJob/PVC/backup; no `STRATT_EVIDENCE_*` in `deployment.yaml`) | Wire `STRATT_EVIDENCE_*` + fail-loud boot probe (prod && no WORM); opt-in `backup:` block (pg CronJob); ship the forwarder Deployment; `task dr:restore-test` |
| **DR-2** | Audit chain is per-Cell, not rebuildable, async-forwarded — a lost Cell loses its un-forwarded tail; no cross-Cell replication | Serious | 🔴 | `auditstore.go:39,274` | Document the RPO (forward-lag) + require Postgres PITR in the ops baseline; optional synchronous/JetStream-mirror forward mode |
| **E2E-1** | No live-cluster e2e: Temporal always in-memory, plugins against sims; CI stands up no substrate → 13 integration tests + the migration-race test SKIP in CI | **BLOCKER** | 🔴 | `workflow_test.go:60`; `host_test.go:20`; `cells_e2e_test.go:33-37`; ci.yml (no `STRATT_TEST_DATABASE_URL`) | Add `services: postgres:18` to CI `build` (un-skips 13 tests **free** = **CI-1**); one `e2e:live` on kind+real-Crossplane driving Intent→Run→real Apply |
| **UPG-1** | No rolling-upgrade schema-skew discipline: migrations run in-boot behind an advisory lock; a breaking `Up` breaks still-serving old replicas; no expand/contract rule, no Helm pre-upgrade hook | Serious | 🔴 | `migrate.go:35,43`; `store.go:78` | Expand/contract (additive-only) rule + lint, or a Helm `pre-upgrade` migrate Job gating the roll; ADR-worthy |
| **SLO-1** | Only View-query has a committed gate (`TestViewQueryGate`) and it's `Short()`+DB-skipped in CI; pod-spawn p95 & freshness measured once by hand (ADR-0008); 99.9%/99.99% are a paper map | Serious | 🔴 | `view_bench_test.go:63-107`; `docs/evidence/multi-region-99_99.md:7` | Commit a pod-spawn gate test; drop `Short()` behind CI Postgres (CI-1); record availability once a fleet exists |
| **DESC-1** | UI descent missing top rungs: no `intents`/`assignments`/`blueprints`/`compile` routes — can't start descent from an Intent; API exposes only list endpoints (no `GET /intents/{name}`) | Serious | 🔴 | `ui/src/main.tsx:136-154`; `openapi.yaml:490,514` | Add `GET /intents/{name}`+`/blueprints/{name}` + matching UI routes with down-links |
| **DESC-2** | `RunEvent` (the descent floor) has no OpenAPI schema — the bottom rung is an untyped string stream to agents/CLI | Minor | 🔴 | `openapi.yaml:257-267` | Add a `RunEvent` schema |
| **DESC-3** | CLI has no descent commands (`stratt runs`/`events`) — §1.6 UI/CLI/agent parity broken on the CLI | Minor | 🔴 | `core/cmd/stratt/main.go` | Thin CLI wrappers over `/runs` + `/runs/{id}/events` |
| **MIG-1** | Migration `Down` blocks + old→new→old sets untested; `TestMigrateConcurrent` skips in CI | Minor | 🔴 | `graph/migrations/**`; ADR-0004 | DB-gated `TestMigrateDownUp` behind CI Postgres (CI-1) |

---

## Verified strengths (not cracks — do not regress)
- §4.3 **mandatory floors** genuinely pause + freeze state, **proven** (`compiler/integration_test.go:258-272`); plan-pinning fail-closed at load; orphan Findings real.
- **SecretBroker** (§2.5): coordinates-only, zeroized, fails-closed, kubelet-resolved — no leak path (`sdk/secretbroker/secretbroker.go`).
- Control-plane + **persistent plugin pod hardening** correct (distroless nonroot, readonly-root, drop-ALL, seccomp).
- Real **OIDC** (coreos/go-oidc JWKS), real **DCO** CI gate, **digest-pinned CI actions**, tamper-evident **`VerifyAudit`** (well-tested), **Evidence object-lock** config, cross-Cell **registry-skew** gate.
- Dispatcher **diagnostic floor (MF5)** surfaces pod/subprocess crashes as typed task events — descent doesn't degrade to an opaque blob on the EE-Job path.

## How we work this
Hollow-guarantees first (a claimed guarantee that isn't enforced is worse than a missing feature), then the
cheap high-leverage blockers (**CI-1** un-skips 13 tests for free), then the chunks (supply chain, observability,
DR, live-e2e). Every fix lands with a test and updates its row here.
