# ADR 0001 — Charter as design authority; Claude Code as the initial build surface

- **Status:** Accepted
- **Date:** 2026-07-10
- **Deciders:** Project steward (dstout)
- **Charter sections:** §0, §1, §2, §7.4, §8

## Context

Stratt will be written **exclusively by Claude Code** at the start (2–4 engineers, heavily
AI-assisted — charter §8). Before any product code exists, the environment must make the charter's
disciplines operative rather than aspirational. `stratt-charter.md` is explicitly the design
authority and supersedes all prior drafts (§0). The category's graveyard is governance failure and
ontology creep (§0, §9); the countermeasure is structural, not cultural.

## Decision

1. **`stratt-charter.md` is the single design authority.** Every non-trivial decision is checked
   against it; when code or a request contradicts it, the conflict is surfaced, not silently
   resolved. It is not edited without explicit instruction; §1/§2 changes carry the highest review
   bar.
2. **Encode the charter into the Claude Code control plane** (this repo's `.claude/` + `CLAUDE.md`):
   - `CLAUDE.md` — always-loaded spine: the 8 Founding Disciplines, non-goals, frozen vocabulary
     banned-terms, stack, workflow, and the §7.4 public-push blocker.
   - `.claude/rules/*` — path-scoped guidance (backend, frontend, graph/data-layer, contracts,
     infra/supply-chain) that loads only when matching files are touched.
   - `.claude/agents/*` — `charter-guardian` (discipline/non-goal review), `vocabulary-linter`
     (frozen-naming enforcement), `dependency-scout` (evergreen §1.7 evaluation).
   - `.claude/skills/*` — `vocabulary` (Named Kinds reference), `new-adr` (this workflow).
   - `.claude/settings.json` + `.claude/hooks/*` — guardrail permissions and hard PreToolUse blocks
     on protected paths and destructive commands.
   - `.mcp.json` — Context7 (live library docs, serving the evergreen discipline) and Playwright;
     Postgres/Temporal deferred to `docs/mcp-servers.md` until Phase-0 substrate exists.
3. **Do not scaffold product code** until the environment is configured and Phase-0 is authorized
   (§8), and **do not create public-facing OSS files or push publicly** until §7.4 OSPO/IP clearance
   is obtained.

## Charter alignment

Serves §1.3 (public ADRs/governance), §1.7 (evergreen tooling via Context7 + dependency-scout),
§1.8 (diagnosis-first workflow), §2 (frozen vocabulary enforcement), and §7.4 (clearance blocker
encoded as a hard rule). In tension with nothing; this ADR only makes existing disciplines operative.

## Consequences

- **Positive:** the disciplines become part of every session's context and are checkable by
  fresh-context subagents; naming/ontology drift is caught early and cheaply.
- **Negative / trade-offs:** always-loaded context (`CLAUDE.md` + unscoped rules) costs tokens —
  mitigated by path-scoping most rules and pushing deep reference into on-demand skills. The control
  plane must be pruned as the project grows (best-practices warns bloated CLAUDE.md reduces
  adherence).
- **Follow-ups:** add real test/build/lint commands to `CLAUDE.md` and a Stop/PostToolUse
  verification gate once a toolchain exists; wire the CI evergreen gate (§1.7) at Phase 1; revisit
  MCP servers when Phase-0 substrate is stood up.

## Alternatives considered

- **Rely on the charter alone, no `.claude/` encoding** — rejected: charter is advisory context;
  without rules/agents/hooks the disciplines are not consistently applied or enforceable.
- **Scaffold the monorepo now** — rejected: violates §0/§8 (environment first, Phase-0 go/no-go gate)
  and risks premature structure that the spike may invalidate.
