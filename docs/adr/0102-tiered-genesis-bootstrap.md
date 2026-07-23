# ADR 0102 — Tiered genesis bootstrap: a minimal self-retiring floor, then Stratt self-deploys the rest

- **Status:** **Proposed** (2026-07-23, steward)
- **Date:** 2026-07-23
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.4 (boring spine, few deps — the genesis floor is only Postgres/NATS/Temporal
  + strattd) · §1.6 (one Principal / one authz model — the bootstrap admin is a Principal like any other)
  · §1.3 (no lock-in / sovereignty — the floor is provably temporary, not a permanent root) · §8
  (phasing — build the next thing, gate the rest). Builds on ADR-0009 (identity/authz), ADR-0013 (chart/
  values), ADR-0092 (helm Actuator / self-deploy), ADR-0101 (real authz + OpenBao-OIDC).

## Context

Standing up the kind dev environment had drifted to a kitchen-sink default: `dev:stack:up` brings up the
full compose substrate + every plugin backend + a statically-enumerated `plugins:` helm list, all
front-loaded. That is the opposite of the platform's own thesis — *boring spine, pluggable everything;
Stratt manages the estate*. The steward's directive: **stand up a minimal core, then have Stratt itself
enable/disable plugins and services.**

Two capabilities already make this reachable without new core code:

1. **The minimal core already exists.** `bootstrap:kind` + `values-allinone.yaml` = kind + in-cluster
   Postgres/NATS/Temporal + strattd, self-contained, no compose, no plugins.
2. **Self-deploy already exists (ADR-0092).** `plugins/helm` serves the targetless `helm/deploy` Action;
   the gated `helm-deploy` Workflow has Stratt materialize a Helm release through its own reconcile →
   Gate → Run → Actuator loop.

Two facts about the authz seam make a clean promotion path possible:

- **One seam, two backends, one CaC source** (`core/cmd/strattd/main.go:411-422`): the in-process
  `TupleAuthorizer` always loads (the semantic reference); `STRATT_OPENFGA_URL` swaps in the OpenFGA
  server, fed the *same* tuples via `SyncTuples` (main.go:1590). So in-proc → server is a config flip,
  not a data migration.
- **The boot guard forbids only dev-header + OIDC** (`checkDevPrincipalSafety`, main.go:95-107) — NOT
  dev-header + OpenFGA. So promoting the authz *backend* while keeping dev-header identity is legal.

**The gap this ADR closes:** genesis boots with **zero tuples** — `values-allinone` never enables inline
declarations, so the dev header resolves any Principal but nothing is granted, and the bootstrap admin
cannot even approve the self-deploy Gate. Genesis needs a minimal grant, and the dev-env order-of-
operations needs to be re-centered on it.

## Decision

Adopt a **two-tier bootstrap contract.**

### Tier 0 — the genesis floor (imperative, minimal, one command: `dev:genesis`)

The irreducible floor Stratt cannot deploy itself (it runs on it, or is gated by it):

- kind + the in-cluster spine (Postgres/NATS/Temporal — §1.4) + strattd,
- the in-process `TupleAuthorizer` (no external authz service required to boot),
- **one self-retiring dev-header bootstrap-admin Principal** (`principal:bootstrap-admin`), granted the
  minimum to drive self-deploy: `member of team:platform-admins` (the Gate-approval grant) and `user of
  credential_ref:helm-deploy` (the §2.5 use-check the `helm/deploy` Action requires), loaded via a lean
  dedicated genesis declarations slice — NOT the full estate.

**The floor is self-retiring by construction, not by discipline.** `checkDevPrincipalSafety` forbids the
dev header coexisting with OIDC, so the moment identity is promoted to real (OpenBao/Zitadel), the header
stops resolving — the bootstrap admin cannot silently persist into the real-*identity* posture (§1.3: the
root is provably temporary, not lock-in).

**Precision (charter-guardian Q5):** "self-retiring" is a *structurally guaranteed* property realized **at
the identity flip**, which is the deferred axis-2/Phase-2 step — it is **not** a state Phase 1 reaches.
Phase 1 deliberately retains the dev-header identity through the *entire* backend promotion (axis 1: in-proc
→ real OpenFGA), so across everything ADR-0102 actually ships, the forgeable header is always present. The
guard makes it *impossible for the header to survive OIDC*; it does not make the header go away on its own.
So axis-1 posture is "real authz **backend**, dev **identity** — NOT real authz end-to-end," and must be
read that way. A defense-in-depth hardening is booked below (Q2a).

### Tier 1 — Stratt self-deploys the rest (declarative, its own gated loop)

Everything above the floor — the helm plugin, the real OpenFGA server, later OpenBao and the tool
plugins — is deployed *by Stratt* through the existing gated `helm/deploy` loop, dogfooding the
self-managing thesis. Phase 1 proves this by having Stratt self-deploy the **real OpenFGA server**
(memory mode) from genesis, then promoting the authz backend in-proc → that server (`SyncTuples` pushes
the CaC tuples; dev-header identity retained — legal per the guard). The compose kitchen-sink becomes an
opt-in dev-speed shortcut, never the default.

