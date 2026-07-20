# MCP servers

Project-scoped MCP servers live in [`.mcp.json`](../.mcp.json) at the repo root and are checked into
git so the whole team shares them. The first time you open the project, Claude Code prompts you to
approve the project servers (`/mcp` to review; they show as `⏸ Pending approval` until you trust the
workspace).

## Active now

| Server | Transport | Why | Notes |
|---|---|---|---|
| **context7** | http | Live, version-pinned library docs at query time. Directly serves the **Evergreen contract** (charter §1.7) and keeps the `dependency-scout` subagent accurate. | Works keyless (rate-limited). For higher limits, get a key at context7.com and add `"headers": { "Authorization": "Bearer ${CONTEXT7_API_KEY}" }` to the server entry, then export `CONTEXT7_API_KEY`. |
| **playwright** | stdio (`npx @playwright/mcp`) | Drives a real browser so Claude can visually verify the React UI (charter §3.1). | First real use may need `npx playwright install chromium`. |

GitHub is intentionally **not** an MCP server — per Claude Code best practices the `gh` CLI is the
most context-efficient path, and it's installed in the devcontainer. Use `gh` for PRs/issues/releases
(and remember `git commit -s` for DCO, charter §1.3).

## Optional — substrate MCP servers (wire when running the dev stack)

The substrate now exists (`task dev …` stands up Postgres/NATS/Temporal). These two servers let Claude
inspect the estate **graph** (Postgres) and **orchestration** (Temporal) planes directly. They're kept
**out of `.mcp.json` by default** so sessions don't fail to connect when the stack isn't running — add
them when you want live introspection during a dev-stack session. Paste into the `mcpServers` object:

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
