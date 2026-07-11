# Contracts & Facet schemas

Pinned, hash-verified **JSON Schema documents** — data, never language classes
(charter §1.5, §2.2, ADR-0002).

- `facets/` — one schema per Facet namespace.

**This directory is intentionally sparse.** Charter §1.1: a Facet schema may
exist only when a shipping Contract demands it — if nothing consumes a schema,
it must not exist (the anti-ontology-creep discipline, §9). Contracts land in
Phase 2 (§8); the first Facet schemas land with them, validated at the
Projector write path and pinned by hash in the ownership registry.
