# ADR 0005 — Phase-0 monorepo layout: multi-module Go workspace

- **Status:** Accepted
- **Date:** 2026-07-11
- **Deciders:** Project steward (dstout)
- **Charter sections:** §2, §3, §8
- **Fulfils:** the ADR-0002 follow-up "control-plane scaffolding (Phase 0) is Go modules."

## Context

Phase 0 is authorized (charter §8); the repo needs its first product-code layout. ADR-0002
fixed Go for the control plane and `stratt-agent`, and `.claude/rules/backend-go.md`
requires shared wire/domain types in a common module rather than duplication. The steward
chose a **multi-module workspace** over a single root module: the agent (Phase 3) must be
able to version and release independently of the control plane, and the shared-types
surface should be a deliberate, minimal boundary from day one rather than an extraction
later.

## Decision

1. **Go workspace (`go.work`) at the repo root** tying together independent modules:
   - **`types/`** — `github.com/dstout-devops/stratt/types`: shared wire/domain types for
     the Named Kinds (§2). Minimal, dependency-free; the module `stratt-agent` will share
     with the control plane. Contracts/Facet schemas are **not** modeled here — they are
     data (§1.5).
   - **`core/`** — `github.com/dstout-devops/stratt/core`: the control plane (§3) —
     graph-store frontend, sync controller, dispatcher, compiler cadences, OpenAPI-first
     API. Binaries under `core/cmd/` (`strattd`); packages under `core/internal/` until a
     surface is deliberately exported.
   - **`agent/`** — reserved for `stratt-agent` (Phase 3); not created until then.
2. **Non-Go top-level directories:**
   - **`contracts/`** — pinned JSON Schema documents (Contracts and Facet schemas), data
     not code (§1.5); `contracts/facets/` for Facet namespaces demanded by shipping
     Contracts (§1.1).
   - **`deploy/dev/`** — the Phase-0 dev substrate (Postgres 18, NATS JetStream, Temporal,
     vcsim) as docker-compose; production packaging (Helm) is Phase 1.
   - **`ee/`** — the execution-environment image (ansible-runner shim; the only in-repo
     Python besides the future plugin SDK, per ADR-0002) — created when the dispatcher
     lands.
3. **Vocabulary applies to layout:** package and directory names use the Named Kinds and
   never the banned terms (§2).

## Charter alignment

- **§1.4 boring spine:** standard Go workspace tooling, no build framework.
- **§1.7 evergreen:** each module pins its own `go` directive at N-1 (1.25) so both
  current and previous toolchains build it.
- **§2 vocabulary:** enforced in identifiers from the first package.
- **§8 sequencing:** layout exists to serve the Phase-0 spike, nothing speculative beyond
  the reserved `agent/` name. No Founding Discipline or non-goal is touched.

## Consequences

- **Positive:** the agent/control-plane split is structural from day one; the shared-types
  boundary is explicit and reviewable; modules version independently at release time.
- **Negative / trade-offs:** multi-module choreography (replace/require dances at tagging
  time; `go.work` confined to local dev) is real overhead a single module would not have —
  accepted by explicit steward choice.
- **Follow-ups:** add `agent/` module at Phase 3; wire per-module CI (build/test fan-out
  over workspace members); revisit `core/internal` boundaries when the plugin SDK (Python,
  ADR-0002) needs a stable Contract surface.

## Alternatives considered

- **Single root module** — rejected by steward decision: retrofitting module boundaries
  when the agent needs independent releases is costlier than carrying the workspace now.
- **Separate repos per component** — rejected: pre-1.0 the spine changes atomically;
  polyrepo choreography contradicts the trunk-based §8 cadence and burdens a 2–4-person
  team.
