# ADR 0060 — Multi-source Facet projection: keep every signal, declare the authoritative view

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout), charter-guardian
- **Charter sections:** §1.2, §1.4, §1.8, §2.1, §2.4, §2.5
- **Amends:** [ADR-0056](0056-estate-as-code.md) decision 2 (per-Facet SoR was "one source per namespace, overlap
  fails"; this relaxes it to *many sources, one declared-authoritative*). **Relates to:**
  [ADR-0041](0041-per-key-entity-label-ownership.md) (the label analog) · [ADR-0059](0059-network-topology-primitives.md)
  (surfaced the need).

## Context

Facet ownership is registered **globally per namespace** (`graph.facet_owner` PK on `namespace`); ADR-0041 does
the same for label **keys**. This was right when each namespace had one natural source. It breaks the moment two
real systems legitimately know the same thing:

- **NetBox** (the IPAM source of truth) and **Crossplane** (which just *built* the subnet and knows its as-built
  CIDR) both project `net.subnet`. vSphere and a cloud Syncer would too.
- The **declared** inventory and the **ansible** gather both report `os.kernel`.

The global lock forces a choice no real estate should make: **monopolize** a namespace to one source forever, or
**cripple** every other plugin to dodge the lock. We nearly did the latter — stripping Crossplane's `net.subnet`
projection so it wouldn't collide with NetBox. Two steward directions correct the course:

> **A plugin must expose everything its system can do** — the collision is a flaw in the ownership model, not a
> reason to cripple the builder. **More signal is universally superior to turning signal off** — keep every
> source's projection and declare a *preferred/authoritative* one as the effective truth.

The old model conflated **"two systems know about `net.subnet`"** (fine, and valuable) with **"two writers
silently fight over one Entity's `net.subnet`"** (the real second-truth risk). It banned the first to prevent
the second, *and* it threw away the loser's signal. Both are wrong.

## Decision

**1. Many sources may project one Facet namespace — full-featured plugins.** Drop the estate-wide per-namespace
lock: multiple grants MAY declare the same `FacetNamespace`. Registration records `(namespace, source)`, so an
**unregistered** source still cannot write it — the §2.5 bounded-grant gate is fully preserved. This stops
pretending "a namespace has one source in the whole estate."

**2. Every projection is RETAINED, per source — signal is never dropped (§1.8).** The Facet grain becomes
per-source: **`(entity_id, namespace, prov_source_id)`**, where the grain key is the **Source** (`prov_source_id`),
made **NOT NULL** — never `prov_writer_ref` (which changes per Run → unbounded rows, guardian **M1**). Every facet
write carries a *registered* source; a write with no source is rejected, not NULL-keyed. Each source is the **sole
writer of its own** row, so no source overwrites another's — §1.2 single-writer holds trivially *per source row*,
no cross-source fight, no lost signal. Two run-write kinds are distinguished (guardian **M2**, preserving §4.3
flap-damping): a **damp write** (a remediation Run reflecting a just-applied change ahead of Syncer lag) carries
the *damped Syncer's* `source_id` and writes **through** that Syncer's row (`prov_writer_kind='run'`, same grain)
— so it still damps the visible value; a **genuine new observation** (e.g. a Crossplane Actuator write-back)
carries its **own** registered source and gets its own row. `facet_history` re-keys to
`(entity_id, namespace, prov_source_id, version)` so two sources' version streams never collide (**M6**).

**3. The effective "truth" per `(Entity, namespace)` is the DECLARED authoritative source — explicit, not implicit
precedence (§2.4).** A consumer (a View facet-predicate, a Baseline check, an API read) resolves the effective
value through a per-Facet-namespace **source authority declared in `sources/` CaC** (ADR-0056): "for `net.subnet`,
NetBox is authoritative." This is a Git-reviewed, rebuild-deterministic authority assignment — the *opposite* of
the silent priority/last-writer-wins field §2.4 forbids. It amends ADR-0056 decision 2: overlapping Facet
namespaces are no longer a plan-time failure; they are legal, and authority is declared rather than monopolized.

**4. Reads resolve by KIND — additive-union for predicates, declared-authority for scalars — never an arbitrary
tiebreak (§2.4, guardian M4/M5).** A **View facet-predicate / membership test** is **additive-union**: the Entity
matches if ANY retained source-row satisfies the predicate (§2.4's approved additive-union — the existing
`EXISTS(...)` selector already yields this; it is now intentional). A **scalar/authority read** (a Baseline
drift-check, `mgmt.site` dispatch routing) resolves to the **declared** authority; else, if exactly one source
projects it, that value; else it raises an **ownership-contention Finding** (framework `ownership`) carrying
**both values + both sources** as Evidence and **refuses to collapse** — it never picks a "stable source-name
order" winner (alphabetical-wins is last-name-wins dressed as determinism, an implicit precedence §2.4 bans). The
same single resolver serves descent (show all) and predicates/checks (the resolved value), so what is displayed
is what was matched (guardian S3).

**5. The re-based Facet guard is registration-only, decided under the PK lock (§2.5 / §1.2, guardian M3).** The
trigger's sole check is the **bounded-grant registration**: is `(NEW.namespace, NEW.prov_source_id)` a registered
grant? Per-`(Entity, namespace, source)` single-writer is then **the primary key itself** — mutual exclusion is
the unique index, decided against `OLD` on `ON CONFLICT`, never a secondary `SELECT ... FROM graph.facet` (which
would reintroduce a first-write TOCTOU). The old **"a Run write is always admissible to any registered
namespace" bypass is dropped**: a Run write is now gated by *its source's* grant (decision 2's write-through
carries the damped Syncer's source, which is registered) — this *tightens* §2.5. Authority resolution (decision
4) is **read-time** and never rewrites provenance, so it opens no third write path — only Normalizers/Run
provenance write (§1.2). Source identity is bound to the authenticated grant at the write layer, not caller input
(guardian S1) — `prov_source_id` is now a PK dimension AND the authority anchor, so it must not be spoofable.

