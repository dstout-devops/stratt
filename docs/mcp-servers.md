# MCP servers

Project-scoped MCP servers live in [`.mcp.json`](../.mcp.json) at the repo root and are checked into
git so the whole team shares them. The first time you open the project, Claude Code prompts you to
approve the project servers (`/mcp` to review; they show as `⏸ Pending approval` until you trust the
workspace).

## Active now

| Server | Transport | Why | Notes |
|---|---|---|---|
| **context7** | http | Live, version-pinned library docs at query time. Directly serves the **Evergreen contract** (charter §1.7) and keeps the `dependency-scout` subagent accurate. | Works keyless (rate-limited). For higher limits, get a key at context7.com and add `"headers": { "Authorization": "Bearer ${CONTEXT7_API_KEY}" }` to the server entry, then export `CONTEXT7_API_KEY`. |
| **playwright** | stdio (`npx @playwright/mcp`) | Drives a real browser so Claude can visually verify the React UI (charter §3.1) once it exists. | First real use may need `npx playwright install chromium`. |

GitHub is intentionally **not** an MCP server — per Claude Code best practices the `gh` CLI is the
most context-efficient path, and it's installed in the devcontainer. Use `gh` for PRs/issues/releases
(and remember `git commit -s` for DCO, charter §1.3).

## Deferred until Phase-0 substrate exists (charter §8)

Postgres (the estate **graph** plane) and Temporal (the **orchestration** plane) don't exist yet, so
wiring their MCP servers now would just fail to connect every session. Add them once the Phase-0
spike stands up the services. Paste into the `mcpServers` object in `.mcp.json`:

```jsonc
// Postgres — read/inspect the estate graph. Pin a *read-scoped* role; the graph is a projection
// (charter §1.2) — never let an MCP session become an unaudited write path to Entity attributes.
"postgres": {
  "command": "uvx",
  "args": ["postgres-mcp", "--access-mode=restricted"],
  "env": { "DATABASE_URI": "${STRATT_DATABASE_URI}" }
},

// Temporal — inspect Workflows/Runs (charter §2.3). Point at your dev namespace.
"temporal": {
  "command": "npx",
  "args": ["-y", "@temporalio/mcp-server"],
  "env": { "TEMPORAL_ADDRESS": "${TEMPORAL_ADDRESS:-localhost:7233}" }
}
```

Then export the referenced env vars (e.g. in `.claude/settings.local.json` `env`, or your shell) and
run `/mcp` to approve. Keep any credentials out of `.mcp.json` — reference them via `${VAR}` only.

> Verify the exact package names/flags against each server's current docs before enabling — pin a
> version rather than tracking `latest` for anything load-bearing (charter §1.5, §1.7).
