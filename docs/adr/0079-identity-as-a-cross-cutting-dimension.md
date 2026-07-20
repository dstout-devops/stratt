# ADR 0079 — Identity is a cross-cutting projection dimension, not a lowest-level type

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** steward (dstout)
- **Reviews:** charter-guardian PASS-WITH-CHANGES (three must-fixes folded as INV-1/2/3 + the Contract-demand sufficiency clause + open `scheme` + single write-owner); vocabulary-linter CLEAN
- **Charter sections:** §1.1 (type the seams, not the world), §1.2 (projections, never a second truth), §2 (frozen vocabulary — Principal/Entity/Facet/Relation), §2.5 (secrets brokered), §9 (no ontology creep)
- **Supersedes framing of:** the implicit "certificates are a standalone `cert` island" model
- **Relates to:** ADR-0009 (Principal/authz/credential), ADR-0030 (`Intent/Certificate`), ADR-0050 (certificate reconcile Actuator), ADR-0035 (SCIM), ADR-0046 (substrate coordinates)

## Context

Stratt models identity in three disconnected places:
- **Certificates** are a first-class graph Entity (`cert` kind, `cert.identity`/`cert.expiry` facets) with a mature reconcile lifecycle (ADR-0050) — but an **island**: a `cert` has no edge to the subject it authenticates, nor to the Principal that wields it.
- **Users/groups** are ingested by SCIM (ADR-0035) into a separate `scim.*` schema that backs the **Principal/authz** plane — never projected into the graph. No connector projects users/groups as Entities.
- **Principals** (ADR-0009) are the authz actor identity, disjoint from graph Entities except an indirect `PrincipalID == OIDC sub`.

The modeling error this ADR corrects: **a certificate is not a sibling of identity — it is a *form* of identity.** A user, a group, a service account, a machine/host identity, a workload (SPIFFE) ID, and an X.509 certificate differ in **form**, not in **kind**. They are all things that can be *named*, *authenticated*, and *bound to*. Treating one form (`cert`) as its own lowest-level type, unrelated to its subject or its principal, fragments what is one concept and makes the highest-value questions unanswerable ("which identity does this credential authenticate, and who operates it?").

## Decision

**Model identity as a cross-cutting projection *dimension* — a shared Facet family plus binding Relations that many Entity forms opt into — NOT a universal `Identity` super-Entity, and NOT a set of disconnected per-form kinds.**

A universal super-type would be ontology creep (§1.1/§9, explicitly forbidden). Disconnected islands are the status quo we are fixing. The middle path — the charter's actual doctrine — is to type the **seam**:

### 1. The `identity/*` Facet family (typed at the connector boundary, §1.1)

**Sufficiency invariant (§1.1 — the missing half of the anti-creep guardrail):** rejecting the super-Entity is *necessary but not sufficient*. A Facet ships **only when demanded by a concrete shipping Contract** — that clause, not the shape of the noun, is what separates a legitimate seam Facet from a universal ontology in Facet clothing. **No `identity/*` Facet ships ahead of a named Contract that demands it**, and each is registered with a single write-owner in the facet-ownership registry (§2.1):
- **`identity/subject`** — the coordinates an identity carries: `name` (UPN/email/CN/SPN/serial), `scheme` (**open string**, not a closed core enum — see below; typical values `user | group | service | machine | cert | workload`), `authority` (the issuing IdP/CA/host), `status` (`active | disabled | expired | revoked`), and lifecycle timestamps. **Demanded by** the user/group Syncer's Normalizer output Contract (slice 3) and/or the leaver/orphan Baseline's check Contract (slice 4). Attaches to whatever Entity kind carries an identity.
- **`identity/credential`** — for credential forms (cert, key, token-ref): what *proves* the identity — validity window, algorithm, issuer — **never the secret material** (§2.5). **Demanded by** the certificate reconcile Actuator Contract (ADR-0050). The existing `cert.identity`/`cert.expiry` facets are the cert-form specialization and fold in here over time.

**`scheme` is an open, extensible string, not a closed core enumeration (§1.4).** A community connector introducing a new identity form (a SPIFFE workload, a cloud managed identity) must not require editing a central core enum — that would be a stewardship chokepoint against "community owns breadth." Core ships the well-known values as documentation; the field accepts any scheme a Normalizer projects.

