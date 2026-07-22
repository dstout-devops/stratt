---
name: mcp-security-review
description: >-
  Security review for MCP (Model Context Protocol) surfaces — both server/handler SOURCE CODE and
  .mcp.json CONFIGURATION. Covers the baseline controls (identity isolation, sessions, rate limits,
  schema validation, official-SDK usage), the 7 RCE vectors, the OWASP MCP Top 10, and config-plane
  risks (hardcoded secrets, shell-injection in args, unpinned servers). Use when reviewing an MCP
  server implementation before release, auditing an .mcp.json, checking a tool handler for injection,
  or for "is my MCP server/config secure?". Especially relevant to Stratt's Go MCP surface and the
  sovereign plugin port (ADR-0046), where MCP is a transport beneath the sovereign contract, never
  load-bearing for the deterministic core (charter §1.5, §1.6). Adapted from github/awesome-copilot.
---

# MCP Security Review (Stratt)

Reviews Stratt's MCP surfaces against a security baseline and produces a report with file/line
evidence. Two planes:

- **Implementation** — server/handler source (the `mcp` Actuator, the EE-MCP image, any Go MCP
  server on the sovereign port). This is the main body below.
- **Configuration** — `.mcp.json` and equivalent (which MCP servers a workspace registers). See
  "Config-plane audit" at the end.

**Charter framing (read first):**
- MCP is a **transport beneath the sovereign contract**, never load-bearing for the deterministic
  core (§1.5). A network-exposed MCP server is a real attack surface and gets the full baseline.
- **Contracts/Facet schemas are pinned and hash-verified** (§1.5); an MCP-declared schema is the
  *lowest* Contract rung and is never admissible for a Syncer. Schema validation (MCP-04) is
  non-negotiable.
- **MCP tool outputs are untrusted data** and must be screened for tool-description / instruction
  injection before they re-enter a model or the graph (maps to OWASP MCP03/MCP06 below).
- MCP is **not** an admissible Syncer transport (needs full-fidelity native transports); if a review
  finds a Syncer projecting via MCP, that is a charter violation, not just a security note.

## Process — follow in order

**Step 1 — Classify the target.** Confirm MCP protocol version
[2025-03-26](https://modelcontextprotocol.io/specification/2025-03-26) or later (current
[2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25)) — flag older as a finding but
continue. Determine **server vs client**. Classify transport as **network-exposed** or **local-only**
(table below). Record transport, protocol version, whether sessions exist.

**Step 2 — Filter false positives.** Apply the false-positive filters before opening findings. Keep
docs only when they describe the repo's own server behavior/transport/auth. For framework/SDK repos,
scope to default configuration and public API surface.

**Step 3 — Baseline controls.** For **network-exposed servers**, check **MCP-01..MCP-05**. For
**local/STDIO servers**, don't PASS/FAIL the baseline — give best-practice notes and continue to RCE.
For **clients**, only review token/session handling visible in client code.

**Step 4 — RCE vectors.** Review all 7. Mark each SAFE / AT RISK / N/A. Prefer direct evidence over
inference.

**Step 5 — OWASP MCP Top 10.** Evaluate all 10. If a Step-3 control already covers a risk, reference
it. For local/STDIO servers, mark network-dependent risks (MCP07, MCP09) N/A. Mark PASS / FAIL /
NEEDS-INVESTIGATION.

**Step 6 — Report.** Use the output format below with file/line references in every justification.
Separate code findings from manual follow-ups. Incomplete evidence → NEEDS-INVESTIGATION naming the
missing artifact.

## Decision rules

- **Network-exposed server:** all 5 controls, then RCE + requested OWASP.
- **Local/STDIO server:** best-practice guidance only for the 5 controls; still run RCE (tool input
  can execute locally).
- **Client:** review received-token handling and refusal to trust server-provided session IDs.
- **Reverse proxy / container exposure:** if traffic can reach the server over a network, treat it as
  network-exposed even if inner binding is localhost.
- **Unclear evidence / ambiguous auth coverage:** do not guess — mark NEEDS-INVESTIGATION and say
  what must be verified manually.
- **Undeterminable transport:** flag for manual review; do **not** assume STDIO (that would wrongly
  skip the server controls).

## Transport classification

**Network-exposed (enforce all controls):** `transport="http"`/`"sse"`;
`StreamableHttpServerTransport`; `SSEServerTransport`; Go `server.NewStreamableHTTPServer` /
HTTP+SSE handler; `host="0.0.0.0"` or no explicit host on Express/Node; `EXPOSE`/published `ports:`
on a Dockerfile/compose with an MCP server.

**Local-only (best practices only):** `StdioServerTransport`; Go `server.ServeStdio` /
`WithStdioServerTransport`; `transport="stdio"`; FastMCP `mcp.run()` no args; `.mcp.json` with a
`command` key and no URL.

