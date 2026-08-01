# Stratt

**One front door for infrastructure.**

An estate-automation platform: a typed estate **graph** plus a durable
**orchestration** engine, where every tool — Ansible, OpenTofu, Helm, MCP
servers — is a plugin that reads typed inputs from the graph and writes typed,
provenance-stamped outputs back.

## The problem

Infrastructure management is spread across a dozen consoles and CLIs, each with
its own access model, vocabulary, and failure modes. Knowing the state of an
estate means asking several tools and reconciling their answers by hand, and
changing it means driving each one separately.

Stratt puts a single typed graph in front of all of them. The core spine is
content-blind: it does not know what Ansible or Helm *are*. Tools plug into a
sovereign port, so adding one is a plugin, not a fork of the platform.

## How it is built

| Piece | What it is |
|---|---|
| `core/` | Go control plane — the graph, the orchestration engine, the plugin port |
| `contracts/` | Hash-pinned JSON Schema contracts shared across every module |
| `ui/` | React front end |
| `estate/`, `ee/` | Estate modelling and enterprise-facing modules |
| `deploy/` | Helm chart |
| `demos/` | Turnkey demos, proven end-to-end on kind |
| `docs/adr/` | 90+ architecture decision records |

Go multi-module workspace (`go.work`), gRPC and Protocol Buffers between
services, PostgreSQL with PLpgSQL functions and versioned migrations,
OpenTelemetry and Prometheus for telemetry, Rego for policy, and Terraform for
the surrounding infrastructure.

## Design discipline

Development is governed by a written charter that supersedes every other
document in the repository, including the contributor guide. Decisions of
consequence are recorded as ADRs before they are implemented. Where code and
charter disagree, the conflict is surfaced rather than quietly resolved.

## Status

Actively developed and substantial rather than a spike. At the time of writing
the repository carries **144 architecture decision records**, **28 Go modules**
in one workspace, four Helm charts, and roughly 1,750 files. A turnkey demo
under `demos/` runs end-to-end on kind.

Phases 0–2 are code-complete and Phase 3 is well progressed. Each phase carries
its own promotion gate — SLO evidence, security review, adoption — which are
deliberately not coding tasks.

`docs/roadmap.md` is the living, evidence-backed tracker. Treat it, not this
file, as the current state.

## Licence

Apache-2.0.
