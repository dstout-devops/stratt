# ADR 0009 — Identity, authorization, and credential brokering

- **Status:** Accepted
- **Date:** 2026-07-11
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.6, §2.5, §7.3, §8 (Phase 1: "CredentialRefs (Vault + K8s), OIDC + basic OpenFGA")

## Context

CredentialRefs are the first surface where identity, authorization, and secret custody
become table shapes. These are the classic hard-to-retrofit decisions: AAP 2.5's
platform-gateway migration (central auth bolted onto components that each grew their own
RBAC) is the standing evidence, and AWX's credential subsystem is both the best
available shape and the best available warning.

A design study of AWX/AAP, Rundeck, Semaphore UI, and HCP Terraform (2026-07-11)
concluded:

| From AWX | Verdict |
|---|---|
| Typed credentials: input schema + declarative injectors, both data | **Keep** (schemas land with Phase-2 Contract machinery) |
| `use` as a grant distinct from `read` (use-without-read) | **Keep** — the single best idea in AWX authz |
| Resolve-at-launch from external stores | **Keep the timing, reject the resolver** (control plane must not resolve) |
| Org-scoped ownership, hierarchy-implied admin | **Keep** (via ReBAC, not closure tables) |
| Secret material in Postgres under one `SECRET_KEY` | **Reject** — the DB becomes the estate's highest-value target; key loss = total loss |
| Control plane fetching plaintext and passing it to the runner | **Reject** — custody violation by design |
| Per-object implicit-role rows / closure tables | **Reject** — role explosion, unauditable effective access |
| User-private credentials; one-credential-per-kind attachment | **Reject** — sprawl generators |
| Injection via extra_vars / free templating | **Reject** — tool-content-visible secrets leak into logs |

HCP Terraform's dynamic provider credentials (workload-identity federation — no stored
secret at all) is the end-state to design toward, not an afterthought.

## Decision

### 1. Principal — one kind, one seam

`Principal{ID, Kind: human|service|agent}` (charter §2.5): agents and services live in
the same authz, audit, and cost model as humans. The API resolves the Principal in one
middleware; every downstream check and audit stamp uses it. Phase-1 interim: a dev-only
header (`X-Stratt-Principal`, gated behind `STRATT_DEV_PRINCIPAL_HEADER=true`, loudly
logged); OIDC via Zitadel replaces the resolver without touching any consumer.

### 2. Authorization — OpenFGA ReBAC; tuples are CaC

Model v1 (OpenFGA DSL; enforced this phase by an in-process tuple evaluator with
identical semantics, swapped for the OpenFGA server with OIDC):

```
model
  schema 1.1

type principal

type org
  relations
    define admin: [principal]
    define member: [principal] or admin

type team
  relations
    define org: [org]
    define admin: [principal] or admin from org
    define member: [principal] or admin

type credential_ref
  relations
    define owner_team: [team]
    define admin: [principal] or admin from owner_team
    define reader: [principal, team#member] or admin
    define user: [principal, team#member]   # use-without-read: implies NOTHING else
```

- **`user` (use) implies nothing** — not reader, not admin. A Principal may bind a
  credential into a Run while being unable to read even its pointer metadata.
- Org admin ⊃ team admin ⊃ object admin ⊃ reader, all by relation, no role rows.
- **Tuples are CaC** (charter §2.5): `authz/tuples.yaml` lives in the declarations repo
  and flows through the same Git reconciliation as Views — RBAC changes are reviewed
  diffs with history, and "who can do what" is a file, not archaeology.
- View-scoped execution ("may run this Workflow, but only against Entities in this
  View") is the named Phase-2/3 extension of this model, not a new mechanism.

### 3. CredentialRef — a pointer the platform can never dereference into itself

```
credential_ref: name · owner_team · backend (k8s-secret | vault | workload-identity)
              · locator (jsonb, backend-shaped) · injection (jsonb policy)
```

- **No material column exists.** Nothing in the schema can hold a secret.
- **Injection policy** is per-field `{key, as: env|file, name}`. **File/volume is the
  preferred mode** (env vars leak via /proc, crash dumps, and child-process
  inheritance); env remains available where tools demand it. Free templating into tool
  variables (AWX extra_vars) is not offered.
- Ownership is a team, always — no user-private credentials (a personal credential is a
  team of one).
- `vault` and `workload-identity` are valid backend kinds from day one so their arrival
  is an addition, not a redesign; resolving them is unimplemented until their slices
  (Vault via CSI/agent resolving in-pod with the pod's own workload identity — the
  control plane holds no Vault token, ever).
- Credential *type* schemas (pinned JSON Schema shapes validated against Contracts)
  arrive with the Phase-2 Contract machinery.

### 4. Custody — projection, not injection

The control plane composes **pod specs**, never material:

- `k8s-secret` backend: `env[].valueFrom.secretKeyRef` and read-only projected Secret
  volumes (0400) under `/runner/credentials/…`. **Kubelet resolves the material**;
  strattd's process never contains it. K8s RBAC on the Secret is a second, independent
  enforcement point.
- Material never enters Postgres, Temporal workflow state, NATS payloads, Run
  summaries, or artifacts — only CredentialRef names travel (§1.8 audit includes the
  names). The API has **no endpoint that returns material — no such code path exists.**
- `use` is checked at dispatch time against the launching Principal, recorded on the
  Run.

## Charter alignment

§1.6 one Principal model before a second surface exists; §2.5 verbatim (pointer +
injection policy, never persists, use-without-read, tuples-as-CaC); §1.4 boring spine
(no new deps this slice; OpenFGA already charter-named); §2.4 no implicit precedence
(grants are explicit tuples; denial is the default); §7.3 secrets never baked. No
Founding Discipline or non-goal is touched.

## Consequences

- **Positive:** AWX's SECRET_KEY catastrophe class is unrepresentable; RBAC is
  reviewable Git history; OIDC/OpenFGA land as swaps behind stable seams.
- **Negative / trade-offs:** dev-principal header is a temporary trust hole (gated,
  loud, removed with OIDC); two config surfaces (DB CredentialRefs + Git tuples) until
  tuple projection is revisited; in-process tuple evaluator must match OpenFGA
  semantics exactly for the swap to be mechanical (kept minimal to keep that true).
- **Follow-ups:** OIDC (Zitadel) + OpenFGA server (next slice); Vault backend via
  CSI/agent; `workload-identity` backend; Syncer (control-plane-side) credential
  resolution custody design; secret-value scrubbing in the event pipeline
  (defense-in-depth); credential-type schemas with Phase-2 Contracts; per-Principal
  cost/usage accounting (§7.6).

## Alternatives considered

- **Encrypt secrets in Postgres (AWX model)** — rejected: charter §2.5 forbids it, and
  the research documents the blast radius and operational hazard.
- **Control-plane resolution of external stores** — rejected: reintroduces custody in
  strattd's memory and a stored broker credential (AWX's turtles problem).