**Host-binding gotchas:** `0.0.0.0` → network-exposed; `127.0.0.1`/`localhost` → local; no explicit
host on Express/Node → defaults to `0.0.0.0` (network-exposed); Docker `ports: "8000:8000"` →
network-exposed even if the process binds `127.0.0.1` inside the container.

## False-positive filters

Skip: `.github/skills/` templates; vendored SDK/OSS copies (`node_modules/`, `vendor/`, files
*defining* `McpServer`/`FastMCP`); `.mcp.json` client configs with no server code; docs/tutorials
with unrelated code fences; outbound-only auth libraries used solely for outbound calls. Docs
describing the repo's **own** server are not false positives.

## Baseline controls

**MCP-01 — Identity isolation** (remote servers). Authenticate every inbound request with a trusted
IdP and enforce authz at the server boundary; never infer auth from session IDs, prior requests, or
network location. Use a dedicated server application identity + audience; outbound calls use their
own scoped credentials, never the inbound token. Only metadata-only OAuth/MCP discovery endpoints
(`/.well-known/oauth-protected-resource`, etc.) may be unauthenticated. *Stratt:* one Principal
model, one authz (OpenFGA), one audit stream (§1.6) — the MCP surface authenticates as an ordinary
Principal, not a privileged bypass. Pitfall: shared identities or forwarded caller tokens create
confused-deputy paths.

**MCP-02 — Sessions** (remote servers with sessions). No session IDs anywhere → N/A (per-request auth
still required). SDK/transport-managed but generation not visible → NEEDS-INVESTIGATION. Authenticate
**every** request; session state never substitutes for token validation. Session IDs are opaque
CSPRNG correlation tokens — they grant no privileges, encode no authz, and never appear in URLs.
Pitfall: treating a session ID as a bearer credential.

**MCP-03 — Rate limits** (servers + tools). Enforce rate limits and abuse protection on tool
discovery and invocation **at the server runtime**, not only a gateway; partition by authenticated
identity and by session. Stricter limits for mutation/high-cost tools; on exceed, fail closed with
HTTP 429 + Retry-After and do not execute the tool. Starting thresholds (tune to load/cost):
read-only 100/min-identity, mutation 10/min, high-cost 5/min, discovery 30/min. *Stratt:* aligns with
per-identity cost/usage accounting (§1.6). Pitfall: gateway-only or one flat bucket.

**MCP-04 — Schema validation** (servers exposing structured tools). Validate **all** tool arguments
against explicit schemas **before execution**: types, required, enums, bounds, and
`additionalProperties: false` by default. Validation runs server-side on every call; invalid input
fails closed with a 400/MCP error and no backend action. *Stratt:* the Contract on a Step's inputs
must be a pinned, hash-verified JSON Schema validated by a standard validator (§1.5) — this control is
the charter's schema discipline at the MCP boundary. Pitfall: allowing extra properties or
client-only validation.

**MCP-05 — SDK-first** (remote servers). Build on an **official MCP SDK**: Tier 1 = TypeScript,
Python, C#/.NET, **Go (modelcontextprotocol/go-sdk)** — Stratt's control plane is Go, so the Go SDK
is the expected foundation. No official SDK → NEEDS-INVESTIGATION and require direct evidence for
auth, sessions, rate limits, and schema validation. Keep the SDK current and patched (§1.7 evergreen).
Pitfall: hand-rolled servers miss one primitive and the gaps compound.

## RCE vectors (mark each SAFE / AT RISK / N/A)

| Vector | Dangerous | Safe | Test payload | CWE |
|---|---|---|---|---|
| Command injection | `exec("convert "+arg)`, `os/exec` with `sh -c`+string, `os.system(f"…{input}")` | `exec.Command("convert", arg)` (no shell), `execFile`, `subprocess.run([...], shell=False)` | `; rm -rf /`, `$(curl attacker)`, `\| net user` rejected/literal | CWE-78 |
| Dynamic code eval | `eval(arg)`, `new Function(arg)()`, template-exec of tool output | sandboxed/AST parser or allowlist | `__import__('os').system('id')` rejected | CWE-94/95 |
| Unsafe deserialization | `pickle.loads`, `yaml.UnsafeLoader`, `gob`/binary of untrusted input | `yaml.safe_load`, `JSON.parse`+schema; avoid binary for untrusted input | crafted payload rejected | CWE-502 |
| Path traversal | `os.ReadFile(arg)`/`fs.readFile(arg)` unvalidated, `open(user_path,'w')` | canonicalize + enforce an allowlisted base dir before read/write/exec | `../../../../etc/passwd`, `..\..\.env` rejected | CWE-22 |
| SSTI | `Template(user_input).render()`, `Handlebars.compile(arg)()` | predefined templates with parameters only | `{{7*7}}`, `${7*7}` must not render `49` | CWE-1336 |
| Dependency hijacking | unpinned deps; internal names resolvable from public registries | pin exact versions + lock/integrity hashes; trusted/scoped registries; verify signatures | `go mod verify`, `npm/pip audit`, review CVEs | CWE-829 |
| SSRF | `http.Get(user_param)`, `fetch(input)` | allowlist schemes/domains; block RFC1918 + link-local; validate before send | `http://169.254.169.254/latest/meta-data/`, `http://localhost:8080/admin` rejected | CWE-918 |

