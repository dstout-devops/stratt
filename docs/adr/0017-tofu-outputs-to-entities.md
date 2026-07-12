# ADR 0017 — tofu outputs → Entities: the provision→configure seam

- **Status:** Accepted
- **Date:** 2026-07-12
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.1, §1.2, §2.2 (rung 2), §2.5, §4 (showcase #1), §8 (Phase 2)

## Context

Charter showcase workflow #1: "tofu apply (Gate on plan) → outputs Contract →
Normalizer projects Entities → ansible Step against the View — one provenance
chain, zero glue scripts." ADR-0016 stopped at apply; this closes the loop.

## Decision

1. **Reserved output `stratt_entities`.** A tofu module declares Entities into
   existence by emitting this output: a list of `{kind, identityKeys, labels}`,
   validated against the **rung-1 Contract `outputs/stratt_entities`** (pointer
   errors; a malformed value fails the Run — statuses only escalate, so the
   tool's rc=0 cannot hide it). v1 observations carry **no Facets** (§1.1: no
   facet schema exists until a Contract demands one; the configure Step gathers
   facts afterward — that is the showcase flow, not a limitation).
2. **Projection path.** Observations flow through the same seams facts do:
   `Interpreted.Entities` → `dispatch.Result.Entities` → `CollectFacts` →
   `ProjectFacts` → **`RunProjector().UpsertEntities` with Run provenance**
   (§1.2 admits run-provenance writes structurally; identity conflicts surface
   as non-retryable errors and never merge — the Syncer posture). Every
   projected Entity gains the automatic **`stratt.workspace` label**.
3. **v1 binding = labels + pre-declared View.** The next Step targets a View
   selecting the author's labels (and/or `stratt.workspace`). True parametrized
   binding ("outputs.instances[*] → View parameters") needs View templating —
   deferred with this rationale: labels already give deterministic selection
   with zero new machinery, and templated Views deserve their own §2.4-reviewed
   design (selection must stay declarative data, not become an expression
   language).
4. **Rung-2 output Contracts.** The driver runs `tofu output -json` after a
   successful apply; the type expressions derive a JSON Schema document
   (string/number/bool/list/set/map/object/tuple/dynamic; deterministic key
   order so identical shapes hash identically), registered as
   `opentofu/<workspace>.outputs` at rung `tool-derived`.
5. **Derived-rung pinning semantics (extends ADR-0015):** shipped rung-1
   documents block on drift; **derived documents auto-version** — same latest
   hash is a noop, a new hash inserts version+1 (`RegisterDerivedContract`).
   The tool legitimately changes its own schema; the version history is the
   audit trail. Rung-1 names keep the blocking path.
6. **Sensitive outputs are redacted by the driver before emission** — the
   event stream is not a secret channel (§2.5); the derived schema marks
   sensitive properties in their description.

## Consequences

- Provision → configure runs as one Workflow with one descent: WorkflowRun →
  apply Run (projects Entities) → configure Run against the View selecting
  them — all links queryable, all writes provenance-stamped.
- Re-applies correlate on identityKeys — no duplicate Entities (§1.2).
- The graph now has a third writer shape: run-provenance Entity creation.
  Rebuildability holds: re-running the apply reproduces the observations.
- charter-guardian findings applied in-slice: plan-json events redact
  sensitive planned values (walking after_sensitive / sensitive_values —
  the ADR-0016 plan path had the gap); `stratt.*` label keys in
  stratt_entities are reserved and rejected visibly (never silently
  overwritten); RegisterDerivedContract's read-then-insert documents its
  reliance on the state-backend lock for same-workspace serialization.
- When Step-output binding lands, consumers must PIN a derived version and
  bumps must roll with compile-diffs — never resolve "latest" (guardian F1).
- Follow-ups: parametrized Views; per-output Facets when a Contract demands
  them; Step output binding into params (needs the same templating decision);
  `GET /contracts` filtering as derived documents accumulate versions.
