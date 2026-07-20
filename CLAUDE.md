# Stratt — Claude Code Operating Guide

Stratt is an open (Apache-2.0) **estate-automation platform**: a typed estate **graph** plus a
durable **orchestration** engine, where every tool (Ansible, OpenTofu, Helm, MCP servers…) is a
plugin that consumes typed inputs from the graph and writes typed, provenance-stamped outputs back.

**Design authority: [stratt-charter.md](stratt-charter.md).** Read the relevant section before any
non-trivial decision. The charter supersedes every other document, including this one. If code or a
request contradicts the charter, surface the conflict — don't silently follow either.

**Status: Phases 0–2 code-complete; Phase 3 ~90%; multi-region Cells shipped ahead of plan; and the whole
platform re-centered onto the sovereign plugin port (dark-matter, ADR-0046 arc) — the core spine is
content-blind and every tool is a plugin, verified in-repo (live-cluster e2e still outstanding).** The Go
control plane (`core/`), the React UI (`ui/`), 60 ADRs, and the Helm chart are all real and substantial —
this is a working platform, not a spike. The living, evidence-backed tracker is
**[docs/roadmap.md](docs/roadmap.md)**; the decision record is **[docs/adr/](docs/adr/README.md)**. Follow
the charter §8 phasing and the roadmap — build the *next* thing, not ahead recklessly — and keep new work
behind an ADR (`/new-adr`) when it is a decision of consequence. **No phase's promote/OSS exit gate is met:**
every one ultimately waits on the charter **§7.4** going-public step (OSPO/IP clearance) plus operational
evidence (SLO, security review, adoption) — none of which is a coding task, and the repo stays **private**
until then.

## Founding Disciplines — binding on every decision (charter §1)
1. **Type the seams, not the world.** JSON Schema attaches only at plugin boundaries and named
   **Facets** — never to whole Entities, never as a universal ontology. Every Facet schema must be
   demanded by a shipping Contract.
2. **Projections, never a second truth.** External systems of record stay authoritative. The graph
   is a rebuildable read-model; **only Normalizers and Run provenance may write Entity attributes**,
   enforced in the data layer, not by convention. Desired state lives in Git; drift is the diff.
3. **Rug-pull-proof by structure.** Apache-2.0 everything, no gated tier ever. DCO, not CLA. Public
   repo / roadmap / ADRs / triage from the first tagged release.
4. **Boring spine, pluggable everything.** Core owns the spine (graph, orchestration, contracts,
   authz, audit); community owns breadth via plugin surfaces. Dependencies: few, boring,
   huge-community (Postgres, NATS, Temporal).
5. **Sovereign contracts, multiple transports.** Our connector contract is our own; REST/gRPC,
   subprocess, and MCP are transports beneath it. No external protocol is load-bearing for the
   deterministic core. All plugin schemas are pinned and hash-verified; schema drift is blocking.
6. **Agent-native, human-first.** Every capability is exposed identically to UI, CLI, CI, and AI
   agents (via MCP) under one Principal model, one authorization model, one audit stream, with
   cost/usage accounting per identity.
7. **Evergreen contract.** Every runtime/toolchain/substrate dependency stays ≥ N-1 on its
   major/LTS line, CI-gated. Upgrade-friendliness is a first-class selection criterion. Never become
   the monolith fossil AWX did.
8. **The abstraction must never hide diagnosis.** Hiding *mechanism* is the product; hiding *failure*
   kills trust. One-click descent — Intent → Blueprint route → Workflow → Run → task event — must
   always exist.

**Permanent non-goals** (charter §1): MDM protocol implementation (Intune/Jamf are Connectors);
OS imaging / bare-metal; new configuration languages; a writable CMDB; a paid tier. Never propose a
feature that violates these.