**6. Labels (ADR-0041) follow the same principle** — per-source values + declared authority — but the label
**bag** is one jsonb column with a single Entity-level provenance stamp, so per-source label retention needs a
per-key provenance change first. This ADR ships the **Facet** mechanism (Facets have per-row provenance) and
sequences the label plumbing; the `os.kernel` case (a Facet) is solved here, `os`-as-a-label follows.

## Charter alignment

- **§1.2 — projections, never a second truth.** Each source's projection is its own provenance-stamped row;
  nothing overwrites another's. The *effective* value is a **declared, deterministic, rebuildable** choice, not an
  arrival-order or last-writer artefact — so a graph wipe-and-resync re-derives the identical effective truth. No
  Entity ever silently holds two conflicting `net.subnet`s; it holds two *observations* and one *declared* truth.
- **§2.4 — no implicit precedence.** Authority is declared in Git (`sources/` CaC), reviewed, and surfaced-as-a-
  Finding when absent — never a silent priority field or last-writer-wins.
- **§2.1 — one writer, made precise + honest.** Single-writer binds `(Entity, namespace, source)`; the estate-wide
  monopoly (mechanism, not principle — it added zero per-Entity protection while banning legitimate co-existence)
  is dropped. Registration still gates who MAY write (§2.5).
- **§1.4 / §1.8 — full-featured plugins, maximal signal.** A plugin projects everything its system reports; the
  model absorbs multi-source reality; every observation is retained and diagnosable.

## Consequences

- **Positive:** full-featured plugins co-exist and keep all signal — NetBox *and* Crossplane both project
  `net.subnet`; declared *and* ansible both report `os.kernel`. New overlapping Connectors stop colliding at
  registration. The effective-truth is a reviewed, rebuild-deterministic declaration. Richer provenance: you can
  see what *every* source thinks a subnet is, plus which is authoritative.
- **Negative / trade-offs:** the Facet store grows a source dimension (`(Entity, namespace, source)`), the
  `facet_owner_check`/write-path triggers are re-based (a migration + careful test — the highest-care §1.2 surface,
  so this **re-guardian is a hard gate before implementation**), and the effective-value read path + the
  ownership-contention Finding are new. Authority declaration rides `sources/` CaC (ADR-0056 1–4), still pending —
  until it lands, the deterministic-default (SoR / stable order) is the interim authority.
