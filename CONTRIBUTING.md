# Contributing to Stratt

Stratt is an Apache-2.0 estate-automation platform: a typed graph plus a durable orchestration
engine, where every tool (Ansible, OpenTofu, Helm, MCP servers…) is a plugin. The
**[charter](stratt-charter.md)** is the design authority for everything here — if something in this
guide and the charter disagree, the charter wins.

## Ground rules

- **License:** Apache-2.0. **No CLA** — instead every commit needs a **DCO sign-off**:
  `git commit -s`. CI rejects PRs with unsigned commits.
- **Trunk-based:** branch off `main`, PR back into `main`.
- **Vocabulary is frozen** (charter §2). Use the Named Kinds (Entity, Relation, Facet, Contract,
  Workflow, Run, …) and avoid tool-specific terms as core-model identifiers — `inventory`,
  `playbook`, `job template`, `CMDB`, `resource` are banned. See the vocabulary reference in
  [`docs/README.md`](docs/README.md) if you're touching naming.
- **Permanent non-goals:** no MDM protocol implementation, no OS imaging/bare-metal, no new
  configuration language, no writable CMDB, no paid tier. PRs pushing into these get closed.

## Getting oriented

Start with **[docs/README.md](docs/README.md)** — it maps every doc in the repo. The short version:

| Doc | What it answers |
|---|---|
| [docs/overview.md](docs/overview.md) | What is Stratt, and why? |
| [docs/architecture.md](docs/architecture.md) | How does it work? |
| [docs/roadmap.md](docs/roadmap.md) | What's built, what's next, what's gated? |
| [docs/adr/](docs/adr/README.md) | Why was it built this way? (Architecture Decision Records) |

## Dev environment

Toolchain floors (evergreen contract, charter §1.7 — CI fails below these):

- Go **1.24+**, Node **22+** (current LTS is 24), Python **3.13+** (execution pods / plugin SDK
  only — Python is never the control plane), [Task](https://taskfile.dev) **3.0+**.

```sh
task setup:devcontainer   # if you're not already in the provided devcontainer
task dev:up               # Postgres 18 + NATS JetStream + Temporal dev substrate
```

## Before opening a PR

Run the same gate CI runs:

```sh
task ci                   # evergreen + fmt:check + lint + codegen freshness + tests
```

Faster inner loop while iterating:

```sh
task test                 # all Go workspace modules
task lint
task fmt                  # or `task fmt:check` to just verify
```

Touched the UI (`ui/`)? Run `task ui:ci`, or `npm run typecheck`/`lint`/`test`/`build` directly.
Touched an OpenAPI spec or `.proto` file? Run `task generate:check` / `task proto:check` to confirm
the committed codegen is still fresh — CI will fail on drift.

Commit messages: descriptive, [Conventional Commits](https://www.conventionalcommits.org/) style
(`feat:`, `fix:`, `docs:`, `chore:`, …).

## Design decisions

Anything non-trivial — a change to the data model, Contracts/Facets, vocabulary, authorization, or
a new dependency — gets an **ADR** under `docs/adr/`. Skim [docs/adr/README.md](docs/adr/README.md)
and [docs/adr/MAP.md](docs/adr/MAP.md) first: the corpus is 100+ decisions deep and something
similar has often already shipped. If you're using Claude Code, `/new-adr` walks this for you
(prior-art scan included); otherwise copy [docs/adr/0000-template.md](docs/adr/0000-template.md).

New third-party dependency? It needs to clear the evergreen bar (charter §1.7): boring, huge
community, ≥N-1 supported, license-compatible.

## Questions

Open an issue, or check [docs/roadmap.md](docs/roadmap.md) for current priorities and known gaps.
