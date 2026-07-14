# ADR 0033 — CIS pack: compliance frameworks as data over a reusable projection

- **Status:** Accepted (Commit 1 — project-then-observe enablement; Commit 2 —
  the CIS pack + compliance score)
- **Date:** 2026-07-13
- **Deciders:** Project steward (dstout)
- **Charter sections:** §2.4 (Baseline / Finding / Evidence; "one kind,
  framework-tagged"), §1.2 (projections, never a second truth; "desired state
  lives in Git; drift is the diff"), §1.1 (type the seams — a Facet schema
  exists only when a shipping Contract demands it), §1.5 (pinned / hash-verified
  supply chain), §2 (frozen vocabulary — "pack" is NOT a Named Kind), §1.4
  (boring spine, no parallel stack), §1.8 (never hide failure); ADR-0019
  (Baselines / check Steps), ADR-0023 (compiler-emitted facet-observation
  Baselines — the machinery this reuses for hand-written ones), ADR-0032 (the
  Bundle / cosign rails the next layer rides)

## Context

Phase 3 is "Enterprise + fleet." The compliance **spine already shipped and is
load-bearing**: `Baseline` (with a `Framework` field commented `e.g. "cis"`),
`Finding` (flap-damped, §4.3), `Evidence` (WORM S3, tamper-evident), the
Intent/Assignment/Blueprint compiler, and the read API. A "CIS pack" is therefore
a **content + score** exercise, not a platform build — no new tables, types, or
orchestration.

Two forks shaped the slice. **How is a control checked?** Steward chose
**project-then-observe** over probe-and-report: a collector projects
`os.hardening.*` Facets once, and each CIS control is a declarative
`{namespace, path, equals}` assertion (a facet-observation Baseline). **Where does
the content live?** There was no in-tree precedent for shipping opinionated tool
content (`contracts/` — schemas-as-data — was the only embedded-content pattern).
Steward chose **in-tree, hash-pinned now; OCI-cosign-signed distribution as the
documented next layer.**

The reframe project-then-observe creates is load-bearing: **the benchmark is
DATA, not code.** The only playbook is the collector gather; every control is a
declarative assertion. Build the projection once and CIS/STIG/PCI-host all become
pure Baseline authoring — no per-framework playbook silo (the AWX content-silo
trap avoided).

## Decision (Commit 1 — project-then-observe enablement)

1. **`os.hardening.*` Facet schemas are the typed seams (§1.1).**
   `contracts/facets/os.hardening.{sysctl,sshd,filesystem,auditd,services}` —
   dot-free keys (the Facet path evaluator splits on `.`), `additionalProperties:
   false`, auto-embedded + auto-pinned. Each is demanded by a shipping CIS
   Baseline in Commit 2 (else it must not exist, §1.1).

2. **The collector is a cadenced gather Run, NOT a new Syncer (§1.2, §1.4).**
   Projection-via-Run already exists (`os.kernel`), is §1.2-legal (`WriterRun`
   provenance through the constrained `Projector`), and a host-gather Syncer that
   dispatched ansible would be a *parallel execution stack* (anti-§1.4). The four
   existing Syncers are external-API Sources; hosts are reached by a Run.
   `ExtractFacts` now projects `os.hardening.*` from the collector's
   `stratt_hardening_<domain>` set_facts; the play owns normalization so the
   projected document matches the pinned schema, else the Projector write is
   refused. The dispatch fact-merge became **additive per namespace** — a gather
   play emits `os.kernel` and `os.hardening.*` from separate tasks, and a later
   event must not erase an earlier namespace.

3. **Hand-written facet-observation Baselines are enabled (ADR-0023 reuse).** The
   runtime already dispatched `Mode==facet-observation` regardless of
   `CompiledFrom`; only CaC parse/validate blocked it. `desiredstate` now parses
   `mode` + `expected` (no actuator/params/check Step) and validates it;
   `EvaluateFacetBaseline` resolves `viewName` when there is no compiled
   selector. An operator Baseline now evaluates projected Facets into Findings.
   There is deliberately **no `claim` field** on the facet-observation variant
   (charter-guardian flag): an observation reads, it never writes/owns the
   Facet, so there is nothing to claim — the anti-GPO `claim` concept belongs to
   the compiler over Assignment-owned writes (§2.4). Two Baselines asserting
   contradictory expectations each independently open a Finding; no silent
   precedence.

