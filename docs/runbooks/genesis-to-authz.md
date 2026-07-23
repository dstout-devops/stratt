# Runbook — Genesis → real authz (ADR-0102)

Promote a minimal **genesis** dev deployment (in-process authz evaluator + a self-retiring
dev-header `bootstrap-admin` Principal) toward real, server-backed authorization — using Stratt's
**own** self-deploy loop to stand up the OpenFGA server. This is a supported, deny-safe operation,
not folklore.

Promotion has **two independent axes**. Phase 1 (ADR-0102) delivers axis 1; axis 2 is the ADR-0101
follow-up (gated on an in-cluster OpenBao chart).

| Axis | From → To | Phase | Mechanism |
|------|-----------|-------|-----------|
| 1. authz **backend** | in-proc evaluator → real OpenFGA server | **1 (this runbook)** | `STRATT_OPENFGA_URL` (boot-time env) — one restart |
| 2. **identity** | dev-header → OpenBao-OIDC | follow-up | needs an in-cluster OpenBao chart + the OIDC flip |

## Why this is safe

- **One seam, two backends, one source.** The in-proc `TupleAuthorizer` always loads and is the
  semantic reference; `STRATT_OPENFGA_URL` swaps in the server, fed the **same** CaC tuples via
  `SyncTuples` on every reconcile ([core/cmd/strattd/main.go:411-422](../../core/cmd/strattd/main.go#L411),
  main.go:1590). Promoting the backend is a config flip, **not** a data migration — grants transfer
  with zero rewrite.
- **Dev-header identity is retained on axis 1.** The boot guard `checkDevPrincipalSafety`
  (main.go:95-107) forbids only dev-header **+ OIDC** — dev-header **+ OpenFGA** is legal. So axis 1
  changes the decision engine without touching identity.
- **Deny is the failure direction.** Every step fails closed; a misordered promotion locks you out
  (recoverable), never opens a hole.

## Axis 1 — promote the authz backend (Phase 1)

Prereq: `task dev:genesis` is up (minimal floor, in-proc authz, `bootstrap-admin` granted).

1. **(Stratt-driven)** Have Stratt self-deploy the real OpenFGA server (memory mode) through its own
   gated `helm/deploy` loop:
   ```
   task dev:genesis:selfdeploy
   ```
   This ensures the `stratt-authz` namespace + gate-marker Secret, launches the `genesis-authz-deploy`
   Workflow, approves the `platform-admins` Gate as `bootstrap-admin`, and waits for the real
   `Deployment/Service openfga` to converge in `stratt-authz`. Observe the descent (§1.8): the Gate
   decision, the streamed Run/TaskEvents, the materialized workload.

2. **(operator-driven)** Flip strattd's authz backend onto that server:
   ```
   task dev:promote:authz
   ```
   This `helm upgrade`s genesis with `--set-string externalOpenfga.url=http://openfga.stratt-authz.svc:8080`
   (renders `STRATT_OPENFGA_URL`; `openfga.enabled` stays false — this is the *external-server* knob).
   **One strattd restart** (backend selection is boot-time env — ADR-0102's Phase-2 boundary). On boot,
   `NewOpenFGAAuthorizer` creates its store/model and `SyncTuples` pushes the CaC tuple set; strattd logs
   `authz backend: openfga`. The same `bootstrap-admin` allow/deny decisions now round-trip through the
   real server.

> **Note — capability proof vs production.** The self-deployed server runs the **memory** engine: it
> loses tuples on restart, but `SyncTuples` re-pushes them every reconcile, so it self-heals. This proves
> the *self-deploy capability*; it is **distinct** from the production posture (`values-authz.yaml`,
> `openfga.enabled` = the Postgres subchart with a DSN Secret + migration hook).

> **⚠ Once promoted, the self-deployed OpenFGA is LOAD-BEARING.** After `dev:promote:authz`,
> strattd's `/readyz` gates on the OpenFGA server (`STRATT_OPENFGA_URL`) — deleting or restarting that
> server takes strattd NotReady and 500s every authorized call (a self-inflicted chicken-and-egg, since
> the deploy that would recreate OpenFGA is itself authorized *by* OpenFGA). To recover, roll strattd
> back to the in-proc evaluator (re-run `task dev:genesis`, which omits `externalOpenfga.url`), redeploy
> OpenFGA via `dev:genesis:selfdeploy`, then re-promote.

> **⚠ Intermediate posture — real backend, DEV identity (time-box it).** After axis 1, authz *decisions*
> are real (OpenFGA server) but *identity* is still the **forgeable dev header**: anyone who can reach the
> API can send `X-Stratt-Principal: bootstrap-admin`. This is **NOT real authz end-to-end** — do not read a
> green demo here as production-grade. It is a legal DEV waypoint only (the boot guard blocks it in
> production/prod/staging and forbids it alongside OIDC). Treat it as short-lived scaffolding on the way to
> axis 2; never leave a shared/reachable deployment parked here.

## Axis 2 — promote identity to OpenBao-OIDC (follow-up, NOT Phase 1)

Blocked on authoring an in-cluster **OpenBao chart** (`values-authz.yaml` already targets
`openbao.stratt-dev.svc:8200`, a Service no chart yet provides — the in-kind OIDC e2e ADR-0101 booked).
The intended, deny-safe sequence when that lands:

1. Self-deploy in-cluster OpenBao (a chart like `openfga-memory`), then run
   `deploy/dev/openbao-bootstrap.sh` identity/oidc against it.
2. **Before flipping**, seed the REAL admin Principal + its grants into the tuple manifest — the OpenBao
   token's `sub` is the entity **UUID**, so the Principal is `openbao/<uuid>` (ADR-0101 §I-1). Re-namespace
   the `bootstrap-admin` grants to that Principal. **Do this first**, or the flip (dev-header off) leaves
   deny-by-default with no admin → lockout (recoverable: re-enable genesis, fix tuples).
3. Flip: `helm upgrade -f values-authz.yaml` (dev-header off, `oidc.issuers` on). The boot guard now
   *requires* OIDC and forbids the dev header — the genesis floor has fully retired.
