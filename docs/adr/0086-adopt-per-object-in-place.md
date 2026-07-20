# ADR 0086 — `adopt`: per-object, in-place, over the live projection (supersedes-in-part the one-shot importer)

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** steward (dstout)
- **Reviews:** charter-guardian PASS-WITH-CHANGES (ADR mandatory) — the reshaping is *more*
  charter-honest than the one-shot importer; read-fidelity model **(b)** confirmed and a targeted
  per-object read confirmed §1.2-clean (a read, never a graph write); four must-fixes folded (below).
  vocabulary-linter PASS — `adopt` (verb), `observed→adopted` (derived state, NOT a stored attribute),
  `adopted-from` (provenance label) all clean; flagged the internal `adoptRehomedSource` name overlap.
- **Charter sections:** §1.2 (projections, never a second truth — adopt writes desired state to Git,
  never the graph), §1.6 (agent-native, human-first — adopt is API-first, CLI/MCP/UI are clients),
  §1.8 (never hide diagnosis — the cutover is explicit + residual gaps reported), §5 (no auto-launch —
  adopt emits a reviewable declaration a human merges), §7.6 (strangler-fig — retire one route at a
  time), §2 (Named Kinds in output; foreign nouns only in provenance/report), §9 (no ontology creep)
- **Supersedes-in-part:** ADR-0025 (see §Supersession) — Decision #1 (one-shot `stratt import awx`
  read/verb) dies; Decision #2's transform *input* re-homes; #2 mapping logic, #3 (SCM content-ref
  `ansible.input.v3`), #4 (clone-in-EE) survive unchanged.
- **Builds on:** ADR-0085 (the `ansible.*` projection domain + relation-presence governance), the pure
  `awximport.Bundle` transform (kept), the read-only `connectors/awx` client (reused for the deep-read).

## Context

ADR-0025 shipped `stratt import awx`: a ONE-SHOT CLI verb that does its own fresh full-estate AWX
`/api/v2` read (`awx.Enumerate`), runs a pure transform (`awximport.Bundle`) mapping the AWX estate to a
reviewable Git desired-state bundle (Views, Workflows, CredentialRefs, survey→Contracts, report), and
writes it to a directory for `stratt plan -d`.

The steward's correction: **"we never import — the projection is always-on; we are connected and simply
know."** The AWX/ansible Connectors (ADR-0085) now project every AWX object into the graph continuously.
A one-shot full-estate self-read is a *second, divergent bulk read* of a system the Syncer already
mirrors — redundant. "Import" is also the legacy-tool verb (AWX "imports inventory") and smuggles in the
one-shot mental model we reject. The deliberate act is not ingesting data; it is **taking authority** over
one object we already observe — a strangler-fig cutover (§7.6), one route at a time.

## Decision

**Replace the one-shot importer with `adopt`: a per-object, in-place act sourced from the live
projection.** `adopt <kind> <identity>` takes an already-OBSERVED object and emits a reviewable CaC
declaration of a Stratt **Named Kind** (Workflow/View/CredentialRef) — a Git PR the operator merges. The
always-on projection is the **catalog**; adopt flips ONE object from foreign-executed (read-only
projection) to Stratt-executed (a Named Kind in Git).

### 1. Read-fidelity model — (b) catalog + targeted single-object read
The projection stays a LEAN catalog + locator (name, kind, org/team, source id, native object id via
provenance labels — never fattened). Adopt:
1. resolves the object from the projection catalog (a graph read) — the identity + native object id;
2. does a **single-object, read-only, transient** deep-read of just that object's full definition,
   reusing the ADR-0025 read-only `connectors/awx` client (`/api/v2/job_templates/{id}/` + its
   `survey_spec` / credential / workflow-node sub-reads) — **never `awx.Enumerate`, never
   re-enumeration, never persisted to the graph** (§1.2: this is a read; the output is Git desired
   state);
3. feeds the SAME `awximport.Bundle` transform (a per-object entrypoint over the deep-read result).

Rejected: **(a) fatten the Syncer** to project survey specs / credential shapes / node graphs / host
filters always-on — that is typing the world (§1.1/§9 ontology creep) to serve a rare deliberate act, and
makes the graph a heavier AWX mirror. **(c) projection-only best-effort** is §1.8-permissible but
knowingly lossy — against "more signal > less; fix the model, don't strip capability."

**Live-read is definition-truth (staleness rule):** the targeted read, not the possibly-stale catalog,
defines the emitted CaC. Adopt **fails loudly** if the object is gone at read-time — it never emits stale
CaC from the catalog alone.

### 2. `observed → adopted` is DERIVED, never a stored attribute (§1.2)
No `adopted` status is written onto the projection Entity (that would be a second truth). The state is
implicit and derivable: **a Named Kind exists in Git carrying `adopted-from` provenance** back to the
source object. Git wins; the projection becomes a secondary read-only record of the external system's
state. "Is this adopted?" is answered by the presence of the lineage-stamped Named Kind, not a field.

### 3. Provenance lineage on the adopted Named Kind (must-fix 2)
Every emitted Workflow/View/CredentialRef carries `adopted-from` lineage — source id + native object id,
in `awx.*`/`ansible.*` provenance labels only (the ADR-0025 `awx.*`-label latitude) — so descent (§1.8)
and audit answer "where did this declaration come from." Foreign nouns never leak into a Named-Kind
identifier (§2).