### The Phase-2 boundary (explicit, load-bearing)

The **deploy half** of dynamic enablement works now (`helm/deploy` materializes a workload). The
**dial/register half** does not: strattd wires `STRATT_<NAME>_PLUGIN_ADDR` and selects
`STRATT_OPENFGA_URL` **once at boot** — a self-deployed plugin or backend cannot be registered/dialed,
and a backend cannot be swapped, without a strattd restart. So **every Phase-1 promotion step costs one
operator-driven restart, by design.** Runtime enable/disable-without-restart (a reconciled plugin
registry replacing the boot-time env wiring) is **Phase 2** and gets its own ADR — it must not be
smuggled into Phase 1.

## Charter alignment

- **§1.4** — the genesis floor is exactly the boring spine (Postgres/NATS/Temporal); no new required
  dependency. OpenFGA/OpenBao stay optional services Stratt deploys, not boot prerequisites.
- **§1.6** — the bootstrap admin is a Principal under the one model / one authz (tuples) / one audit; it
  is not a bypass — deny-by-default still holds, it simply holds *one* explicit grant.
- **§1.3 / sovereignty** — the floor is self-retiring (structurally, via the boot guard); a promoted
  deployment owes nothing to the genesis header. No gated tier, no lock-in.
- **§1.8** — the descent (Intent → Workflow → Run → task event) over the self-deploy loop is unchanged;
  the dogfood is observable end to end (Gate decision, streamed TaskEvents, the materialized workload).

## Consequences

- **Positive:** the documented default becomes a genuinely minimal core; the self-managing thesis is
  dogfooded (Stratt deploys its own OpenFGA); the genesis→real-authz promotion becomes a supported,
  deny-safe runbook rather than folklore; the compose kitchen-sink stops being load-bearing.
- **Two latent ADR-0092 bugs surfaced + fixed (this was the FIRST functional run of `helm/deploy`):**
  (1) the plugin named its output Contract but never emitted the `Outputs` payload, so
  `ValidateActionOutputs` saw null and failed every Run — fixed to marshal `{release, namespace}`, with a
  regression test; (2) the self-deploy RBAC lacked `pods`/`replicasets` read, which `helm --wait` needs to
  observe rollout readiness — without it `--wait` hung until cancelled, leaving the release
  `pending-install` — fixed with a separate read-only Role rule. Both were invisible because the Action
  had only ever been render-tested. **Live-proven end to end:** genesis floor → bootstrap-admin launches +
  approves the Gate → Stratt's helm Actuator deploys the real OpenFGA (1/1, release `deployed`,
  WorkflowRun `succeeded`) into the chart-owned namespace via the namespaced Role → `dev:promote:authz`
  flips strattd to `authz backend: openfga` on that self-deployed server.
- **Negative / trade-offs:** until Phase 2, each promotion is a one-restart operator act (boot-time env);
  the self-deployed memory OpenFGA is a *capability proof*, distinct from the production `openfga.enabled`
  postgres subchart (memory loses tuples on restart, but `SyncTuples` re-pushes each reconcile). A lean
  genesis slice adds a second staging path (`dev:stage-genesis`) alongside `dev:stage-estate`.
- **De-scoped (booked follow-ups):** an in-cluster **OpenBao chart** and the full **OIDC identity flip**
  (dev-header → `openbao/<uuid>`, incl. the ADR-0101 Principal re-namespacing) — Phase 1 keeps dev-header
  identity through the backend promotion; full OpenFGA-postgres via `helm/deploy`; and the Phase-2
  **runtime plugin registry**.
- **Booked hardening (charter-guardian Q2a):** `checkDevPrincipalSafety` blocks the dev header in
  `production/prod/staging` but permits it on empty/unknown `environment` — a genesis-derived deploy that
  forgets `environment: dev` still boots the forgeable header. Consider requiring the dev header to name an
  explicit known-dev environment (deny-by-default on empty), rather than allow-by-default-on-empty. Deferred
  as a core boot-guard change (Phase 1 ships no new core Go); the dev overlays here always set
  `environment: dev`.

## Alternatives considered

- **Keep the kitchen-sink default, just document a minimal path.** Rejected — the default shapes the
  mental model; leaving `dev:stack:up` as the way in keeps front-loading everything.
- **Include real authz (OpenFGA server + OpenBao) in the genesis floor.** Rejected as the default — it
  bloats Tier 0 and misses the point; standing these up is exactly what Stratt should do itself. They
  remain services Stratt self-deploys.
- **Author the OpenBao chart + do the full OIDC identity flip now.** Deferred — materially more work
  (new chart + in-kind OIDC bootstrap), and the backend-only promotion already proves the thesis; ADR-0101
  already books the in-kind OIDC e2e.
- **Build the runtime plugin registry now (skip the reorder).** Rejected — the registry is a core
  runtime change (an ADR of consequence); the reorder + dogfood de-risks its design and delivers value
  immediately with no new core code.
