# ADR 0041 — Per-key Entity-label ownership (the label mirror of facet_owner)

- **Status:** Accepted
- **Date:** 2026-07-15
- **Deciders:** Project steward (dstout)
- **Charter sections:** §2.1 (ownership registry — exactly one write owner, no shared claim), §2.4 (no
  implicit precedence — the anti-GPO axiom), §1.2 (projections, never a second truth; enforced in the
  data layer, not by convention), §4.3 (Run-provenance writes bypass ownership, like facets);
  resolves the deferral logged in **ADR-0038 §Honest deferrals** ("a general per-key/per-writer
  Entity-label ownership model — the deeper fix for the label bag") and its ADR-0037 predecessor.

## Context

The config-mgmt Syncer track (ADR-0037/0038/0039) established source-scoped **facets**: two Sources
correlating onto one `dns.fqdn` Entity carry both `<source>.node.*` facet sets side-by-side, each
provenance-stamped, because `graph.facet_owner` gives each namespace exactly one owner (§2.1) and the
`enforce_facet_owner` trigger makes a cross-owner write structurally impossible (§1.2). Selectable data
was moved onto those facets precisely because the shared Entity **label** bag had no such guard.

The label bag was the last un-owned write surface on the Entity. The Projector's UPDATE arm did
`SET labels = $incoming` — a **whole-blob replace**. Two consequences, both §2.4 violations hiding in
plain sight:

1. **Cross-source clobber.** A host co-managed by two Sources with disjoint label keys (e.g. msgraph's
   `graph.name` and vcenter's `vcenter.name`) would have one Source's labels erased every time the
   other re-projected — silent last-writer-wins, the exact implicit precedence the charter forbids.
2. **No-label wipe.** A Syncer that emits no labels (chef/puppet/salt write none) writing `{}` would
   wipe a co-managed Entity's entire label bag on every cycle.

Labels are also the primary **View** selector (GIN `@>` containment), so the bag is a hot path and any
fix had to leave the read side and its index untouched.

## Decision

**Mirror `facet_owner` for label keys, and change the Projector from whole-blob replace to per-key
merge.** Three parts, all in the data layer (§1.2):

1. **`graph.label_owner` registry** (migration `00023`) — `key text PRIMARY KEY`, `owner_kind CHECK IN
   ('syncer','blueprint','team')`, `owner_ref`, `view_scope`. The label analogue of `facet_owner`: one
   owner per key by construction, a double claim is a registration error, not a precedence rule.

2. **`enforce_label_owner` trigger** (BEFORE INSERT OR UPDATE ON `graph.entity`) — for a **Syncer**
   write, every label key the write *adds or changes* versus the prior bag must be owned by that Syncer;
   unchanged keys (owned by other Sources) are preserved and never re-checked. An unregistered key is
   rejected (§2.1: registration precedes writes); a key owned by another owner is rejected (§2.4).
   **Run/blueprint-provenance writes bypass** the check, exactly like `enforce_facet_owner` (§4.3) —
   `stratt.workspace`, `createvm`'s `aws.region`, etc. are Run outputs, not Syncer projections.

3. **Projector per-key merge** — the UPDATE arm becomes `SET labels = labels || $incoming::jsonb`. A
   writer contributes only its own keys; other Sources' keys on a correlated Entity survive; a no-label
   writer's `{}` is a no-op. The INSERT arm is unchanged (a new Entity carries the full incoming bag,
   and the trigger checks all its keys). `RegisterLabelOwner`/`GetLabelOwner` mirror the facet-owner
   registry API (idempotent same-owner, `ErrOwnerConflict` on a different owner); each Connector
   declares `LabelOwners()` and registers them in `Register()` alongside `RegisterFacetOwner`.

Today the owned keys are already disjoint and source-scoped — `graph.name` (msgraph), `vcenter.name`,
`aws.region`/`aws.name`, `cert.commonName`; chef/puppet/salt write no labels — so ownership is clean
with no contention. The registry makes that discipline **structural** rather than incidental, and open
to future Blueprint- and team-owned label keys via the same `owner_kind`.

## Charter posture

- **§2.1/§2.4** labels now have the same one-owner invariant as facets; cross-source clobber and
  no-label wipe (both silent last-writer-wins) are structurally impossible, not fixed by convention.
- **§1.2** enforced by a Postgres trigger in the write path, not by a review norm; the projection stays
  rebuildable and Source-authoritative.
- **§4.3** Run/blueprint provenance bypasses ownership exactly as it does for facets — the projection
  guard applies to Syncer writes only.
- **Read side untouched** — the merge changes only the write SQL; the `labels @>` GIN selector and the
  50k-Entity View gate are unaffected (no per-key row rewrite, no reader migration).

## Alternatives considered

- **Per-key label *rows* (normalize labels out of the JSONB bag).** Rejected: rewrites every reader and
  the View-membership query from a GIN `@>` containment to an `EXISTS`/join, a hot-path regression on
  the primary selector, plus a backfill — all to buy per-key label *provenance* that nothing demands.
  The blob + merge + trigger gives the same ownership guarantee reader-side-free.
- **Keep whole-blob replace, forbid cross-source label correlation.** Rejected: it would forbid exactly
  the cross-source unification the config-mgmt track celebrates, and push all selectable data onto
  facets permanently — labels would be second-class for no structural reason.
- **A multi-owner label registry / precedence field.** Rejected as charter-hostile — the §2.4
  last-writer-wins the whole data layer is built to forbid.

## Reviews

- **charter-guardian:** _(recorded in the slice commit)_ — §2.1/§2.4 one-owner invariant extended to
  labels; §4.3 bypass symmetric with facets; §1.2 data-layer enforcement.
- **vocabulary-linter:** `label_owner`/`owner_kind`/`owner_ref` are infra columns; no Named-Kind misuse,
  no banned term.
- **No dependency-scout** — zero new dependencies (a migration + projector SQL + registry methods).

## Honest deferrals

- **Facet-consistent label staleness.** A label key a Syncer stops emitting persists until overwritten
  (the merge never deletes keys). This matches facet semantics (a facet no longer projected persists
  until re-owned) and is a projection property, not a regression; per-key label *tombstoning* on
  full-enumeration is a future concern if a Contract ever demands it.
- **Per-key label provenance.** The bag still carries one Entity-level provenance stamp; genuinely
  per-key label provenance would need the normalized-rows design rejected above and is deferred until
  something demands it.
- **`view_scope` not trigger-enforced.** The column is stored but the trigger looks ownership up by
  `key` only — a symmetric deferral with `facet_owner`. All current owners are unscoped (NULL); when a
  View-scoped label owner ships, enforcement must be narrowed to the View (a §2.1 gap otherwise).
- **Cross-source Entity liveness** remains the separate ADR-0038 deferral (per-source presence), not
  touched here.

## Consequences

The Entity label bag now has the same structural one-owner guarantee as facets: two Sources can
correlate onto one host and contribute disjoint labels without clobbering, a no-label Syncer no longer
wipes a co-managed Entity, and a Syncer writing a key it does not own fails the upsert with a clear
`P0001`. The last un-owned write surface on the Entity is closed — with no reader change, no new
dependency, and no cost to the View query gate. ADR-0038's per-key-label deferral is resolved.
