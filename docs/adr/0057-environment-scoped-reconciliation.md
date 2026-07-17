# ADR 0057 — Environment-scoped reconciliation: one estate repo, N environments

- **Status:** Accepted
- **Date:** 2026-07-17
- **Deciders:** steward (dstout), charter-guardian
- **Charter sections:** §1.2, §2.4, §4.1, §4.3
- **Frames under:** [ADR-0055](0055-estate-composition.md) (Estate Composition)

## Context

[ADR-0056](0056-estate-as-code.md) consolidated the estate into one reconciled Git tree (`estate/`). But a
single reference estate must serve **multiple environments** (dev / staging / prod), and those are *not*
identical — a dev cell runs a subset of connectors and must not fire prod's schedules. The design already
anticipated this: `types.Assignment` carries an `environments []string` field (`desiredstate.go:1234`,
parsed and stored). **It is never enforced** — `core/internal/compiler/compiler.go` does not read it and
strattd has no active-environment concept. It is a **half-built seam**.

Reconciling the full `estate/` in the dev turnkey stack exposes the gap immediately: the `cert-reconcile`
schedule Trigger (every 6h) and the salt/chef/puppet declarations would reconcile and fire against plugins the
dev cell does not run — noise, failed Runs, and conceptually wrong (the reference estate ≠ a cell's active
estate). This ADR finishes the seam: an **active environment + a reconcile-boundary filter** so one estate repo
reconciles the correct slice per environment (the terraform-workspace / AWS-CDK-stage model ADR-0055 references).

## Decision

**1. strattd carries an active environment: `STRATT_ENVIRONMENT` (default empty = UNSCOPED).** An unscoped
daemon (env unset) applies **every** declaration regardless of tags — byte-identical to today (backward
compatible). A scoped daemon (`STRATT_ENVIRONMENT=dev`) applies only in-scope declarations (rule 2).

**2. A declaration is IN SCOPE iff its `environments` is empty OR contains the active environment.** Empty
`environments` = **all environments** (the default; most declarations are universal — you tag only the
env-specific ones). This is a **selector, never a precedence field** (§2.4): membership, not priority — no
last-writer-wins, no ordering. A declaration tagged for another environment is simply **not this daemon's**.

**3. Scope BOTH the apply-set and the prune-candidate set — in the data layer, not by convention.**
`desiredstate.ParseDir` stays a pure, env-agnostic parser. The reconcile `Controller` (which holds the active
env) filters the parsed `Declarations` to the in-scope slice before `ComputePlan`/`Apply`/`compile`. **But the
filter alone is a data-loss footgun** (guardian): today the prune emits `ActionDelete` for any cac row in the DB
but absent from the parsed decls (`desiredstate.go:2354`). Simply dropping out-of-scope declarations would make
*every other environment's rows* prune candidates — two scoped daemons on **one** Postgres would mutually wipe
each other's estate, and `MaxPruneFraction` caps the rate, not the outcome. "Each environment has its own
substrate" is a **convention**, which §1.2 forbids ("enforced in the data layer, not by convention").

