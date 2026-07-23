# ADR 0101 — Activate real cluster authz; OpenBao-OIDC workload identity; multi-issuer Principal resolution

- **Status:** **Accepted** (2026-07-23, steward) — charter-guardian **PASS-WITH-CHANGES**: direction
  charter-sound (§1.6 real Principal for humans/services/agents under one authz+audit; §1.5 resolver-is-
  contract-issuers-are-transports; per-Cell OpenBao the right sovereign default). **Phase A merges as
  specified.** **Phase B is GATED on six fail-closed invariants, now BINDING in the Decision (I-1…I-6):**
  I-1 issuer-scoped Principal (the verifying issuer, never a claimed `iss`) — bare `sub` is a
  cross-issuer privilege-escalation vector; I-2 mandatory audience (boot-guarded); I-3 `kind` never
  load-bearing in an authz decision; I-4 durable `sub`↔principal binding (no name reuse); I-5
  fail-closed init + "deny unless ALL reject"; I-6 audience-restricted SA→OpenBao login, short-TTL
  tokens, never persisted. `KindAgent` deferral accepted (the constant exists; only the resolver
  mapping is deferred) but bound to "before any agent Principal for cost-accounted MCP automation"
  (§1.6/§7.6). §1.5: the resolver is ingress-edge only, never the reconcile/compile hot path.
  vocabulary-linter **PASS** (no renames).
- **Date:** 2026-07-23
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.6 (one Principal / authz / audit for humans, services, AND agents) · §1.4
  (boring spine, few deps — OpenBao can serve OIDC, no mandatory second IdP) · §1.5 (sovereign
  contracts, multiple transports — the resolver is the contract, an IdP is a transport) · §1.3 (no
  lock-in — any compliant issuer) · §2.5 (workload auth, brokered) · builds on ADR-0009 (identity/
  authz), ADR-0028 (View-scoped runner grant), ADR-0044 (Cells / authz-home), ADR-0094/0098 (the
  OpenBao pieces this leans on); forward-compatible with the ADR-0100 per-Cell sovereignty seam.

## Context

The authz **engine is built and correct** — an `OpenFGAAuthorizer` (server-backed, tuple-syncing), an
in-process tuple evaluator, real OIDC Bearer validation (`go-oidc`, standard discovery + JWKS),
deny-by-default at three chokepoints (API `requireGrant`, `RunAgainstView` runner-check, the Action
credential use-check), and the OpenFGA subchart vendored + pinned. Two gaps remain, and they are what
actually block the in-cluster e2es:

1. **Nothing activates it.** Every values profile ships the dev bypass — `openfga.enabled=false`,
   `oidc.issuer=""`, `devPrincipalHeader=true`. So a real deploy is all-anonymous-deny or the dev
   header; the M2 self-deploy + Syncer e2es only ever exercise the *in-proc* evaluator through inline
   CaC + the trusted header, never a real OpenFGA server + real IdP.

2. **No workload identity.** OIDC Bearer for human/service API callers is built (verify-only), but
   pods/agents/plugins have **no cryptographic identity → Principal** — it is `devPrincipalHeader` +
   CaC-declared principal strings + a plugin identity "stand-in" string-match. mTLS/SPIFFE/SA-token→
   Principal is designed-and-commented, not built (§1.6 for agents is unmet).

**The OpenBao lever (steward, 2026-07-23).** We already run OpenBao (KMS/PKI/KV/SecretBroker), and it
is a standards-compliant **OIDC provider** (`identity/oidc`: discovery + JWKS). So the workload chain
is: **pod → OpenBao Kubernetes auth (its own ServiceAccount) → OpenBao-issued OIDC token → the existing
`OIDCResolver` → Principal** — no new *validation* code. Per-Cell OpenBao makes this a **sovereign
identity floor**: a cut-off Cell mints its own workload identities locally (aligning with ADR-0100).

**The one real limit found:** the resolver is **single-issuer** (`oidc.go` holds one verifier bound to
one `STRATT_OIDC_ISSUER`; a wrong-`iss` token is rejected — `oidc_test.go`). So "Zitadel for humans AND
per-Cell OpenBao for workloads on the same Cell" needs a **multi-issuer** resolver, and the CaC
`runner`/`user` tuples must grant the resolver's issuer-namespaced Principal id (`openbao/<entity-uuid>`
— see the Decision's point 2 empirical correction).

## Decision

Two phases; the local floor of identity (deny-by-default) is never weakened.

### Phase A — Activate the real authz stack in-cluster (mostly wiring + proof)

1. A **real-authz values profile** (a new `values-authz.yaml` layer, and flip the plugin-e2e path to
   use it) that sets `openfga.enabled=true`, `oidc.issuer=<the Cell's OpenBao OIDC provider URL>`,
   `oidc.audience=<stratt>`, and `devPrincipalHeader=false`. The existing structural boot guard already
   refuses dev-header-alongside-OIDC and prod-with-dev-header — so this profile is self-checking.