### 2. Identity-binding Relations (the "forms are interchangeable / bound" insight, made concrete)
- **`identifies`** — a credential Entity → the subject it authenticates (a `cert` `identifies` a `service`/`host`/`user`). **This is the edge that ends the cert island.**
- **`member-of`** — `user` → `group`, `group` → `group` (nesting).
- **`authenticates-as`** — bridges a **Principal** (the authz actor) to its identity Entity. The authz and estate planes **connect without merging**: authorization still evaluates Principals + tuples (ADR-0009 unchanged); the graph merely gains the correlation. **Realized (slice 4) as a correlation ATTRIBUTE** — `principalId` on `identity.subject` — **not** a graph edge, because a real edge would require representing the Principal as a graph node, the plane-merge the guardian warned against. A Principal-keyed datum (an audit event, a Run, a cost line) correlates to the estate identity by joining on `principalId`; the authz evaluator never reads it (INV-3, enforced structurally).
- **`operates` / `owns`** (optional, later) — an identity → a resource, for leaver/orphan reach ("a departed user still owns three hosts").

### 3. Forms slot into the seam; none is the authority
- `cert` (exists) — retrofit: keep the kind + its reconcile lifecycle (ADR-0050); add `identity/*` + an `identifies` Relation so it participates.
- `user`, `group` (new Entity kinds) — carry `identity/subject`; projected from the IdP SoR (via the SCIM registry and/or a pull syncer — a later slice decides the first transport).
- `service`, `machine`, `workload` identities — future forms carrying the same Facets.

### 4. Enforceable invariants (data-layer, not aspiration)

These are the guardian's three must-fixes, promoted from stated intent to binding invariants each build slice must **enforce and prove** (a test), because §1.2 requires enforcement "in the data layer, not by convention":

- **INV-1 — projection write-path (§1.2):** identity Entity attributes are writable **only** by Normalizers and Run provenance — the same data-layer ownership already enforced for every other Entity. An identity Entity that could be authored directly would be a second truth.
- **INV-2 — remediation goes to the SoR, never the graph (writable-CMDB non-goal):** a "disable this user" / "change this membership" / leaver clean-up is an **Action against the identity SoR or desired-state-in-Git**, never a graph edit. `member-of` and `identity/subject` are read-only projections; authoring identity state in the graph is the writable-directory non-goal arriving through the back door, and is forbidden. The `operates`/`owns` Relations exist to *surface* leaver/orphan reach as Findings — the remediation of which flows out to the SoR.
- **INV-3 — authz never consults the graph (§1.6, ADR-0009):** authorization evaluation traverses **zero** graph Relations. `authenticates-as` is **correlation-only** — read by Views/Findings, never by the authz evaluator. Authorization stays on Principals + OpenFGA tuples exactly as ADR-0009 defines; the moment an access decision traverses the graph edge, the graph becomes load-bearing for authz and a second truth. Slice 4 ships a test asserting the authz path reads no graph Relation.

- **§2 vocabulary:** identity is expressed with the **existing** Named Kinds — `Facet`, `Relation`, `Entity`, `Principal`. It is **not** a new Named Kind and does not compete with `Principal`. `identity/*` facet namespaces, the `identifies`/`member-of`/`authenticates-as` Relation kinds, and the `user`/`group` Entity kinds are unfrozen domain data. **vocabulary-linter: CLEAN** — `user`/`group` do not collide with Principal's `human|service|agent`; `member-of` and `cert` are already charter-blessed instances.

## Charter alignment

- **§1.1 (type the seams):** identity is a Facet family + Relation vocabulary at the connector seam — many forms opt in; no whole-Entity identity schema. Each Facet is demanded by a shipping Contract (the sufficiency clause), so the family can never become a free-floating universal ontology.
- **§1.2 (projections):** every form projects from its SoR; the graph correlates, never authors — enforced by INV-1 (write-path) and INV-2 (remediation flows to the SoR), not by convention.
- **§2 (vocabulary):** built from frozen Named Kinds; no new noun, no collision with `Principal`. (Guardrail: `identity` must never become a Named Kind or a scalar owner field.)
- **§9 (ontology creep):** the explicit rejection of a universal `Identity` super-Entity is the anti-creep guardrail.

## Consequences