- **Casbin / homegrown RBAC / K8s RBAC as the platform model** — rejected: charter
  names OpenFGA; ReBAC expresses use-without-read and View-scoped execution natively.
- **OpenFGA server in this slice** — deferred by steward decision: the tuple evaluator
  ships the semantics now; the server lands with OIDC as one auth slice.

## Implementation notes (slice 5 — OIDC + OpenFGA server, 2026-07-11)

The two deferred swaps landed behind the seams this ADR named; no consumer changed.

- **Pins (dependency-scout 2026-07-11):** OpenFGA server `openfga/openfga:v1.17.0`
  (RECOMMEND — CNCF Incubating 2025-10, prompt CVE history, Postgres first-class);
  `github.com/openfga/go-sdk v0.7.0` (CAUTION — pre-1.0: pinned exact, every bump
  treated as breaking and gated by the agreement test); `github.com/coreos/go-oidc/v3
  v3.18.0` (RECOMMEND — verify-only scope, K8s/Dex pedigree). `openfga/language`
  (DSL parser) deliberately skipped: the authorization model is authored as JSON
  (`core/internal/authz/authzmodel.json`, embedded, §1.5 schemas-are-data); the DSL
  above stays documentation.
- **Dev IdP: Zitadel in the dev compose** (`ghcr.io/zitadel/zitadel:v4.16.0`), riding
  the shared Postgres with its own database. dex was rejected (no `client_credentials`
  grant — exactly the machine flow production uses); Keycloak rejected (JVM substrate
  skew). **License flag:** Zitadel core relicensed Apache-2.0 → AGPL-3.0+CLA
  (2025-03). Acceptable strictly as an unmodified, network-accessed service — the
  Ansible-subprocess analogy; never vendored, forked, or linked. The e2e service
  identities are provisioned with **access-token-type JWT** (go-oidc verifies via
  JWKS; opaque tokens would require introspection).
- **Datastore:** memory engine in dev — tuples are CaC and re-synced every reconcile
  cycle, so the server is a rebuildable projection of Git (§1.2) and losing it loses
  nothing. Production runbook (Helm slice): postgres engine + `openfga migrate`.
- **Tuple sync is desired-state, not additive:** `SyncTuples` diffs the manifest
  against a full server read and issues adds *and* deletes — grants added out-of-band
  on the server are revoked on the next cycle (§2.4 no implicit precedence; Git is
  the declarer).
- **Swap fidelity is a test, not a claim:** `TestOpenFGAAgreement` runs the tuple
  evaluator's fixture through both backends across every principal × relation ×
  object and fails on any disagreement. The evaluator stays as the no-substrate dev
  path and the model's executable semantics.
- **Principal.ID = `sub`** (stable, non-reassignable; usernames and emails are not
  identifiers). **Kind is a heuristic for now** — profile claims
  (`preferred_username`/`email`) → human, else service; an explicit kind claim and
  claims→team mapping (the AAP authenticator-maps analog) are follow-ups — team
  membership stays explicit in `tuples.yaml`.
- **Audience check is opt-in** (`STRATT_OIDC_AUDIENCE`): dev client_credentials
  tokens carry client-specific audiences; production sets it. A presented Bearer that
  fails verification is **401, never downgraded to anonymous**; an absent credential
  is anonymous and denied per-grant (403).
- **Backends are optional by env** (`STRATT_OPENFGA_URL`, `STRATT_OIDC_ISSUER`
  unset → evaluator + gated dev header): the no-substrate dev path is a feature and
  keeps the reference semantics honest.
- **Correction to the file-injection mode:** slice-4 projected Secret files as 0400,
  which is unreadable in practice — the kubelet owns Secret files as root and the EE
  runs non-root (uid 1000), so the "verified" 0400 file was never readable by the
  tool (caught by this slice's e2e). Files are now 0440 with pod `fsGroup` =
  the EE gid (`STRATT_EE_FSGROUP`, default 1000): root:fsGroup, group-read only, no
  world access, tool stays non-root. "File-preferred" stands.