## Decision (Commit 2 — the CIS pack + compliance score)

4. **A "pack" is a curated grouping of existing Kinds — NOT a Named Kind (§2).**
   `Pack` never appears as a core-model identifier / table / API noun. The `packs/`
   module mirrors `contracts/`: hash-pinned DATA at the repo root; the load /
   content-hash / materialize logic lives in `core/internal/packs`. The CIS pack
   ships a manifest, one collector Trigger (with the inline read-only gather
   play), and 23 facet-observation Baselines across all five domains
   (framework: cis).

5. **`stratt pack list|show|install` materializes into the operator's Git (§1.2).**
   `install cis --view <hosts> -o <cac-dir>` substitutes the pack's `${VIEW}` /
   `${PRINCIPAL}` / `${CRED}` placeholders and writes the Baselines + collector
   Trigger into the operator's desired-state directory, which they commit —
   desired state stays operator-owned. Facet schemas ship compiled-in. A left-over
   placeholder is a hard error (never emit invalid CaC, §1.8).

6. **`GET /compliance/{framework}` is the posture score (OpenAPI-first).** It
   folds the framework-tagged Baselines (the controls) against their open Findings
   into a per-View `{controls, passing, failing, score, failingControls}`. A
   control passes when no target in its View has an open Finding. Read-only over
   the existing surface; one grouped SQL query for the open-Finding tally.

## Charter posture

- **§1.1** every `os.hardening.*` schema is demanded by a shipping CIS Baseline,
  pinned/hash-verified in `contracts/`.
- **§1.2** the benchmark is desired-state DATA in Git; the collector projects
  Facets with `WriterRun` provenance through the constrained `Projector`; drift is
  the diff. The collector is a Run, not a parallel stack (§1.4).
- **§2 vocabulary** no `Pack` identifier; the pack is Contracts + a gather Trigger
  + Baselines. No banned terms (`inventory`/`resource`).
- **§1.5** the pack is content-hash-pinned in-tree; OCI-cosign-signed distribution
  is the documented next layer on the Bundle rails.
- **§2.5** the collector gather is read-only, empty `Env`, RemoteSafe — it could
  later ship as a signed Bundle to a pull-Site.

## Alternatives considered

- **Probe-and-report (ansible check-mode Baselines).** Simpler (no collector, no
  new Facets), but each framework becomes a playbook silo and the graph learns
  nothing reusable. Rejected for the project-then-observe generalization.
- **Facet-observation collector as a Syncer.** A host Syncer that dispatched
  ansible would duplicate the execution stack (§1.4). Rejected — projection is a
  Run side-effect.
- **`Pack` as a new Named Kind / OCI-signed distribution now.** Would mint a
  vocabulary term and build the community-content-hub install flow in the same
  slice. Deferred — the in-tree hash-pinned pack proves the shape first.
- **SCM-ref an upstream CIS role.** A runtime git dependency, not hash-pinned
  (§1.5), GPLv3 content, upstream owns semantics. Rejected for v1.

## Honest deferrals

Broad coverage is additive — the collector front-loads projection, and v1 covers a
broad-but-curated 23-control set across all five domains, extensible
control-by-control as pure data. Remediation Workflows stay refs-only, gated
(§5 Flow 2). Deferred: OCI-cosign-signed distributable packs + trust tiers +
`stratt pack push` (the community content hub, on the ADR-0032 rails); STIG/PCI
packs (pure Baseline authoring on the same projection — the payoff); the collector
gather play's probe details are the operator-tunable part and are structurally
valid but not yet e2e-run against a live host this slice; array-indexed
FacetExpectation paths (the evaluator walks object keys only).

## Consequences

The typed graph gains a reusable host-hardening projection; compliance frameworks
become authored data over it, with a posture score. The `packs/` module
establishes the in-tree content-pack precedent (hash-pinned, evergreen-gated),
cleanly upgradeable to signed OCI distribution without disturbing the operator's
Git-owned desired state.
