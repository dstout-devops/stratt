# ADR 0155 — The account nobody offboards: correlating an AWX login to an identity, without claiming one

- **Status:** **Proposed** (2026-07-31, steward). Charter review by hand — this session's rules bar the
  subagent; §1.2/§1.6/§2.1/§2.4/§1.8 answered inline. **No new dependency.**
- **Date:** 2026-07-31
- **Deciders:** steward
- **Charter sections:** §1.2 (projections, never a second truth), §1.6 (one Principal model),
  §2.1 (single write-owner), §2.4 (no implicit precedence), §1.8 (never hide diagnosis)
- **Closes AWX-017.** Extends ADR-0079 slice 3 (the SCIM identity projection) with a second
  identity key. Uses the soft-edge-and-its-absence pattern of ADR-0085, as sharpened by ADR-0154.
  **Does not reopen ADR-0130 D3** (`AWX-005`), which declined role grants on grounds this does not
  address.

## Context

The object-model audit files AWX-017 as:

> correlating `ansible.user` to the SCIM identity — the AWX analogue of ADR-0079 4a's
> leaver-credential Finding, where **a local AWX account matching no known identity** is the account
> nobody offboards; it needs a username-resolvable identity key on `user` Entities, which is a
> decision about the identity plane.

Both halves already ship and cannot see each other. ADR-0130 D1 projects AWX's local account table as
`ansible.user`, and is explicit that it may claim neither `identity.subject` nor `identity.name`,
because `EnsureIdentitySubjectOwner` registers the SCIM projector as sole owner of both (§2.1 —
"not a preference; it is a registration error"). ADR-0079 slice 3 projects IdP identities as `user`
Entities keyed `identity.scimId: <idp>/<scimId>`.

So the two sit in one graph with **nothing joining them**, and the security question — _which AWX
logins correspond to no person the IdP knows?_ — is unanswerable. Those accounts are exactly the
ones an offboarding process misses, because offboarding runs against the IdP.

### The join is the whole problem

AWX knows a **username**. The identity plane keys by **`<idp>/<scimId>`**. There is no shared
identifier, and the obvious bridges are each wrong:

- **Let the AWX plugin write an identity fact** — refused by §2.1, and refused again by ADR-0035's
  reasoning, which ADR-0130 quotes: letting a foreign directory mint identity by naming a user makes
  the model hostage to that namespace.
- **Have the operator configure "this Controller authenticates against that IdP"** — a convention
  typed into an env var, joined by string equality. That is precisely the shape ADR-0154 spent its
  length repairing: when the convention is broken the edge drops, indistinguishably from a genuine
  no-match. Repeating a defect one ADR after fixing it is not a plan.
- **Key identities by bare username** — sound only if usernames are unique across every configured
  IdP, which nothing guarantees.

## Decision

### D1 — the SCIM projector emits a second identity key, and only when it is UNAMBIGUOUS

A `user` Entity gains `identity.userName: <lowercased userName>` **alongside** its existing
`identity.scimId` key — but only when that userName appears in exactly **one** configured IdP.

The projector is the only component that can make this call, and that is why it makes it: it
enumerates every IdP in one pass, so it alone can see that `jsmith` exists in two directories. When
it does, **neither** entity gets the key, and nothing links. That is not a gap — it is the correct
answer. Two candidate people is not a person, and picking one would be exactly the implicit
precedence §2.4 refuses.

**Lowercased, and that is measured rather than assumed.** RFC 7643 §4.1.1 defines SCIM `userName` as
unique across the provider's Users with `caseExact: false` — so two identities cannot differ only by
case, lowercasing cannot merge two distinct people, and it makes the join robust against a Controller
that stores `JSmith` where the IdP stores `jsmith`.

**This is a KEY, not a Facet.** It claims nothing about the person: `identity.subject` stays solely
the projector's (§2.1), and adding a second way to ADDRESS an entity the projector already owns
contests no ownership. It is the same move that lets the AWX half point at `ansible.playbook` — a
pointable scheme, never a writable namespace.

### D2 — AWX emits a SOFT edge, and its ABSENCE is the finding

`ansible.user --same-account-as--> identity.userName:<lowercased username>`.

The host resolves it and **drops it when the target does not exist** (no vivify, §1.2). A dropped
edge means one of exactly two things, and both are worth a Finding:

- no IdP identity has that username — **the account nobody offboards**; or
- the username is ambiguous across IdPs — Stratt genuinely cannot say who it is.

The Baseline (`awx-account-unlinked`, `requiredRelations: [same-account-as]`) is the same
relation-presence mechanism ADR-0085 built and ADR-0154 sharpened. `damping: 2` absorbs projection
lag, exactly as the orphan-template Baseline does.

**`same-account-as` asserts a correspondence, not an identity.** It says the AWX account and the IdP
identity share a login name — which is a fact about two strings, checkable, and all the offboarding
question needs. It does not say they are the same principal, and **nothing reads it for
authorization**: `TestINV3_AuthzConsultsNoGraph` fails the build if the authz package imports graph
at all, so this is structurally unable to become an access decision (ADR-0079 INV-3).

### D3 — a service account is not a leaver, and the estate says which is which

AWX deployments run automation under local service accounts that will never have an IdP identity, and
firing on them forever would train an operator to ignore the Finding — the failure mode ADR-0130 D4
avoided by NOT firing on inactive accounts.

The Baseline is scoped by a **View**, so an estate excludes its service accounts by declaring them
out of `awx-users` (or by declaring a narrower View for this control). That keeps the judgement in
Git where it is reviewable, rather than in a heuristic here guessing from a name prefix — a
`svc-`-detector in the plugin would be core inventing a naming convention for somebody's estate.

## Consequences

**The security question becomes answerable** — "which AWX logins match no person the IdP knows" is a
View away, and it is the AWX analogue of ADR-0079 4a's leaver-credential Finding, which is what the
audit asked for.

**A multi-IdP deployment links less, honestly.** Every username colliding across IdPs goes unlinked
and fires. That is a true statement about what Stratt can determine, and the Finding text says which
of the two causes applies is for the operator to check — but it does mean the control is noisier in a
multi-directory estate, which is stated here rather than discovered.

**The `identity.userName` key is now part of the identity plane's surface.** Anything else that knows
a person only by their login name can point at it the same way. That is a deliberate widening of
ADR-0079's projection and should be weighed the next time a Source wants to correlate people.

**Not addressed:** role grants (`AWX-005`), which ADR-0130 D3 declined for reasons this ADR does not
touch — the read has no clean shape, and a convincing grant graph invites being used as an
authorization truth regardless of INV-3. That decision stands.
