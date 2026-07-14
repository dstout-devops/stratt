# ADR 0035 — SCIM 2.0 Service Provider: IdP-driven Principal lifecycle + group→team authz

- **Status:** Accepted (Commit 1 — the SP + identity registry; Commit 2 — authz projection + deactivation block)
- **Date:** 2026-07-14
- **Deciders:** Project steward (dstout)
- **Charter sections:** §8 (Phase-3 "SCIM"), §1.2 (projections vs a second truth; born-here records
  vs the estate graph), §1.6 ("one Principal model, one authorization model, one audit stream"),
  §2.1 (one declared write owner; the Facet ownership registry this mirrors), §2.5 (Authorization is
  CaC; CredentialRef — material only as a hash); ADR-0009 (the Principal/authz wedge + tuple
  authorizer), ADR-0018 (the Emitter token-hash ingest pattern this reuses), ADR-0028 (View-scoped
  execution authz that team membership now feeds), ADR-0034 (the one audit stream SCIM provisioning
  records into)

## Context

The charter promises "one Principal model, one authorization model" (§1.6). Identity was **ambient**:
`ResolvePrincipal` yields a Principal from the OIDC `sub` (or the dev header), and there was **no
Principal registry** — no store of who exists, no active/deactivated state, no membership table.
Team membership lived only in CaC `authz/tuples.yaml`. Two gaps: (1) **no offboarding** — a
deactivated user had no representation, and any still-valid access token kept resolving; (2) the
**directory was not wired to authorization** — memberships were hand-maintained in Git.

**AAP has no SCIM.** Red Hat AAP derives org/team from SAML/LDAP attribute-mapping *at login*; there
is no SCIM push-provisioning story. So a real SCIM 2.0 Service Provider is a statement feature — the
same shape as ADR-0034's audit stream: ship the capability enterprises pay for, as an open one.

Steward scope: **Users + Groups**. Three design forks were reasoned to the enterprise "better-than-AAP"
bar (with research):
- **Identity home = a born-here registry in a new `scim` schema, NOT graph Entities.** Every real SCIM
  SP (GitLab, GitHub Enterprise, Datadog, PagerDuty) stores provisioned identities in a dedicated
  identity subsystem, never as objects in the product's domain graph. Charter-clean too: the graph is
  the **estate** (§1.2); Principals are the **actors on it** (§2.5) — projecting humans as Entities
  would sweep them into Views (which dispatch Ansible), muddy the two constrained graph write-paths,
  and invite "resource"-thinking about people (a banned term). Mirrors the `audit`-schema call.
- **Group→team = a CaC mapping declaration**, not auto-teaming. IdP group names are directory-chosen,
  renamed, and differ across IdPs; auto-teaming makes the permission model hostage to that namespace
  and lets a *directory* admin mint authorization principals just by naming a group. The mapping gives
  **separation of duties**: the directory owns *who is in a group*; the platform admin owns *what a
  group can do* (team→role grants, Git-reviewed). Kept simple — one line per group; unmapped groups
  are projected-but-ungranted.
- **Deactivation = revoke grants AND block at request time.** Grant-revocation alone is AAP-parity (a
  still-valid token lingers). The request-time block closes the access-token-TTL window. Research
  confirmed `active:false` PATCH is the primary offboarding signal for both Okta and Entra (DELETE is
  deferred/rare); soft-deactivation preserves audit history — which the tombstoning registry does.

## Decision (Commit 1 — the SP + identity registry)

1. **A new `scim` schema (migration 00020): a born-here projection, two declared owners.**
   `scim.identity` / `scim.group` / `scim.group_member` are the projection (written **only** by the
   SCIM handler — an IdP push, not a Principal action); `scim.idp` is the CaC config (written **only**
   by the desired-state engine). `scim.identity.principal_id` is the value we expect the OIDC `sub` to
   carry — the join key that makes the registry **back the one Principal model** (§1.6), not create a
   second one. `active` is a soft flag; DELETE tombstones (`deleted_at`). Not `graph.*`, not a writable
   CMDB — a rebuildable read-model of the IdP (§1.2).