## Vocabulary is API — frozen at v1.0 (charter §2)
Use the **Named Kinds** exactly: Entity, Relation, Facet, Provenance, View · Source, Connector
(Syncer / Action / Emitter) · Actuator, Contract, Step, Workflow, Run, Trigger, Bundle, Site ·
Intent, Assignment, Blueprint, Baseline, Finding, Evidence · Principal, CredentialRef.

**BANNED in core-model identifiers** (each is a tool-specific rendering or a namespace collision):
`inventory`, `playbook`, `job template` / `job_template`, `CI`, `CMDB`, `resource`. Full reference
and AWX→Stratt migration mapping: invoke the **`/vocabulary`** skill.

## Tech stack (charter §3)
- **Control plane: Go.** Reconciliation controllers (sync controller, dispatcher, compiler cadences),
  graph-store frontend, K8s-native operator posture — client-go/controller-runtime plus Go-native
  SDKs for NATS / Temporal / OpenFGA. One language, shared with the pull agent. **API is
  OpenAPI-first** (huma / oapi-codegen). **Contracts & Facet schemas are data** — pinned,
  hash-verified JSON Schema, validated by a standard validator, **never language classes**.
- **Python lives only in execution pods** (the `ansible-runner` shim in the EE image) **and the
  plugin SDK** (one supported language for Connector/Actuator authors). Use `uv` there. Python is
  **not** the control plane.
- **Frontend:** React + TypeScript + Vite · TanStack Router/Query · vendored Radix/shadcn components
  owned in-repo · Tailwind (build-time only). Node current-or-previous LTS (this container: 24).
- **Agent / Sites:** Go (`stratt-agent` pull agent, NATS-leaf dispatcher) — shares types with the
  control plane.
- **Substrate:** Postgres 18 · NATS JetStream · Temporal · any **S3-compatible** object store
  (Garage / SeaweedFS / cloud — never MinIO-by-name) · Loki · OTel · OIDC (Zitadel) · OpenFGA ·
  cosign / SLSA / SBOM.
- **Ansible is subprocess-only** (GPLv3 boundary): the Go control plane never links it; it shells out
  to `ansible-runner` in the EE image. **OpenTofu over Terraform.**
- Task runner: `task` (Taskfile). Prefer it for repeatable commands once they are defined.

## Workflow
- **Explore → plan → implement → verify.** Use plan mode for multi-file or architecture-affecting
  work. The charter is the spec — check the result against it.
- **Verify every non-trivial change** with a real signal (test, build, or run) and show the evidence.
  Never assert success without it. (Add concrete test/build commands here once they exist.)
- **Charter review:** for changes to the data model, Contracts, vocabulary, authz, or a new
  dependency, delegate to the **`charter-guardian`** subagent to check against §1 and the non-goals
  before finalizing.
- **New dependency?** Run it past the **`dependency-scout`** subagent (evergreen §1.7: license, N-1
  support, upgrade track record) before adding it.
- **New core-model identifier?** Before merging a new Entity/Facet/Contract type name, API route, DB
  table/column, or CLI noun, run the **`vocabulary-linter`** subagent against it (charter §2 — naming
  is frozen v1.0 API).
- **Decisions of consequence** get an ADR under `docs/adr/` — run **`/new-adr`**.

## Repo etiquette
- Trunk-based; branch off `main`. Commit and push only when asked.
- **DCO sign-off is required on every commit:** `git commit -s`. No CLA, ever.
- Descriptive, conventional commit messages.
- **§7.4 blocker — highest-severity project risk:** the repo stays **private** until written employer
  OSPO/IP clearance is obtained. Until then do **not** create public-facing OSS files (SECURITY.md,
  CONTRIBUTING, a public README) and do **not** push to any public remote.
- **Never edit `LICENSE` or `stratt-charter.md` without explicit instruction.** The charter is the
  design authority; changes to §1/§2 require the highest review bar in the project.

<!-- Keep this file under ~200 lines. Path-specific guidance lives in .claude/rules/ (loads only when
     the matching files are touched); deep reference lives in skills (loads on demand). Prune ruthlessly. -->
