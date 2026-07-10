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