- **Migration safety (guardian M7, §1.2 highest-care):** existing run-written facet rows have `prov_source_id`
  NULL — a NULL cannot enter the PK, so the migration **backfills before re-keying**: run-only namespaces get a
  reserved run source (or, since the graph is rebuildable, drop-and-let-resync); `facet_owner`'s existing
  `syncer`/`blueprint`/`team` owners re-key by `owner_ref` (the registration stays; the *authority* declaration
  lives in `sources/` CaC, never overloaded into this table).
- **Follow-ups:** the label per-key-provenance change (decision 6); the ownership-contention Finding + resolution
  UX; the retroactive entity-merge case (two pre-existing entities that each already hold the Facet — with the
  per-source grain their rows don't PK-collide on merge, but the merge path must preserve both); binding
  `prov_source_id` to the authenticated Source at every write layer (S1).

## Alternatives considered

- **Keep the global lock; cripple plugins to fit** — rejected: strips capability *and* signal to accommodate a
  model limitation (the failure this ADR corrects).
- **Reject the second writer + raise a Finding (single authoritative row, per-Entity from provenance)** — the
  first draft; rejected on the steward's direction: it throws away the loser's signal, and deriving ownership from
  the row's last-writer provenance is unsound (a §4.3 Run write silently evicts a Syncer's ownership; first-write
  TOCTOU) — the retain-all + declared-authority model avoids both by giving each source its own row.
- **Last-writer-wins / a precedence field** — rejected under §2.4 (implicit precedence).
- **Per-field Facet ownership** — rejected as over-fine; a Facet is one document, `(Entity, namespace, source)` is
  the right grain.
- **Source-qualified namespaces** (`netbox:net.subnet`) — rejected: two namespaces for one concept is itself a
  second truth (§1.2); a subnet's CIDR is a subnet's CIDR regardless of who observed it. Retain-per-source keeps
  the values distinct by *provenance*, not by minting parallel concepts.

## Reviews

- **charter-guardian (2026-07-18): SOUND-WITH-CHANGES** on the first-draft *reject-the-second-writer* model —
  principle upheld (§2.1/§2.4/§2.5/§1.2), but five must-fixes on the data-layer mechanism (Run-provenance eviction
  of ownership; first-write TOCTOU; durable rebuild-deterministic resolution; no third write path; keep both the
  registration + per-entity gates), plus surfacing both contending values (§1.8).
- **Steward direction (2026-07-18):** keep every signal + declare the authoritative view (do not reject/drop) —
  adopted, superseding the reject resolution. This **dissolves** guardian M1 (Run-eviction) and M2 (reject-TOCTOU)
  — each source owns its own row, nothing is rejected or evicted — and M4 (no third write path, authority is
  read-time). It **grounds** M3 (durable resolution) in ADR-0056's Git-declared per-Facet authority, and satisfies
  S8 (both values retained inherently). Guardian **M5** (keep both data-layer gates) and the should-fixes (pin
  Source-vs-Syncer as the ownership key; interim/hand-off semantics; state the retroactive-merge assumption; the
  `os.kernel` facet-vs-label example) are folded above.
- **charter-guardian re-review (2026-07-18): mechanism SOUND-WITH-CHANGES → folded.** The retain-all principle is
  charter-sound (§1.2/§2.4/§2.5); seven data-layer must-fixes on the mechanism, now folded into decisions 2/4/5 +
  the migration note: **M1** grain key = `prov_source_id` NOT NULL (never per-Run `prov_writer_ref`); **M2** §4.3
  damp writes write-through to the damped Syncer's source-row, Actuator write-backs get their own source; **M3**
  the re-based trigger is registration-only, decided under the PK lock (no secondary SELECT), and drops the
  "Run always admissible" bypass; **M4** no "stable source-name order" — predicates are additive-union, scalars
  resolve declared-authority-or-single-or-Finding; **M5** `FacetValuesByEntities` (the `mgmt.site` routing hot
  path) must resolve the effective value in-query, not last-row-wins; **M6** `facet_history` PK re-keys to include
  source; **M7** the migration backfills NULL run-source rows before re-keying. Flags folded: S1 (bind
  `prov_source_id` to the authenticated grant), S2/S3 (single resolver for descent + predicates). **The gate is
  cleared** — the mechanism is now specified to implement without a §1.2/§2.4 defect.
