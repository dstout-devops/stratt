# ADR 0022 — mcp Actuator: consuming external MCP servers, rung 3

- **Status:** Accepted
- **Date:** 2026-07-12
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.5 (drift blocking), §2.2 (rung 3:
  MCP-declared-and-pinned; trust tiers), §2.3 (`mcp` generic MCP-client),
  §3 (sandboxed identically), §7.3 (injection screening), §8 Phase 2

## Context

The other half of Phase 2's MCP line item: Stratt as MCP **client** —
external MCP servers as execution tools. The design problem is rung 3: the
server declares its own tool schemas, so pinning and drift-blocking must be
real, not ceremonial.

## Decision

1. **CaC kind `mcp-servers/`** (`types.MCPServer`): name, transport
   (stdio | http), **rev** (keys the pins), stdio **script** — the server's
   entire source in the declaration, run verbatim in the sandbox. Git
   review authorizes exactly what executes; the spawned command can never
   derive from Principal or Run-time input — the structural mitigation for
   the MCP stdio-injection class (dependency-scout mandate). http servers:
   endpoint + optional tokenRef (CredentialRef pointer; the kubelet mounts
   material into the pod, the control plane holds names only, §2.5).
2. **Driver: hand-rolled JSON-RPC client** (dependency-scout REJECT on the
   Python MCP SDK for this bounded use: its mandatory ASGI-server stack —
   starlette/uvicorn/pyjwt[crypto] — is dead attack surface in a sandbox,
   and the SDK is mid-v2 rework). stdio = stdlib only; http = pinned httpx.
   EE image `stratt-ee-mcp` (python3.14-alpine + httpx). A CI conformance
   test runs the real driver against a reference stdio server fixture on
   every `go test` — spec drift is caught like schema drift (§1.5); the
   same fixture is the e2e declaration.
3. **Registration is a Run.** `mode: register` lists the server's tools and
   pins each schema as Contract `mcp/<server>/<tool>.input` at
   **version = rev**, rung `mcp-declared`, with **blocking** semantics
   (`RegisterMCPContract` = RegisterContract's drift rules): same-rev drift
   fails the Run with both hashes named. Accepting a schema change is a Git
   act — bump rev, re-register; superseded pins remain as audit. Rung 2's
   auto-versioning is deliberately not reachable from this path.
4. **Calls verify the pin twice.** Prepare refuses unpinned tools ("register
   first"), validates arguments against the pinned schema (JSON-pointer
   errors at the door), and writes the pinned hash into step.json; the
   driver re-lists tools and **hard-fails before tools/call** when the live
   schema's canonical hash differs — drift blocks inside the sandbox
   against a control-plane pin, with both hashes on the Run's events
   (§1.8). Canonical form = sorted-keys compact JSON in both languages;
   parity is unit-tested; any mismatch fails safe. Successful calls report
   `changed` — an external tool call is presumed effectful.
5. **Screening (§7.3):** tool description text is never pinned into
   Contract documents — it rides the registration Run's events,
   inspectable; agent-facing surfaces already envelope estate text
   (ADR-0021).

## Consequences

- Verified live end-to-end: register pinned `mcp/demo-tools/greet.input`
  v1 (`mcp-declared`); a call returned the tool result; bad arguments
  failed with the contract named and JSON pointers; a Git schema mutation
  **without** a rev bump blocked the call in-sandbox (schema_drift, both
  hashes) and blocked re-registration (ErrContractDrift); rev 2
  re-registered cleanly, v1 retained as audit.
- Call-mode Runs also emit declared schemas; orchestrate re-pins them —
  idempotent for the pinned rev by construction (an unpinned tool cannot
  reach call mode: Prepare refuses it first).
- The http transport reaches out of the pod — the same egress posture as
  the tofu state-backend; NetworkPolicy hardening lands with the Phase-3
  sidecar work as already recorded.
- charter-guardian findings, fixed in-slice: (1) **call-mode Runs could
  mint pins for unreviewed sibling tools** — registration by side effect,
  defeating "pinned at registration". Now double-gated: the driver ships
  schemas only on register-mode events (call mode lists names only), and
  the orchestration pin path additionally requires `mode: register`.
  (2) The stdio server's stderr was discarded — a crashing declared server
  hid its traceback (§1.8); stderr now surfaces as events on the failure
  path. (3) Go/Python canonical forms diverged on HTML-special and
  non-ASCII characters (Go HTML-escapes by default; Python
  ensure_ascii) — would have permanently blocked legitimate tools, fail-
  safe but broken; both sides now pin one form (SetEscapeHTML(false) /
  ensure_ascii=False), parity-tested over adversarial schemas.
- Guardian flags, recorded: execution pods still lack NetworkPolicy — this
  slice is the first combining arbitrary in-pod code with deliberate
  egress; a defined egress posture is REQUIRED before any non-dev
  deployment (rides the Phase-3 sidecar/network work, priority raised).
  And http-transport pins are not reconstructable from Git alone (the
  approved hash lives in graph.contract) — a graph rebuild requires
  re-screening http servers before re-registration; stdio pins ARE
  Git-reproducible (source + rev in the declaration).
- vocabulary-linter flag, disclosed: **MCPServer** is not a §2 Named Kind
  yet appears as a CaC kind/table/wire schema. Position taken here: it is
  MCP-protocol-native terminology — the protocol's own noun for the thing,
  namespaced under the `mcp` Actuator the charter names — analogous to
  opentofu's tool-native `workspace`, not a new estate concept. **Steward
  decision recorded as open:** either charter it (a §2 amendment — highest
  review bar) or fold it into the Bundle packaging model when that
  solidifies (packaged servers already point there). Until then it stays a
  tool-scoped declaration kind, not vocabulary.
- Deferred: packaged/registry servers (npx-style) wait for the
  Bundle/trust-tier packaging model — in-declaration source is the v1
  community-tier posture (sandboxed by default, §2.2); MCP resources/
  prompts (tools only in v1); Step output binding of tool results.
