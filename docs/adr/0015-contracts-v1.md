# ADR 0015 — Contracts v1: pinned, hash-verified JSON Schema at the seams

- **Status:** Accepted
- **Date:** 2026-07-12
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.1, §1.5 (schemas as data; drift is blocking), §1.8, §2.2 (derivation ladder), §2.3, §8 (Phase 2)

## Context

Phase 2's keystone: everything downstream (OpenTofu output-derived Contracts,
SchemaForm/SchemaTable UI laws L3/L7/L8, MCP declared-and-pinned schemas) consumes
this machinery. Until now Step params were actuator-interpreted with hand-rolled
checks that surfaced only at dispatch time — the slice-7 e2e hit exactly that
(`params.script is required` after the Workflow was already running).

## Decision

1. **Documents live in the repo-root `contracts/` module** — the public, reviewable
   location — exporting only `embed.FS`; the module deliberately contains no logic
   (§1.5: data, never language classes). The binary must be able to validate before
   any database exists, so the embedded copies are the validation source; the
   `graph.contract` table is the public **pin/audit record** and the drift tripwire.
2. **v1 documents (all hand-written — ladder rung 1):**
   `actuators/ansible.input` (play?, eeImage? — empty play = the gather default),
   `actuators/script.input` (script required, interpreter?), and
   `facets/os.kernel` — the one Facet schema, **demanded by** the ansible gather
   output (§1.1: a Facet schema exists only when a shipping Contract demands it).
   All use `additionalProperties: false`: a typo is a contract violation, not a
   silently ignored key.
3. **Pinning + drift:** Hash = sha256 over the exact document bytes. Startup
   registers every shipped document: same name+version+hash ⇒ noop; same
   name+version with a different hash ⇒ **refuse to boot**, error naming both
   hashes; the fix is a new version file, never a mutated pin. Versions are
   whole documents encoded in the filename: `<name>.schema.json` is v1;
   `<name>.v2.schema.json` is version 2 of the **same** Contract name — the
   loader derives (name, version) from the path, so the registry's
   per-version pinning axis is real (guardian review fix).
4. **Validator:** `santhosh-tekuri/jsonschema/v6` v6.0.2 (dependency-scout
   RECOMMEND — reference-grade draft-2020-12 conformance; single-maintainer bus
   factor and a 13-month tag gap flagged: pin exactly, watch releases quarterly,
   fork-and-maintain is a realistic fallback). Violations surface with JSON-pointer
   locations (§1.8: diagnosis never hidden).
5. **Validation seams (all structural, none advisory):**
   - `POST /runs`: params checked **before any Run row exists** → 400 with pointers.
   - CaC declarations: `ValidateTrigger`/`ValidateWorkflow` check actuation params
     at parse — a bad file fails at plan/reconcile, not dispatch. (Immediately
     caught a `source:`-vs-`script:` typo in our own test fixture.)
   - Projector `upsertFacetTx`: pinned Facet schemas validate at the write path
     itself — every writer (Normalizer and Run provenance) passes through;
     uncovered namespaces pass by design (§1.1).
   - Actuator `Prepare` checks remain as defense-in-depth.
   - An actuator **without** an input Contract is refused — uncontracted Step
     surfaces must not exist (§2.3).
6. **Read API:** `GET /contracts` returns every pinned document + hash + rung —
   the SchemaForm rendering source. (No single-get: Contract names contain `/`;
   the list is three documents. Revisit if the set grows.)

## Consequences

- Malformed Steps now fail at the door (API 400 / CaC file error) with pointer
  detail instead of inside a running Workflow.
- Facet writes to `os.kernel` are schema-enforced for every writer; other
  namespaces are untouched until a Contract demands their schema.
- Deferred to the OpenTofu slice: Step **output** Contracts and cross-Step output
  binding; tool-derived (rung 2) and MCP-declared (rung 3) documents; Facet-schema
  hash pinning inside the facet-ownership registry row (today the pin lives in
  graph.contract alongside the others).
- Follow-ups: `ui:*` hint layer + SchemaForm consuming GET /contracts (ADR-0003
  L8); quarterly release-watch on the validator (scout); Contract docs for future
  in-tree Actuators are mandatory at introduction.
