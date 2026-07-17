# Stratt documentation

Internal engineering docs. The **[charter](../stratt-charter.md)** is the design authority and supersedes
everything here; **[CLAUDE.md](../CLAUDE.md)** is the operating guide for working in this repo.

> **Private repo (charter §7.4).** Until written OSPO/IP clearance lands, this repo stays private and we do
> **not** add public-facing OSS files (a root README, SECURITY.md, CONTRIBUTING). The files here are internal
> navigation, not the public front door.

## Map

| Area | What's here |
|---|---|
| **[roadmap.md](roadmap.md)** | Phase status vs charter §8 — what's built, what's gated, what's deferred. Start here for "where are we?" |
| **[adr/](adr/README.md)** | Architecture Decision Records (54, indexed). Every decision of consequence; run `/new-adr` to add one. |
| **[runbooks/](runbooks/)** | Operational procedures: [ha-dr.md](runbooks/ha-dr.md) (in-region HA + DR), [cell-failover-drill.md](runbooks/cell-failover-drill.md) (multi-region Cell failover + fenced Source re-home). |
| **[evidence/](evidence/multi-region-99_99.md)** | Requirement→evidence maps that back an availability/compliance claim (e.g. the 99.99% multi-region path). |
| **[ux/](ux/)** | Design system + product UX: [design-tokens.md](ux/design-tokens.md), [screen-catalog.md](ux/screen-catalog.md), [competitive-teardown.md](ux/competitive-teardown.md). |
| **[mcp-servers.md](mcp-servers.md)** | MCP server surface (the agent-native control plane, §1.6). |

## Conventions

- **ADRs are immutable once Accepted** — supersede with a new ADR, never rewrite. Charter §1/§2-touching
  ADRs carry a charter-guardian review in their Reviews section.
- **Vocabulary is frozen** (charter §2) — use the Named Kinds; the `/vocabulary` skill is the reference.
- **DCO sign-off on every commit** (`git commit -s`); no CLA.