So the scope is persisted and enforced structurally: **each cac row carries the declaration's `environments`
set** (a new column on the scopable kinds' tables; empty = all-environments). The prune **candidate set** is
restricted to rows *in scope for the active environment* (`environments` empty OR containing the active env);
a row tagged only for another environment is **invisible** to this daemon's prune — never a target. On a shared
DB, a dev daemon and a prod daemon co-manage the untagged (universal) rows and each prune only their own tagged
rows; neither can delete the other's estate. Re-tagging a declaration `dev → prod` drops it from dev's in-scope
set and it becomes a normal desired-state prune in dev (gated by the desired-state `MaxPruneFraction` guard,
`controller.go:101`, surfacing an orphan Finding via the compiler's withdrawn-Assignment path) and appears in
prod. An **unscoped daemon** (`STRATT_ENVIRONMENT` empty) treats every row as in scope — byte-identical to today.

## Environment vs. Cell — orthogonal axes (do not conflate)

Cell (ADR-0044) and environment are **different axes and both are needed**:
- **Cell** = a *physical* partition: a region-local, single-writer control-plane shard with its own substrate;
  residency/home is `home_cell` on the datum. It answers *"which shard physically owns and writes this."*
- **Environment** = a *logical* slice of desired state (dev / staging / prod / ring) **within** a Cell. It
  answers *"which policy tier does this declaration belong to."*

A single Cell (one DB) legitimately holds **more than one** environment's cac rows (a dev Cell may serve dev +
staging); conversely one environment (prod) may span **multiple** Cells (prod-us-east + prod-us-west). Because a
Cell's one DB holds multiple environments, **decision 3's data-layer environment stamp on each row is mandatory**
— environment scoping can NOT lean on "own substrate," or it collapses into Cell and becomes a redundant second
residency axis (§1.1 ontology-sprawl). Environment does not touch `home_cell`; the two compose.

**4. The scopable kinds, via a uniform `EnvScoped` seam.** A small interface —
`EnvScoped { ScopedEnvironments() []string }` **and nothing more** — is implemented by every declaration type
that carries `environments`; the Controller's filter is generic over it, so adding a new scopable kind is just
adding the field + column. The interface is a **boolean membership filter only**: it must never grow into a
router or a source of env-conditional *values* (`EnvironmentValues()` is forbidden — per-environment values are
a Blueprint per-capability concern (§2.4), and env-conditional config values would be a new-config-language
non-goal). The first cut carries it on the **launching/active** kinds that cause cross-environment effects:
**Assignment** (already has the field — now enforced), **Trigger**, and **Baseline**. Views/Workflows are
**not** independently scoped in v1 — and that is safe *only* because every declarative launch path passes
through a scoped kind (a Trigger fires a Workflow; a Baseline refs a remediation Workflow; an Assignment
compiles to a Baseline). **Invariant (write it down):** any future declarative kind that can *independently
launch a Run at the reconcile boundary* MUST implement `EnvScoped`, or it is an environment hole. **Sources**
(ADR-0056) and Emitters/NotifySinks gain the field as they land (a dev cell obviously runs `vcenter-dev`).

**5. Environment scope is a declarative-reconcile filter — NOT an execution/isolation boundary (§1.8).** It
governs *which declarations a daemon reconciles*, nothing more. Manual, API, MCP, and `stratt` CLI launches (all
first-class §2.4 launch paths) **bypass it entirely** — a `dev`-tagged Trigger's Workflow can still be run by
hand against prod Entities. The execution isolation boundary remains **OpenFGA View-scoped execution**
(§2.5/ADR-0028). `environments` must never be documented or surfaced as a safety guarantee it does not provide
(§1.8 — the abstraction must not imply a protection that isn't there).

## Charter alignment

- **§2.4 no implicit precedence.** `environments` is a **membership selector**, not a priority/last-writer field
  — the anti-GPO line holds. It is the logical-environment analogue of ADR-0044's Cell partitioning (which
  physically partitions a single writer); here the partition is a declared environment tag, filtered at reconcile.
- **§1.2 desired state in Git.** The filter only scopes *which slice this daemon reconciles*; the full desired
  state stays in one Git tree, and drift is still the diff — per environment.
- **§4.1 / §4.3.** No inheritance/precedence introduced. A mass re-tag whose resulting prune exceeds the fraction
  is refused unattended by the **desired-state** `MaxPruneFraction` guard (`controller.go:101`) — NOT the
  compiler's per-Assignment max-delta gate (a distinct mechanism); the guardrail that stops a silent estate wipe
  is the reconcile prune guard.
- **§2 vocabulary.** "environment" is a scoping **attribute**, not a Named Kind — no vocabulary change, no banned
  term (`vocabulary-linter` confirms no Named-Kind collision). One naming caveat: §4.1 already uses "environment
  overlay directories" for Kustomize-style *file layering* that produces the declaration set (a build-time
  concern); this ADR's `environments` **tag** is a *runtime* membership filter over the produced set. Two
  distinct mechanisms — keep them distinct in docs and identifiers.

## Consequences

- **Positive:** one estate repo serves all environments; a dev cell reconciles its slice with no cross-env noise;
  the latent `Assignment.environments` field is finished and made meaningful; the model matches CDK stages /
  terraform workspaces. Unblocks Layer 2 (turnkey stack reconciles `estate/` with `STRATT_ENVIRONMENT=dev`).
- **Negative / trade-offs:** a new cross-cutting runtime concept (the active environment) threaded into the
  reconcile; three types gain a field + parse/validate; the filter must be applied consistently (compiler reads
  the pre-filtered slice, so filtering happens once, upstream of it).
- **Follow-ups:** extend `EnvScoped` to Sources (ADR-0056) and Emitters/NotifySinks; surface the active
  environment on `GET /cellinfo` and in `stratt plan` (so a plan states which environment it targets); a
  Helm value `environment:` wiring `STRATT_ENVIRONMENT`.

## Alternatives considered

- **Separate estate directory per environment** (`estate-dev/`, `estate-prod/`) — rejected: defeats "one estate"
  (ADR-0056), and duplicated trees drift. `environments` tags on one tree are the single-source model.
- **Leave `environments` inert; dev reconciles a curated subset** — rejected: the field rots as documented-but-
  false, and you cannot honestly have "one estate, N environments" without enforcing the scope.
- **Filter inside `ParseDir`** — rejected: couples the pure parser to runtime env; the reconcile boundary is the
  right place (ParseDir is reused by the offline `stratt plan`, which must stay env-agnostic unless asked).
- **A precedence/priority ordering across environments** — rejected outright (§2.4): environments partition,
  they never override each other.

## Reviews

- **charter-guardian (2026-07-17): SOUND-WITH-CHANGES** (anti-GPO reasoning was found airtight and unchanged).
  **Must-fixes (folded):** (1) the original "safe because each environment has its own substrate" was an
  *unenforced convention* (§1.2 violation) — two scoped daemons on one Postgres would mutually prune each
  other's estate; replaced with **data-layer scoping** (persist each cac row's `environments`, restrict the
  prune candidate set to in-scope rows so out-of-scope rows are invisible to prune, decision 3). (2) the ADR
  **conflated environment with Cell** ("a dev cell's DB") — added the "Environment vs. Cell" section fixing them
  as **orthogonal** (Cell = physical residency/`home_cell`; environment = logical slice within a Cell), which
  makes the data-layer stamp mandatory rather than redundant with `home_cell`. (3) corrected the §4.3 citation
  (the prune is gated by the desired-state `MaxPruneFraction`, not the compiler max-delta). **Flags (folded):**
  `EnvScoped` stays a boolean filter only (no `EnvironmentValues()` router, decision 4); the "any independently
  launch-capable declarative kind MUST implement `EnvScoped`" invariant is written down (decision 4); and
  environment scope is explicitly **a reconcile filter, not an execution/isolation boundary** — OpenFGA
  View-scoped execution remains the isolation boundary (decision 5, §1.8).
