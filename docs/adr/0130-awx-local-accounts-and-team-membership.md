# ADR 0130 — AWX's local accounts are an estate fact, not an identity source and never an authz one

- **Status:** **Accepted** (2026-07-26, steward) and **implemented** — schema pinned, accounts and
  membership projected, Baseline shipped, `task ci` green. Charter review by hand (this session's rules bar the
  subagent); §1.1/§1.2/§1.6/§2/§2.1/§9 answered inline. **No new dependency.** Vocabulary reviewed by hand
  — `ansible.user` beside the existing `user` Entity kind is the close call; see D1.
- **Date:** 2026-07-26
- **Deciders:** steward
- **Charter sections:** §1.2 (projections, never a second truth), §1.6 (one Principal model, one
  authorization model), §1.8 (never hide diagnosis), §2 (frozen vocabulary), §2.1 (Source ownership), §9
  (no ontology creep)
- **Reconciles with:** ADR-0009 (Principals + tuples are authorization), ADR-0028 (OpenFGA), ADR-0035
  (SCIM: the identity home is a born-here registry, and group→team is a **CaC mapping**, never
  auto-teaming), ADR-0041 (per-key label ownership), ADR-0079 (identity as a cross-cutting dimension —
  INV-1/2/3 and the §2.1 single-write-owner gate), ADR-0127/0128/0129 (the sibling projection decisions)
- **Closes:** `AWX-003` and `AWX-004`. **Explicitly does NOT close `AWX-005`** — see D3.

## Context

The audit's Tier-2 item: the mirror has orgs and teams but **no people**, so "who can launch this?" is
unanswerable, and an `ansible.team` is an empty container. It is also the item the audit said needed a
decision _before_ code, because AWX RBAC facts are exactly the kind of thing that must not become a second
authorization truth beside OpenFGA.

Reading the prior art turned that caution into hard constraints rather than a worry, and it reshaped the
decision:

- **`identity.subject` has a single write-owner.** `EnsureIdentitySubjectOwner` registers the SCIM
  identity projector as sole owner of the namespace **and** the `identity.name` label key (§2.1 / ADR-0041
  / ADR-0079 slice-3 gate), with the comment spelling out that "a pull syncer may not claim either without
  displacing this owner." So AWX users **cannot** be projected as `user` Entities carrying
  `identity.subject`. That is not a preference; it is a registration error.
- **INV-3 is enforced structurally, not by discipline.** `TestINV3_AuthzConsultsNoGraph` fails the build if
  the `authz` package ever imports `graph`. So nothing projected here can reach an access decision even by
  accident, which is what makes projecting AWX's authorization facts safe to consider at all.
- **ADR-0035 already refused auto-teaming**, for a reason that applies verbatim: letting a foreign
  directory mint authorization principals by naming a group makes the permission model hostage to that
  namespace. AWX's teams are exactly such a foreign directory.

## Decision

### D1 — AWX users project as `ansible.user`, a mirror of AWX's **local account table**

A seventh owned namespace for the controller half: `username`, `email`, `isActive`, `isSuperuser`,
`isSystemAuditor`. Not `identity.subject`, for the ownership reason above — and the ownership rule is
pointing at something true rather than merely in the way. **An AWX local account is not an identity; it is
an estate fact about AWX.** The IdP is the identity SoR, AWX's user table is AWX's own, and the two are
different systems of record that happen to describe overlapping people. Projecting the second **as** the
first would be precisely the second truth §1.2 forbids.

**Vocabulary (§2), the close call.** `user` is not a banned identifier, and the graph already has a `user`
Entity kind from the SCIM projector. `ansible.user` is deliberately a **different kind**, not a
same-kind-different-identity: an AWX account and an IdP identity are different objects, and merging them on
a shared kind while they carry no shared identity key would produce two Entities of one kind that look like
they should be one. The `ansible.` prefix does the same quarantining job it already does for
`ansible.credential` beside `CredentialRef` (ADR-0128 D2) and `ansible.template` beside Workflow.

**On projecting people as Entities at all** — ADR-0035 warned this would "sweep them into Views (which
dispatch Ansible)". ADR-0079 slice 3 already crossed that line for `user`/`group`, so the precedent
exists; and the failure mode is bounded by an existing guard rather than an argument: a target with no
`mgmt.address` cannot be rendered, and ADR-0117 D5 made a Run that actuates nothing a **failure** rather
than a green no-op. A View that scoops up accounts fails loudly.

**Fidelity caveat (§1.8), stated because it will surprise someone:** `/users/` is RBAC-filtered by the
token. A non-superuser token projects the subset it can see, so the mirror shows _what this credential can
see_, not necessarily every account. That is honest projection of an external system we do not own
(PLG-1), and it is the kind of thing that reads as a bug when it is a property.

### D2 — Team membership is an edge; it is **never** consulted for authorization

`ansible.team --has-member--> ansible.user`, from `/teams/{id}/users/`. Same-source — both ends owned by
the controller half — so it always resolves, unlike the cross-source `runs` edge.