- **Positive:** one identity model across users/groups/services/machines/certs; the cross-form Finding no island can produce (expiring cert → subject → operator → leaver); certificates gain a subject and a principal instead of floating; a clean bridge (`authenticates-as`) from authz Principals to estate identities without merging the planes.
- **Negative / trade-offs:** it reframes the existing `cert` kind (a retrofit, not a rebuild — the reconcile lifecycle is untouched); it introduces new Entity kinds + facet/relation vocabulary (needs vocabulary-linter); the Principal↔Entity bridge must be drawn precisely or it risks the very plane-merge ADR-0009 avoided.
- **Neutral:** no code in this ADR — it fixes the model. Implementation is sliced (below).

## Slice roadmap (each its own ADR-gated increment)
1. **This ADR** — the doctrine (identity is a dimension; the Facet family + Relations; forms; SoR/Principal discipline). Charter-guardian + vocabulary-linter gated.
2. **`identity.credential` + cert retrofit — SHIPPED.** Defined `contracts/facets/identity.credential.schema.json` (demanded by the cert reconcile Contract, ADR-0050; `scheme` open per §1.4) and retrofitted the certissuer plugin to emit it alongside `cert.identity`/`cert.expiry` (more signal, not less), with the cert connector as its single §2.1 write-owner. The cert is no longer an island — it carries the cross-form credential projection. (Facet namespaces use the codebase's dotted convention — `identity.credential` — not the ADR's illustrative `identity/credential`.) The `identifies` Relation and `identity.subject` move to slice 3, where their subject Entities and demanding Contract exist (sufficiency invariant).
3. **User/group projection + `identity.subject` + `member-of` — SHIPPED (transport: project the SCIM registry).** `ProjectSCIMEntities` projects the SCIM registry into the graph as `user`/`group` Entities carrying `identity.subject`, with `member-of` Relations, on the SCIM reconcile cadence — the graph a rebuildable read-model of the registry (itself from the IdP SoR, §1.2). Status is projected from the SoR, never authored (INV-2). Written only by this Normalizer (INV-1). **§2.1 gate honored:** `EnsureIdentitySubjectOwner` registers the projector as the single write-owner of `identity.subject` + the `identity.name` label — a pull syncer may not claim them without displacing it. Transport chosen: the SCIM registry (data already ingested, no new creds), seam left open for a pull syncer later. **Remaining:** the `identifies` edge (cert → the subject it attests) — a cert-CN→subject correlation, its own small increment.
4a. **`identifies` edge + the leaver Finding — SHIPPED.** `CorrelateIdentities` links a credential Entity to the subject it attests (`identifies`, matching a cert's `subjectName` to a subject's name/email/principal) and raises an `identity/leaver-credential` Finding when a **valid credential attests a DEACTIVATED identity** — the cross-source (PKI × IdP) query no island model can answer. Remediation flows to the PKI SoR (revoke), never a graph edit (INV-2). Platform-computed (run provenance), idempotent, on the reconcile cadence. `owns`/`operates` (identity → infrastructure) awaits a data source (a future ownership Connector, or derivation from Run provenance) and is a later increment.
4. **The Principal↔identity bridge — SHIPPED.** `authenticates-as` realized as the `principalId` correlation attribute on `identity.subject` (projected from `SCIMIdentity.PrincipalID`) — a Principal-keyed datum joins to the estate identity without a principal graph node (no plane merge). **INV-3 enforced structurally**: `TestINV3_AuthzConsultsNoGraph` fails the build if the authz package ever imports the graph package — the authz decision path cannot traverse a graph Relation, so the identity edges stay correlation-only. This is the keystone: audit/run/cost (Principal-keyed) now correlate to users, groups (`member-of`), and — once slice 4a lands — the resources they operate.

## Alternatives considered

- **A universal `Identity` super-Entity** (one schema for user/group/cert/service) — rejected: textbook ontology creep (§1.1/§9); would attach a schema to whole Entities, not seams.
- **Keep the islands; just add a `user`/`group` connector next to the `cert` kind** — rejected: it is the status quo the user correctly challenged; it leaves certs unrelated to subjects and principals and forecloses the cross-form queries that are the entire point.
- **Fold identity into the Principal model** (make graph identities into Principals) — rejected: conflates the authz actor plane with the estate projection plane; ADR-0009 keeps them separate for good reason. `authenticates-as` connects them instead.
- **Make `identity` a new Named Kind** — rejected: §2 vocabulary is frozen; identity is a *dimension* expressed with existing Kinds (Facet/Relation/Entity), not a new noun.