### 4. The cutover is EXPLICIT — no silent double-execution (must-fix 1, the mandatory core)
After adoption the same real object is BOTH still projected read-only (foreign-executed) AND declared in
Git (Stratt-executed). Left unmarked, the estate could run it in **both** places. The strangler cutover
(§7.6) must be diagnosable (§1.8), so adopt defines a **cutover governance signal**: a facet-observation
Baseline (ADR-0085 machinery) surfaces an adopted source object whose FOREIGN-side execution is still live
— e.g. an `ansible.schedule` still `enabled` for a template now carried by an `adopted-from` Git Workflow
— as a Finding: *"this AWX object is now Stratt-owned; disable its AWX schedule."* The adopt act writes the
`adopted-from` marker that this Baseline reads; the Baseline is a fast-follow slice. No stored `adopted`
attribute; the dual-execution risk is turned into an explicit, damped Finding, never a silent dual truth.

### 5. §1.8 residual report survives (must-fix 3)
Even a faithful deep-read cannot render some fields — approver identity AWX does not carry, irreducible
or/not/regex host filters, a manual-project placeholder play. The per-object report entry for residual
gaps survives; model (b) narrows the gap set, it does not eliminate the report. The abstraction never
hides the gap.

### 6. API-first, agent-native (§1.6 flag → decision)
Adopt is a **capability**, not a CLI feature: it is an OpenAPI operation under one Principal / one authz /
one audit / one cost model; the CLI, MCP, and UI are equal clients, so an agent Principal can propose an
adoption PR audited and cost-accounted like a human (§1.6). Adopting flips an object to Stratt-authored —
a meaningful grant, **OpenFGA-scoped** (a `may-adopt` relation within a Source/View scope), never
implicit. **No `adopt-and-run` affordance ever** (§5): adopt yields a declaration; the merged Workflow is
Gated like any other.

### Slice roadmap
1. **This ADR + core:** the per-object adopt function — resolve from the projection catalog, targeted
   read-only deep-read, per-object `awximport.Bundle` entrypoint, `adopted-from` lineage, fail-loud
   staleness; the API operation + a CLI client over it.
2. **Fast-follow:** the cutover Baseline (§4) — the dual-execution Finding; the MCP surface + OpenFGA
   `may-adopt` scope.

> **Credential handling ruling (2026-07-19, charter-guardian).** The AWX read token rides the
> `POST /adoptions` request transiently (never persisted/logged) — the **accepted resting point**
> (§2.5). Resolving a **CredentialRef IN-CORE** (the API server calling the SecretBroker) is a §2.5
> VIOLATION and is rejected: the guarantee is *structural* — no CredentialRef-material-resolution path
> may exist in the long-lived, multi-tenant control-plane process (unlike strattd's own operator-
> provisioned DB/OIDC/HMAC infra secrets; tenant CredentialRefs live behind `use-without-read`). The
> chartered-clean way to earn `use-without-read` ergonomics (so a caller/agent never custodies raw AWX
> material, §1.6) is **Option D**: run the deep-read + `awximport.Bundle` transform in a **first-party
> execution pod** with the CredentialRef injected at **pod spawn** (the canonical §2.5 path; the transform
> stays core code in a core-owned job image, so §1.4 does not bite). Cost: adopt becomes async for a rare
> operation. Option D is its own future ADR (adopt-as-a-job); until then the transient token stands.

## Supersession (must-fix 4 — partial, precise)
ADR-0025 becomes **Superseded-in-part by ADR-0086**:
- **Dies:** Decision #1 — the one-shot `stratt import awx` verb + its full-estate `awx.Enumerate` self-read.
- **Re-homes:** Decision #2's transform *input* — from an `awx.Enumerate` snapshot to a targeted
  per-object read; the mapping LOGIC (`awximport.Bundle`) is reused unchanged.
- **Survives (ADR-0025 stays the authority):** Decision #2 mapping logic, Decision #3 (SCM content-ref
  `ansible.input.v3` — an adopted job template still references its playbook by project+path), Decision #4
  (clone-in-EE).

## Charter alignment
- **§1.2:** adopt reads (catalog + targeted deep-read) and writes desired state to Git; it never writes
  the projection graph. Static-inventory hosts still never projected (ADR-0025 stance carries).
- **§1.6:** API-first capability; CLI/MCP/UI clients; OpenFGA-scoped `may-adopt`; agent-proposable.
- **§1.8:** the cutover is an explicit Finding; residual un-renderable fields are reported.
- **§5/§7.6:** reviewable declaration, human-merged, Gated; one-object-at-a-time strangler cutover.
- **§9:** the projection is not fattened to a universal AWX ontology to serve adopt.

## Naming note
`adopt` also exists internally as `adoptRehomedSource` (ADR-0044 cross-Cell Source rehoming — "take
ownership of a re-homed Source"). Different layer, non-overlapping context; both mean "take ownership."
The user-facing capability is `adopt <kind> <identity>` (foreign object → Named Kind); the rehoming
`adopt` is control-plane-internal. Documented here to prevent confusion.

## Alternatives considered
- **Keep `import` (rename only)** — rejected: paints a new word on the superseded one-shot model; the
  redundant full-estate self-read remains. The steward's insight is a model change, not a rename.
- **(a) Fatten the Syncer to a full AWX mirror** — rejected (§1.1/§9 ontology creep; heavier graph).
- **(c) Projection-only best-effort** — rejected as the default (knowingly lossy; strips signal).
- **CLI-only adopt** — rejected: §1.6 requires the capability be API-first so agents/CI/UI share one
  Principal/authz/audit/cost path; CLI is one client.