2. **`/scim/v2` Service Provider, mounted OUTSIDE `/api/v1`** (the IdP is not a Principal). Bearer
   token → `sha256` → `subtle.ConstantTimeCompare` against every registered `scim.idp.token_hash`;
   the token both authenticates and **identifies which IdP** the push belongs to (multi-IdP by
   construction). Reuses the Emitter token discipline (ADR-0018; CaC holds only the hash, §2.5). Core
   surface: Users/Groups CRUD, `ServiceProviderConfig`/`ResourceTypes`/`Schemas` discovery (Okta/Entra
   probe these), `PATCH active:false` (+ Entra's string-bool), Group member `PatchOp`s, DELETE.
   **Hand-rolled wire formats — no SCIM library, no new dependency** (mirrors the hand-rolled SIEM
   drivers).

3. **Provisioning records into the one audit stream (§1.6, ADR-0034):** each mutation emits an
   `audit.event` with the IdP as a service Principal (`principal:idp:<name>`), actions
   `scim.user.provision|deactivate|delete`, `scim.group.member-add|remove`. Offboarding is itself
   auditable and SIEM-forwardable.

4. **CaC IdP registration:** a `scim/*.yaml` kind (`{ name, tokenHash, groupMappings: [{group, team}] }`),
   validated like an Emitter (tokenHash 64-hex, no duplicate group), reconcile-controller-applied via
   the Git path (matching notify/subscriptions — not added to the CLI `DesiredState` wire).

## Decision (Commit 2 — authz projection + request-time deactivation block)

5. **Group→team membership joins the tuple union; SyncTuples stays the ONE authoritative writer.**
   The `TupleAuthorizer` unions a CaC set (`LoadTuples`) with a projected set (`SetProjectedTuples`);
   `Check` and `Snapshot` read the union, so the OpenFGA sync still projects a single authoritative
   desired state — CaC ∪ SCIM-projected. Membership rides the **existing** `team:<name>#member`
   usersets — **no authz-model change**. The reconcile loop composes the union each cycle from
   `store.ProjectedMemberships` (active members of mapped groups → `principal:<id> member team:<t>`).

6. **§2.1 one-owner guard.** `cacOwnsMappedTeam` refuses to project if a mapped team's membership is
   ALSO declared in CaC (a `member` tuple on that team) — previous grants kept, logged loudly. A
   team's membership is owned by CaC **XOR** an IdP mapping, never both — the Facet two-writer
   registration-error posture. No implicit precedence.

7. **Request-time deactivation block (`SCIMGate`).** A SCIM-managed **human** the IdP deactivated is
   denied at resolve time, even before the token expires. Scoped strictly to human subjects;
   **service/agent and unknown-to-SCIM subjects are never gated** (unknown-to-SCIM ≠ deactivated —
   enabling SCIM must not lock out non-SCIM or break-glass identities). **Fail-open** on a lookup
   error: a DB blip must not add a new denial to every human (the request fails at its grant check
   anyway if the store is truly down). REST and MCP inherit it via `api.Server.ResolvePrincipal`; the
   `/api/v2` AWX façade has its own resolver and applies the SAME `SCIMGate` (`awxfacade.Config`), so
   the compat write surface (launch/cancel) is not a weaker offboarding path (§1.6 symmetry —
   charter-guardian Violation 1, fixed in-slice).

## Charter posture

- **§1.2** identity is a born-here operational record in the `scim` schema — a rebuildable projection
  of the IdP (IdP authoritative), NOT the estate graph and NOT a writable CMDB.
- **§1.6** one Principal model (SCIM backs it via `principal_id`; OIDC still resolves; the gate acts on
  active-state), one authz model (the same tuple authorizer + OpenFGA), one audit stream (provisioning
  recorded).
- **§2.1** a mapped team's membership has exactly one owner; the guard makes double-ownership a hard
  refusal, not a precedence resolution.
- **§2.5** authorization policy/role-grants stay CaC and Git-reviewed; only membership *population* is
  IdP-projected. The IdP token is a CaC `sha256`; material never stored.
- **§1.8** SCIM `Error` envelopes; deactivation is an explicit 401; provisioning audited.

## Alternatives considered

- **Project identities as graph Entities.** Rejected: conflates actors with the estate, sweeps humans
  into Views, and muddies the constrained write paths (§1.2). The registry is the real-world shape.
- **Auto-provision a team per IdP group.** Rejected: leaks the directory namespace into authorization
  and lets a directory admin mint authz principals (privilege-escalation surface). The CaC mapping is
  the separation-of-duties answer.
- **Revoke grants only (no request-time block).** AAP-parity: a valid access token lingers. The block
  is what makes offboarding demonstrably better.
- **A second tuple writer for SCIM membership.** Rejected: two writers to the tuple store = a second
  truth. The union + single SyncTuples keeps one authoritative writer.
- **A SCIM library / SDK.** Hand-rolling the JSON-over-REST surface keeps the SP dependency-free
  (dependency-scout no-op), consistent with the hand-rolled SIEM drivers.

## Honest deferrals

- **`principal_id`↔OIDC-`sub` correlation** is `externalId`-first with a `userName` fallback; a per-IdP
  claim-mapping config is a follow-up. If an IdP's `externalId` ≠ its OIDC `sub`, the operator must
  align them (or the deactivation gate and grant projection key on the wrong value).
- **Best-effort SCIM ingest:** a push during a DB outage 500s and the IdP retries (SCIM clients retry);
  no outbox. The audit emit is best-effort (logged on failure), like the audit-stream ingest (ADR-0034).
- **Enterprise-SCIM extras** beyond Okta/Entra's core needs: Enterprise User schema extensions, `/Me`,
  bulk ops, ETag concurrency, and cursor pagination.
- **Multi-IdP** is schema-supported (token identifies the IdP) but the harness proves one IdP.
- **CLI `stratt apply`** does not carry the `scim` kind (matching notify/subscriptions) — the reconcile
  controller (Git path) is the apply surface; a CLI apply would see an empty scim set.
- **One-owner conflict is a reconcile-time refusal, not a plan-time compile error** (charter-guardian
  Flag 2). Both sides are CaC (the `member` tuple in `authz/tuples.yaml`, the mapping in `scim/*.yaml`),
  but they are validated by different subsystems (`authz.LoadTuples` vs `desiredstate`), so
  `cacOwnsMappedTeam` detects the double-claim only at reconcile — it fails safe (drops the projected
  set, keeps previous grants, logs `Error`) but does not surface in `stratt plan`/apply or as a
  Finding. Cross-checking the two at plan time so the apply fails (§2.4 compile-error posture) is a
  follow-up.
- **`principal_id`↔`sub` mismatch silently no-ops the gate** (charter-guardian Flag 3): if an IdP's
  stored `principal_id` (externalId/userName) diverges from the OIDC `sub`, `LookupActive` returns
  "unknown" and the human is not gated — a quiet offboarding hole. The per-IdP claim-mapping config
  above is the fix; until then the operator must align the two.
- **Intra-`scim`-schema two-writer split is convention, not row-level enforcement** (charter-guardian
  Flag 4, accepted): `scim.idp` (desired-state) vs the projection tables (SCIM handler) are separated
  by code organization, matching the audit-schema precedent — §1.2's data-layer mandate governs the
  estate graph, not this born-here schema.
- **A UI identity viewer** and Zitadel-as-SCIM-client harness wiring (vs the scripted bootstrap).
- The **live kind e2e** (bootstrap pushes → mapped team gains runner → provisioned Run succeeds →
  deactivate → grants vanish + request 401s → non-SCIM admin unaffected) is harness-wired
  (`task dev:scim:bootstrap`) but not run this session; the store, tuple union, gate, and full SCIM
  HTTP path are proven by integration + unit tests (incl. a real-DB handler test).

## Consequences

"One Principal model" gains a real lifecycle: an IdP provisions and deprovisions Principals over SCIM
2.0; group membership drives authorization live through the existing team model; and deactivation both
revokes grants and blocks at request time — a demonstrably-better-than-AAP offboarding story, as an
open capability. Adding an IdP is a CaC `scim/*.yaml` + a bearer token; adding a group→team binding is
one mapping line. The registry is a born-here projection reusing the boring spine — no new dependency,
no new authz model, no new graph write-path.