This is a **read-only estate fact and nothing else.** Stratt authorization remains Principals + OpenFGA
tuples (ADR-0009/0028), team membership in Stratt remains the CaC group→team mapping ADR-0035 chose over
auto-teaming, and INV-3 keeps the graph out of the decision path structurally. What this buys is the
question the audit asked — _who is on this team in AWX, and therefore what could they launch there today_ —
answered about **the system still running the automation**, which is the whole point of a mirror during a
migration.

### D3 — Role grants (`AWX-005`) are deliberately NOT projected, and this ADR does not close them

The audit filed users, membership, and role grants as one slice. They are not one decision, and shipping
two of three is the honest outcome:

- **The read has no clean shape.** AWX exposes grants as `/roles/` (every assignment in the deployment, an
  unbounded read) or per-object `access_list` (an N+1 over _every projected object_, on top of the per-team
  and per-workflow N+1 reads ADR-0129 already added). Neither is a poll-interval read against a Controller
  we do not own.
- **It is the part that most looks like an authorization truth.** A projected grant graph is one query away
  from being used as one, and "INV-3 stops it" is an argument about mechanism, not about what people will
  build on top of a convincing-looking permission graph.
- **Its value is the lowest of the three.** Membership plus superuser visibility answers most of the
  practical question; the full grant matrix mainly matters at cutover, and cutover reads AWX directly.

Booked as **AWX-005**, still open, with the read-shape problem recorded as the thing to solve first.

### D4 — Ship the consumer: `awx-superuser-review`

A facet-observation Baseline over a new `awx-users` View asserting `isSuperuser == false`. Every AWX
superuser raises a Finding — which is the point, not noise: "who has god mode in the automation platform"
is an audit question with a small, enumerable answer, and periodic review of that list is the control.
`isActive == false` raises nothing: a disabled account is hygiene, not a finding.

This demands the `isSuperuser` field, so the schema has a facet-reading consumer rather than only its write
path (the stronger of the two §1.1 framings, per ADR-0128 D5).

## Charter alignment

- **§1.2.** AWX stays the SoR for its own account table; nothing is written back; remediation flows to AWX
  (INV-2's rule, applied to a new SoR).
- **§1.6 / ADR-0009.** One authorization model, unchanged. Nothing here is read by the evaluator, and
  `TestINV3_AuthzConsultsNoGraph` is what enforces it.
- **§2.1.** No contested ownership: `identity.subject` and `identity.name` stay solely the SCIM projector's;
  this claims only `ansible.*`.
- **§9.** One namespace, with a named consumer (D4) — not a person ontology.

## Consequences

- **Positive.** `ansible.team` stops being an empty container. The AWX estate's account and membership
  picture becomes queryable **during** a migration, when it matters most. Superuser review becomes a
  standing Finding rather than a thing someone remembers to check.
- **Negative — a third N+1 read.** `/teams/{id}/users/` per team, per poll, on top of ADR-0129's per-workflow
  node reads and the seven collection reads. Same trade, same reason (`Enumerate` fails the whole Observe on
  any one error, §1.8), and it is now three ADRs deep — **which is itself the finding**: the projection's
  per-poll cost against an external Controller is growing one decision at a time, and no ADR has owned it.
  Booked as **AWX-018**: a poll-cost budget for the controller half (cadence separation for expensive
  sub-reads, or a documented ceiling), before a fourth N+1 lands.
- **A convincing-looking permission picture that is not one.** Someone will eventually ask why Stratt does
  not enforce with it. The answer is INV-3 and it is structural, but the question is now inevitable, and D3
  declines to make it worse.
- **Follow-ups booked:** **AWX-005** role grants (read shape first) · **AWX-017** correlating
  `ansible.user` to the SCIM identity — the `identifies`-shaped edge ADR-0079 4a used for certificates,
  whose payoff is the AWX analogue of its leaver Finding: **a local AWX account that matches no known
  identity**, which is the account nobody offboards. It needs a username-resolvable identity key on `user`
  Entities, which is a change to the identity plane and therefore its own decision.

## Alternatives considered

- **Project AWX users onto the existing `user` Entity via `identity.subject`.** Rejected in D1: the §2.1
  single-write-owner gate forbids it outright, and it would be wrong even if permitted — an AWX local
  account and an IdP identity are facts from two systems of record, and collapsing them authors a truth
  neither system asserts.
- **Add a username identity scheme to `user` Entities so AWX can point at people directly.** Attractive —
  it makes membership a cross-source edge with the same drop-is-signal property as `runs`. Rejected _for
  now_ because it changes the identity plane (username collisions across IdPs are exactly why the SCIM key
  is `<idp>/<scimId>`), which is a decision about identity, not about AWX. That is AWX-017.
- **Project role grants too, completing the slice.** Rejected in D3 on read shape, risk, and value — and
  recorded as still-open rather than silently dropped.
- **Project nothing; teams stay empty containers.** Defensible on the "don't mirror people" instinct, and
  rejected because it leaves the audit's actual question unanswered while the estate is still running on
  AWX, which is the exact window a mirror exists for.