## OWASP MCP Top 10 (mark PASS / FAIL / NEEDS-INVESTIGATION)

1. **MCP01 Token mismanagement & secret exposure** — no hardcoded secrets, sensitive fields
   redacted, short-lived/rotated tokens (Stratt: CredentialRef, material never persists — §2.5).
2. **MCP02 Privilege escalation via scope creep** — least-privilege scopes, per-request authz, no
   runtime capability expansion.
3. **MCP03 Tool poisoning** — tool definitions static and server-controlled; outputs are data, not
   LLM-parseable instructions.
4. **MCP04 Supply chain & dependency tampering** — lock file, exact pinning, no suspicious
   post-install, no unpatched CVEs, trusted registries (§7.3).
5. **MCP05 Command injection & execution** — no shell execution from untrusted input; only
   parameterized/allowlisted exec.
6. **MCP06 Prompt injection via contextual payloads** — tool outputs are data; untrusted content
   sanitized/truncated/sandboxed; chained calls guarded. *(This is the charter's "screen MCP outputs
   for tool-description injection" line — always FAIL if raw external content returns to the model.)*
7. **MCP07 Insufficient auth/authz** — all endpoints require valid auth; per-tool authz; enforced
   server-side, not only at the gateway. (N/A for local/STDIO.)
8. **MCP08 Lack of audit/telemetry** — invocations logged with caller identity, centralized, alerted
   (Stratt: one audit stream — §1.6).
9. **MCP09 Shadow MCP servers** — inventoried, isolated, owned. (N/A for local/STDIO.)
10. **MCP10 Context injection & over-sharing** — minimal data returned, sensitive fields masked,
    context isolated per user/Principal.

## Output format

Cite specific file/line evidence in every Justification cell.

**Control summary** — table of MCP-01..05 with status (PASS / FAIL / NEEDS-INVESTIGATION / N/A) +
justification. Use PASS only when code clearly satisfies the control; FAIL when the violation is
observable; NEEDS-INVESTIGATION when it depends on deployment/IdP/logs not visible in source.

**RCE summary** — the 7 vectors with SAFE / AT RISK / N/A + justification.

**OWASP summary** — the 10 risks with PASS / FAIL / NEEDS-INVESTIGATION + justification.

**Manual follow-ups** — every check unresolved from source, naming the artifact/access needed.

## Config-plane audit (.mcp.json)

A misconfigured `.mcp.json` can leak credentials, allow shell injection, or connect to untrusted
servers. Treat the following as **review patterns** (not code to execute) when auditing a config:

- **Hardcoded secrets** (CRITICAL) — any credential literal in `args`/`env` instead of a `${VAR}`
  reference. Look for: `api_key`/`token`/`secret`/`password` assignments with an inline value;
  `Bearer <token>`; provider token prefixes (`ghp_`/`gho_`/`sk-`/`AKIA…`); `-----BEGIN … PRIVATE
  KEY-----`; a DB URL with inline `user:password@`. Fix: `"env": { "API_KEY": "${MY_API_KEY}" }`.
  (Stratt §2.5 — material never persists; reference brokered secrets only.)
- **Shell-injection in args** (HIGH) — `$(...)`, backticks, `;`/`|`/`&&`/`||` chaining, `eval`,
  `bash -c`/`sh -c`, `>/dev/tcp/…`, `curl … | sh`. Fix: direct command execution, no shell
  interpolation.
- **Unpinned servers** (MEDIUM) — `@latest` or an unversioned package in `args`. Fix: pin an exact
  version (matches §1.5 "pinned and hash-verified" and §1.7 evergreen). `npx` without `-y` is a LOW
  (interactive prompt in CI).
- **Unapproved / unexpected servers** (MEDIUM) — a server not on the project's known list; for
  Stratt, cross-check against `docs/mcp-servers.md`. Prefer `http` transport; SSE is deprecated.

Config-plane report shape:

```
MCP Security Audit — .mcp.json
Servers scanned: N
Findings: X (a CRITICAL, b HIGH, c MEDIUM)

[CRITICAL] <server>: hardcoded secret in config
  Fix: use an env-var reference: ${ENV_VAR}
[MEDIUM]   <server>: unpinned dependency <pkg>@latest
  Fix: pin an exact version
```

## Exception process

Document the unmet control, exact deviation, residual risk, and any compensating controls; route
through security/release approval with an owner and expiry; record and re-evaluate on expiry or
whenever the server, tools, traffic profile, or exposure changes.