2. Grants reconcile to the **real OpenFGA server** via the existing `SyncTuples` (leader/authz-home
   only, ADR-0044) — no new reconcile code; the tuple manifest is already the source.
3. **Live end-to-end proof:** a real Bearer token is accepted; an anonymous request is denied; a
   Principal *without* the grant is denied; *with* the grant passes; the M2 `helm-deploy` gate
   authorizes through real OpenFGA, not the dev bypass.

### Phase B — OpenBao-OIDC workload identity (the code)

1. **Multi-issuer `OIDCResolver`** — trust a LIST of issuers, each its own `go-oidc` verifier; a token
   is accepted if ANY configured issuer verifies it (then map claims → Principal). Config becomes
   `STRATT_OIDC_ISSUERS` (a JSON list of `{issuer, audience, subNamespace, alias}`; `STRATT_OIDC_ISSUER`
   + `STRATT_OIDC_AUDIENCE` stay as the single-issuer alias). This
   is the load-bearing new code and is the §1.5 "one contract, multiple transports" applied to IdPs:
   per-Cell OpenBao (workloads/sovereignty) + optional central Zitadel (human SSO/SCIM) coexist under
   the one Principal model (§1.6).
2. **OpenBao as the workload OIDC provider** — an `identity/oidc` key + role, with each workload an
   OpenBao **entity**. OpenBao **Kubernetes auth** binds a pod's ServiceAccount → that entity. The
   `openbao-bootstrap.sh` seeds the dev key/role/entity and prints the entity id.
   **Empirical correction (OpenBao 2.5.5, proven in-repo):** an `identity/oidc` token's `sub` is the
   entity's **stable UUID**, NOT its name, and `iss` is `<addr>/v1/identity/oidc`. A UUID is a
   *non-reassignable* identifier — a recreated entity gets a fresh UUID — so the `(namespace+sub)`
   Principal satisfies **I-4 by construction** (no name-reuse can inherit grants), which is *stronger*
   than the original "entity-name = sub" sketch. The CaC tuple therefore grants the namespaced UUID
   (`openbao/<uuid>`, the bootstrap prints it). Readable name-based Principals via an `identity/oidc`
   **role claim template** are a documented follow-up — and they would *reintroduce* the entity-name
   immutability burden I-4 exists to avoid, so the UUID is the secure default, not the fallback.
3. **The workload flow:** a pod/agent/plugin logs in to OpenBao with its projected SA token → gets an
   OpenBao OIDC ID token (`sub` = its entity UUID, no `preferred_username`/`email` ⇒ resolves as
   `KindService`) → presents it as `Authorization: Bearer` to strattd → the multi-issuer resolver
   verifies it against the Cell's OpenBao issuer and **prepends that issuer's namespace** → Principal
   `openbao/<uuid>`. No plugin "stand-in" string-match, no dev header.
4. **`devPrincipalHeader` stays dev-only** (structurally gated already); it is never enabled in the
   authz profile.

### Fail-closed invariants — BINDING on Phase B (charter-guardian, must be structural not aspirational)

- **I-1 — Issuer-scoped Principal identity.** A bare `sub` is a cross-issuer Principal-collision →
   privilege-escalation vector. The Principal identity is `(verifying-issuer, sub)` — realized as a
   configured, **disjoint per-issuer namespace that the resolver PREPENDS to `sub`** to form the
   Principal id (boot-validate the namespaces are non-overlapping), so the id stays globally
   unambiguous: a `sub` minted under issuer B — even one byte-identical to issuer A's — resolves under
   B's namespace and can never become A's Principal. The namespace is contributed by the issuer that
   **cryptographically verified** the token — NEVER trusted from a pre-verification `iss`/`sub` claim.
   (Prepending, not prefix-matching: OpenBao's `sub` is a bare UUID with no namespace to match, so the
   resolver owns the namespacing; proven in `oidc_test.go`.)
- **I-2 — Mandatory audience when OIDC is active.** Extend the boot guard to refuse OIDC-active +
   empty audience; every verifier checks `aud`. (Prevents replay of a token minted for another RP.)
- **I-3 — `kind` is never load-bearing in an authz decision.** Deny-by-default keys on Principal id +
   tuples only; no chokepoint (`requireGrant`, `RunAgainstView`, credential use-check) branches on
   `kind`. (Guards the heuristic-kind mislabel.) Given this, `KindAgent` deferral is acceptable.
- **I-4 — Durable `sub`↔principal binding.** OpenBao entity *names* are mutable/reusable; a renamed/
   recreated entity must not inherit a principal's grants. **Satisfied by construction:** the token
   `sub` is the entity **UUID** (proven, see point 2), a non-reassignable identifier — a recreated
   entity gets a new UUID and thus a new Principal, inheriting nothing. Only the deferred
   readable-name-claim follow-up would reintroduce a name-immutability requirement.
