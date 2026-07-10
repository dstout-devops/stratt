---
name: vocabulary
description: >-
  The frozen Stratt vocabulary — the Named Kinds, the banned terms, and the AWX→Stratt migration
  mapping (charter §2). Load this when naming or reasoning about any core-model concept, designing an
  API path / DB table / CLI noun, or translating an AWX/AAP/SCCM/Jamf concept into Stratt terms.
---

# Stratt Vocabulary (charter §2 — frozen at v1.0)

Naming is API. These names are frozen at v1.0 with a formal deprecation policy thereafter. Use them
exactly; do not invent synonyms where a Named Kind exists.

## Graph plane (§2.1)
- **Entity** — a node: anything with identity (host, VM, device, cert, VPC, namespace, account).
  Identity keys + labels + typed document + per-attribute Provenance.
- **Relation** — typed directed edge (`runs-on`, `member-of`, `issued-by`, `depends-on`).
- **Facet** — a named, schema'd fragment of an Entity's document (`net.ipv4`, `os.kernel`,
  `cert.expiry`, `apps.installed`, `mgmt.channels`). **Schemas attach here and nowhere else.**
- **Facet ownership registry** — every Facet namespace has one declared write owner (Syncer,
  Blueprint output, or team) scoped by View. Two writers = registration error.
- **Provenance** — per-attribute stamp: which Run/Syncer wrote it, when, from which Source.
  Non-optional; exactly one answer per attribute.
- **View** — a saved, **versioned, CaC-declared** graph query producing a live Entity set. Unifies
  inventory / smart inventory / Jamf Smart Groups / SCCM collections. Not UI-editable when referenced
  by an Assignment.

## Sources & Connectors (§2.2)
- **Source** — an external system of record, registered with CredentialRefs and trust settings.
- **Connector** — the versioned integration package for a Source. Ships some combination of:
  - **Syncer** — projection: bulk enumeration + delta ingestion → **Normalizer** →
    Entities/Facets/Relations with provenance. Requires full-fidelity native transports; MCP is not
    an admissible Syncer transport yet.
  - **Action** — one typed operation (`create-vm`, `assign-policy`, `revoke-cert`): input Contract +
    output Contract + idempotency/dry-run declaration.
  - **Emitter** — event producer (webhook receiver, poller, stream subscriber) → typed events.
- **Trust tiers** — `core` (in-tree) / `verified` (reviewed, signed) / `community` (signed,
  sandboxed). Applies to Connectors, Actuators, and Blueprints alike.

## Execution plane (§2.3)
- **Actuator** — execution-engine plugin that runs *tool content*: `ansible`, `opentofu`, `script`,
  `helm`, `mcp`, future `packer`. Interprets content, produces many effects.
- **Contract** — JSON Schema on a Step's inputs/outputs. Ladder: hand-written > tool-derived >
  MCP-declared-and-pinned. Only the top rung is admissible for Syncers.
- **Step** — one contracted invocation: (Actuator + content ref + params) or (Action + params).
- **Workflow** — Temporal-backed DAG of Steps with success/failure/always edges, **Gates**
  (human/policy approval), convergence, nesting.
- **Run** — execution instance: status, per-target results, event stream, artifacts, cost/usage,
  provenance written.
- **Trigger** — anything that starts a Run (Temporal Schedule, Emitter event × CEL, manual,
  API/MCP). Cron is just one Emitter.
- **Bundle** — cosign-signed OCI artifact of content + deps for pull-mode agents.
  **Site** — remote execution locus (satellite dispatcher + NATS leaf).

## Intent layer (§2.4)
- **Intent** — a small declarative document of *what*, by payload kind (`Intent/Application`,
  `Intent/Certificate`, `Intent/FileSet`, `Intent/Access`, `Intent/Config`, …). Carries
  `onRemove: retain | revert | remove` (default `retain`).
- **Assignment** — binds an Intent to a View, per environment/ring, optionally behind a Gate.
- **Blueprint** — composition that compiles (Intent × Assignment × View membership) into Baselines +
  remediation Workflows, **routed by capability-scoped Facets** (per-capability maps, never scalars).
- **Claim types** — **exclusive** (one Assignment per Entity; double-claim = compile error) or
  **additive** (set-union, per-element provenance). No implicit precedence — the anti-GPO axiom.
- **Baseline** — compiled/hand-written desired state: View selector + expected Facet values / check
  Step + remediation Workflow ref + cadence.
- **Finding** — a drift/compliance/orphan result: Entity + Baseline + observed-vs-expected diff +
  severity + Evidence ref.
- **Evidence** — immutable (object-locked) artifact bundle backing a Finding; the audit/PCI unit.

## Identity (§2.5)
- **Principal** — human or service/agent identity, one kind, one authz/audit/cost model.
- **CredentialRef** — pointer + injection policy to brokered secrets. Material never persists;
  injected only into execution pods at spawn. `use-without-read` is a first-class grant.
- **Authorization** — ReBAC via OpenFGA; View-scoped execution. Platform RBAC is itself CaC.

## BANNED in core-model identifiers
`inventory` · `playbook` · `job template` / `job_template` · `CI` · `CMDB` · `resource`
Each is a tool-specific rendering or a namespace collision. Fine in *tool content* and compat docs;
never in Stratt's own type names, tables, API paths, CLI nouns, or Facet namespaces.

## AWX / AAP → Stratt migration mapping (§2, §5.6)
| Old term | Stratt |
|---|---|
| job template | **Step preset** |
| smart inventory / inventory / collection / Smart Group / SCCM collection | **View** |
| survey | **input Contract** (with UI hints) |
| job | **Run** |
| workflow (AWX) | **Workflow** |
| credential | **CredentialRef** (+ **Source** trust settings) |
| project (SCM) | tool **content ref** on a Step |
