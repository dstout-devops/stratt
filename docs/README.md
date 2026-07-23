# Stratt documentation

Internal engineering docs. The **[charter](../stratt-charter.md)** is the design authority and supersedes
everything here; **[CLAUDE.md](../CLAUDE.md)** is the operating guide for working in this repo.

> **Charter §7.4 (OSPO/IP clearance) is cleared.** Public-facing OSS files may now exist —
> see **[../CONTRIBUTING.md](../CONTRIBUTING.md)**. The files here are still internal navigation, not a
> replacement for it.

## Map

**New here? Start with [overview.md](overview.md), then [architecture.md](architecture.md).** They
answer "what does this do?" and "how does it work?" in plain language, grounded in the charter.

| Area | What's here |
|---|---|
| **[overview.md](overview.md)** | **What Stratt is** — the thesis, the problem it solves, the three-planes mental model, the Named Kinds glossary, status in plain language. The front door. |
| **[architecture.md](architecture.md)** | **How it works** — the deployable shape, the three runtime loops (projection / orchestration / intent-drift), the sovereign plugin port, Cells/Sites, the repo layout, how to run it. |
| **[roadmap.md](roadmap.md)** | Phase status vs charter §8 — what's built, what's gated, what's deferred. The authoritative "where are we?" with evidence. |
| **[adr/](adr/README.md)** | Architecture Decision Records (~110, indexed). Every decision of consequence; run `/new-adr` to add one. |
| **[enterprise-readiness.md](enterprise-readiness.md)** | The hardening sibling of the roadmap — a living, evidence-backed inventory of the gaps between what Stratt *claims* and what it *enforces in the shipped artifact*. |
| **[runbooks/](runbooks/)** | Operational procedures: [ha-dr.md](runbooks/ha-dr.md) (in-region HA + DR), [cell-failover-drill.md](runbooks/cell-failover-drill.md) (multi-region Cell failover + fenced Source re-home), [genesis-to-authz.md](runbooks/genesis-to-authz.md) (promoting a minimal genesis deployment to real, server-backed authz). |
| **[evidence/](evidence/multi-region-99_99.md)** | Requirement→evidence maps that back an availability/compliance claim (e.g. the 99.99% multi-region path). |
| **[ux/](ux/)** | Design system + product UX: [design-tokens.md](ux/design-tokens.md), [screen-catalog.md](ux/screen-catalog.md), [competitive-teardown.md](ux/competitive-teardown.md). |
| **[mcp-servers.md](mcp-servers.md)** | MCP server surface (the agent-native control plane, §1.6). |
| **[oss-connector-tool-landscape.md](oss-connector-tool-landscape.md)** | Candidate Apache-2.0-compatible OSS infrastructure tools surveyed for the unified Connector/Actuator plugin surface. |
| **[aap-2.7-parity.md](aap-2.7-parity.md)** | Feature-parity tracker against Ansible Automation Platform 2.7 — the floor the "structurally-open successor" thesis must clear. |

## Conventions

- **ADRs are immutable once Accepted** — supersede with a new ADR, never rewrite. Charter §1/§2-touching
  ADRs carry a charter-guardian review in their Reviews section.
- **Vocabulary is frozen** (charter §2) — use the Named Kinds; the `/vocabulary` skill is the reference.
- **DCO sign-off on every commit** (`git commit -s`); no CLA.
