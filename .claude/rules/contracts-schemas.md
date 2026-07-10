---
paths:
  - "**/*.schema.json"
  - "**/contracts/**"
  - "**/facets/**"
  - "**/blueprints/**"
---

# Contracts, Facets & Blueprints rules — charter §1.1, §1.5, §2.2, §2.4

- **Type the seams, not the world (§1.1).** A schema may attach only to a plugin boundary or a named
  Facet. **No whole-Entity schemas. No universal ontology.** Every Facet schema must be *demanded by
  a shipping Contract* — if nothing consumes it, it must not exist (§9 "ontology creep").
- **Contract derivation ladder (§2.2):** hand-written (core) > tool-derived (tofu plan JSON, OpenAPI
  import) > MCP-declared-and-pinned. **Only the top rung is admissible for Syncers**; all rungs are
  admissible for Actions/Actuators.
- **Sovereign contracts (§1.5):** our connector contract is our own; REST/gRPC/subprocess/MCP are
  transports beneath it. **All plugin schemas are pinned and hash-verified at registration; schema
  drift is detected and blocking, never silently absorbed.**
- **Claim types (§2.4)** — every Facet a compiled Baseline manages is claimed as:
  - **exclusive** — one Assignment per Entity; a double-claim is a **compile error**; or
  - **additive** — set-union (`ensure contains`, not `ensure exactly`) with per-element provenance.
  There is **no implicit precedence anywhere** — this is the anti-GPO axiom. Never add a
  "last-writer-wins" or priority field.
- **Routing keys are per-capability maps, never scalars (§2.4):**
  `mgmt.channels: {apps: intune, certs: ansible}` — co-management is the default case, not an edge.
- **Blueprints are versioned; Assignments pin a Blueprint version.** Version bumps roll through rings
  with compile-diffs (§4.3). Authorship follows trust tiers bound to the facet-ownership registry —
  the platform team stewards, never gatekeeps (§2.4, §9).
- **Lifecycle:** every Intent kind carries `onRemove: retain | revert | remove` (default `retain`);
  withdrawn-but-retained state always raises an orphan Finding (§2.4). Removal semantics live in the
  schema, never in tribal memory.
