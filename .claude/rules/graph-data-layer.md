---
paths:
  - "**/models/**"
  - "**/migrations/**"
  - "**/normalizers/**"
  - "**/*.sql"
---

# Graph / data-layer rules — charter §1.2, §2.1, §3

**The graph is a projection, never a second truth.** This is enforced *in the data layer*, not by
convention (§1.2). When touching models, migrations, or normalizers:

- **Only Normalizers and Run provenance may write Entity/Facet/Relation attributes.** Build this as a
  structural guarantee (constrained write paths / row-level ownership), not a code-review norm.
- **Every attribute carries Provenance** — which Run/Syncer wrote it, when, from which Source.
  Provenance is non-optional and by construction has exactly one answer per attribute (§2.1).
- **Facet ownership registry:** every Facet namespace has one declared write owner scoped by View.
  Two writers to one namespace is a **registration error** — the schema/migration must make a double
  claim impossible, not resolve it by precedence. There is no implicit precedence anywhere (§2.4).
- **Postgres, not a graph DB** (§3): relational + JSONB + recursive CTEs + GIN facet indexes. Design
  for estate scale 10⁵–10⁶ Entities. Views are saved, **versioned, CaC-declared** queries.
- **Schemas attach at Facets only** — never a whole-Entity schema (§1.1, §9 "ontology creep").
- **Rebuildability:** the projection must be reconstructable from Sources + Run provenance. No datum
  may exist that can only live in the graph (that would make it a second truth).
- **Desired state lives in Git**, not the database. The DB is truth for *projections and provenance*,
  not for intent.

## Postgres idioms — it's a PG-18 store, use it as one

Concrete review rules (leverage what makes Postgres Postgres; don't write generic ANSI SQL). Each
ties to a Stratt structure above:

- **JSONB Facets: containment + GIN, never `->> LIKE`.** Query Facet documents with the containment
  operators (`@>`, `?`, `?|`) backed by a **GIN** index — `WHERE facet @> '{"status":"active"}'`, not
  `WHERE facet->>'status' = 'active'` (the latter can't use the GIN index). This is the concrete form
  of the "GIN facet indexes" bullet above.
- **`TIMESTAMPTZ`, never `TIMESTAMP`.** Every Provenance stamp (`at`, `lastSeen`, `observedAt`) and
  audit time is timezone-aware. A naive `TIMESTAMP` silently drops the offset and corrupts ordering
  across Sites/regions.
- **Constrain the domain in the schema, not the app.** Use `CHECK` constraints and `DOMAIN`s (or
  `ENUM` for closed sets like Run/Finding status) so invalid state can't be projected. Validation at
  the write boundary is the data-layer's job, not a Normalizer convention.
- **`CITEXT` for case-insensitive uniqueness** (identity keys / labels that must match
  case-insensitively) rather than `lower()` sprinkled through queries.
- **Enforce single-writer with a constraint, not a trigger race.** The "one declared write owner per
  Facet namespace, scoped by View" rule (above) should be a **UNIQUE / EXCLUDE (GiST) constraint** on
  (namespace, view) so a double claim *fails at write time* — the anti-GPO axiom made structural
  (§2.4), not resolved by precedence.
- **Write-path guarantee via RLS / constrained roles.** "Only Normalizers and Run provenance may
  write" is enforceable with **Row-Level Security** policies + a least-privilege role (`GRANT
  SELECT,INSERT,UPDATE` on the specific tables, not `ALL ON ALL TABLES`), so the guarantee is
  structural, not a code-review norm (§1.2).
- **Provenance-stamping triggers fire only on real change:** `... FOR EACH ROW WHEN (OLD.* IS
  DISTINCT FROM NEW.*)` — a no-op re-projection must not churn provenance or wake reconcilers.