- **I-5 — Fail-closed init + resolution.** Never resolve without a verified signature. A configured
   issuer that fails discovery/JWKS init fails closed for its tokens (boot fails on misconfiguration —
   never silently narrow OR widen trust). The multi-verifier accept-loop denies only when **all**
   verifiers reject; it never short-circuits on a claimed `iss`.
- **I-6 — Root-of-trust for the SA→OpenBao step.** The pod's projected K8s ServiceAccount token used
   to log in to OpenBao is **audience-restricted to OpenBao**; OpenBao-issued OIDC tokens are short-TTL;
   neither the SA token nor the OIDC token ever persists to the graph.

## Charter alignment

- **§1.6 (one identity/authz/audit for humans, services, AND agents).** The multi-issuer resolver +
  OpenBao workload identity finally gives *all three* Principal kinds a real cryptographic proof under
  the ONE model, one authz (OpenFGA/tuples), one audit — closing the agent/service gap the charter
  demands and the dev header papered over.
- **§1.4.** OpenBao (already in the spine's optional set) can serve OIDC — no *mandatory* second IdP;
  Zitadel stays optional (human SSO where its directory earns it). Required deps unchanged.
- **§1.5.** The `OIDCResolver` is the sovereign contract; issuers (OpenBao, Zitadel, Keycloak) are
  swappable transports. Multi-issuer makes that literal.
- **§1.3 / sovereignty.** Any compliant issuer works (no lock-in); per-Cell OpenBao = a self-contained,
  cut-off-survivable identity floor (ADR-0100 alignment).
- **Deny-by-default preserved.** Activating real authz only *tightens* — anonymous is denied, grants
  are explicit, the boot guard forbids the dangerous dev-header-with-OIDC combination.

## Consequences

- **Positive:** the M2 + Syncer e2es become runnable through REAL authz (unblocking the standing
  blocker); workload/agent identity becomes cryptographic (§1.6 met); one OpenBao per Cell is now the
  Cell's KMS + PKI + KV + **IdP** — a genuinely self-contained identity+crypto floor.
- **Negative / trade-offs:** multi-issuer resolution is new security-sensitive code (must fail closed
  on every issuer; a token verifying under the wrong issuer must never map to a foreign Principal);
  the `sub`=principal-name binding couples OpenBao entity naming to the CaC principal namespace (stated
  as an invariant, tested); OpenBao K8s-auth + `identity/oidc` is real operator setup (seeded in dev,
  documented for prod). Full human login flow (authorization-code) remains a separate slice.
- **Follow-ups:** `KindAgent` via an explicit token claim (heuristic only yields human/service today);
  **readable-name Principals (§1.8 diagnosability) — BOUND to the same gate as `KindAgent`:** the
  `openbao/<uuid>` Principal is deterministically traceable (the bootstrap prints the uuid↔entity map)
  but opaque in audit/descent, so a uuid→name resolve at the audit/UI edge (or the role-claim-template)
  must land *before* any human-facing prod audit/descent surface ships (guardian flag 1); SPIFFE/mTLS
  as an *alternative* workload transport under the same resolver contract; auto-deriving runner/`use`
  grants from estate objects (today hand-authored tuples); the in-cluster continuous Syncer e2e (now
  unblocked).
- **Migration note (guardian flag 3, fail-safe).** Moving a deployment from single-issuer/empty-
  namespace (bare-`sub` Principal) to multi-issuer forces namespacing and *changes* existing Principal
  ids (`<uuid>` → `openbao/<uuid>`), orphaning previously-authored tuples. The failure direction is
  **deny** (safe), but operators must re-namespace the affected tuples in the same change.

## Alternatives considered

- **SPIFFE/mTLS → Principal for workloads.** A valid transport, but it needs a whole new
  attestation+validation path; OpenBao-OIDC reuses the *existing* Bearer resolver and the OpenBao we
  already run. Kept as a future alternative transport under the same contract, not the first mover.
- **Zitadel as the single IdP for humans AND workloads.** Rejected as the *default* — a central IdP is
  a cross-DC dependency a cut-off Cell can't rely on; per-Cell OpenBao is the sovereign floor. Zitadel
  stays an optional human-SSO issuer via multi-issuer.
- **Keep `devPrincipalHeader` and just load tuples into a real OpenFGA.** Rejected — it would activate
  authz-decision without real *identity*; the dev header trusting a spoofable string is exactly what
  workload identity must replace. (Phase A does stand up real OpenFGA; Phase B replaces the identity.)
- **Auto-derive all grants from estate objects now.** Deferred — orthogonal usability work; the
  hand-authored tuple manifest already reconciles correctly.
