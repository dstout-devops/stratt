# ADR 0053 — MCP as a generic transport: the last domain logic leaves the core

- **Status:** **Accepted** (2026-07-17, steward) — steward-directed (the MCP re-centering: "a generic connector
  fed by various MCP servers, not locked to one implementation") + charter-guardian §1.5/§2.2 design review
  (SOUND-WITH-CHANGES, five must-fixes MF-1..MF-5 folded into the Decision). Unlike ADR-0052 this ADR **realizes**
  the charter (§1.5 already names MCP a transport) rather than departing from it — no conscious-departure
  sign-off is required. Does not edit the charter.
- **Date:** 2026-07-17
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.5 (sovereign contracts, multiple transports — "REST/gRPC, subprocess, and **MCP**
  are transports beneath [our contract]; no external protocol is load-bearing for the deterministic core") ·
  §2.2 (the Contract rung ladder — rung 3 mcp-declared-and-pinned) · §1.4 (spine keeps mechanisms, not domains) ·
  §7.3 (community-tier plugins sandboxed) · builds on ADR-0022 (MCP consumption), ADR-0046 (dark-matter),
  ADR-0051 (the EE-Job transport), ADR-0052 (SecretBroker as a generic port).

## Context

`core/internal/actuators/mcp` (470 LOC) is the **last domain logic in the Apache core** — the final piece of the
ADR-0046 dark-matter re-centering. It is the generic MCP client: Stratt consuming external MCP servers as
sandboxed tool providers (ADR-0022). Today it holds, IN-CORE, a hand-rolled JSON-RPC client
(`initialize`/`tools/list`/`tools/call`, `driver.py`), transport selection (stdio in-pod / Streamable-HTTP),
canonical schema hashing, and the rung-3 pinning machinery — an `if mcp {…}` protocol expert exactly of the kind
§1.4 forbids in the spine.

The steward's framing sets the direction: **MCP is not "the one mcp actuator" — it is a generic transport (a
connector like the SecretBroker port) fed by *various* MCP servers, not locked to one in-core implementation.**
The charter already says so: §1.5 names **MCP a transport beneath the sovereign contract**, "no external protocol
load-bearing for the deterministic core." Today MCP *is* load-bearing in the core; that is the defect.

Crucially, the generic-over-servers property **already exists**: MCP servers are fed in as Git-declared
`types.MCPServer{Name, Transport, Rev, Script, Endpoint, TokenRef}` desired state — the actuator is generic over
any declared server. What is wrong is only that the **protocol expertise lives in the core**.

**In scope:** extracting the MCP protocol + sandbox execution out of the core while keeping the rung-3
Contract-pinning seam core-validated. **Out of scope:** an MCP *server* (Stratt exposing its own capabilities
over MCP — that is the §1.6 agent-native surface, a separate thing); new MCP verbs beyond tools/list + tools/call.

## Decision

Extract the MCP **protocol** into a generic `stratt-mcp` **EE-Job shim** that speaks the sovereign port, fed by
the *unchanged* `MCPServer` declarations; keep **only** the rung-3 Contract-pinning **seam** in the core. MCP
becomes structurally what §1.5 says it is — a transport, not an in-core protocol expert.

1. **What LEAVES the core (protocol → `stratt-mcp` shim).** The JSON-RPC client (`initialize`/`tools/list`/
   `tools/call`), the stdio/HTTP transport selection, and the sandboxed execution of the (untrusted) MCP server
   move into a generic shim that runs in the EE pod (the §7.3 sandbox is why EE-Job, not gRPC — the untrusted
   server still runs in an ephemeral, network-policied pod). The shim is generic over ANY declared server: it
   reads the server declaration from the Job content and speaks MCP to it. **The shim carries the shim-side
   canonical-hash for its own live-drift hard-fail — but that is the Python COPY; the AUTHORITATIVE canonical
   hash stays in the core (MF-4, below), which recomputes and pins.**

2. **What STAYS in the core (the §1.5 sovereign-contract seam).** Rung-3 Contract **pinning** — canonical-hash
   verification, `RegisterMCPContract` at the declaration's rev, and **drift-within-a-rev is BLOCKING** — stays
   core-validated (§2.2 rung ladder, §1.5 "schema drift is blocking, never silently absorbed"; the ADR-0052
   finding-#3 principle: content-blindness is the seam, never its abandonment). So is the **args-against-pin**
   validation before a `call`. The plugin proposes tool schemas; the CORE pins them.
   - **MF-4 — the Go canonical hash STAYS in the core and is a BLOCKING cross-artifact gate.** Only the shim's
     Python copy leaves; the authoritative `CanonicalHash` (Go) remains the seam the core pins with. Because the
     Go core and the EE-mcp shim are now two independently-released artifacts, the Go↔Python canonical-form
     parity test is promoted from a follow-up to a **blocking, version-pinned cross-artifact gate** — a divergence
     fails safe (the call is refused) but permanently blocks legitimate tools (the ADR-0022 finding-#3 hazard) and
     version-skew between a new shim and an old core is the vector this extraction introduces; the gate closes it.

3. **How MCP maps onto the port — the rung branch is a t=0 STRUCTURAL INVARIANT (MF-1/2/3/5).** MCP register
   reuses the existing `derived_contract` channel (ADR-0047 §4), but that channel today defaults to rung-2
   auto-versioning (`host.go` drops the wire `rung`; `orchestrate.go` hardcodes `RungToolDerived` →
   `RegisterDerivedContract`, which *never* blocks). Reusing it for rung-3 without the following gates would
   silently auto-version an MCP tool-schema change instead of blocking it — the exact §1.5 drift-absorption
   violation. So the reuse ships WITH these invariants, in the Decision, not as follow-ups:
   - **MF-1 (fail-closed rung branch).** The host reads and carries `DerivedContract.rung` (today discarded) and
     branches registration: **rung-3 → `RegisterMCPContract`** (blocking-drift at the rev); rung-2 →
     `RegisterDerivedContract` (auto-version); **`RUNG_UNSPECIFIED`/unknown → HARD REJECT** (never a silent
     auto-version default).
   - **MF-2 (rev is core-authoritative, never shim-chosen).** The pin is keyed `(name, rev)`; the core keys it at
     the `types.MCPServer` **declaration's rev it already holds** (and passes into the Job content), NEVER the
     shim-echoed rev — else the untrusted shim escapes a drift block by minting a fresh pin at rev+1 (verify-
     don't-trust, ADR-0051 MF4). The proto `rev` is a `string`; the declaration rev is an `int`; the mapping and
     the authority (core) are pinned explicitly.
   - **MF-3 (pin is NEVER a side effect of a `call`, two structural gates).** The generic `derived_contract`
     channel is emitted on ordinary rung-2 Applies too, so the ADR-0022 invariant does not ride for free. BOTH
     gates are required: (i) the shim emits tool-schema `derived_contract`s **only in register mode** (call mode
     is names-only `TaskEvent`s); AND (ii) the core mints a rung-3 pin **only when the Step is a register-mode
     MCP Step** — a `derived_contract` arriving on a call-mode MCP Apply is REJECTED, never pinned.
   - **MF-5 (register stays a NON-Syncer, §2.2).** The register path emits **`derived_contract` only, NEVER
     `write_back`** Entities/Facets — projecting zero graph state keeps "MCP is not a Syncer" structural (MCP has
     no pagination/change-feed; it is a client, not a Syncer transport).
   - **call** → the core validates the args against the pinned Contract (the door check, core-side), dispatches
     the shim, and the shim invokes the tool and emits the result as a typed `TaskEvent` + `ItemResult`
     (`STATUS_CHANGED` for an effectful call). The live-schema drift check stays in the shim as a defense-in-depth
     hard-fail before `tools/call`, mirrored by the core's authoritative pin at register.
   - **§1.8 diagnostic floor carried forward (F-1).** The shim forwards server stderr/tracebacks/JSON-RPC errors
     and the `schema_drift` both-hashes as typed diagnostic `TaskEvent`s (ADR-0022 stderr fix + ADR-0051 MF5); a
     shim dying without a terminal `ItemResult` folds to not-OK via the required-terminal — reused from `ApplyRaw`.

4. **MCP servers feed in as declarations (unchanged).** `types.MCPServer` stays the desired-state seam — a new
   MCP server is added by Git-declaring it, never by core code. The shim renders the server (stdio script mounted
   read-only + run verbatim, or the HTTP endpoint + a kubelet-mounted token, §2.5) from the declaration the core
   passes in the Job content. "Fed by various MCP, not locked to one implementation" is realized: the core knows
   the *transport*, never a specific server.

The in-tree `core/internal/actuators/mcp` is deleted; the `ee/mcp.Dockerfile` bakes the `stratt-mcp` shim; the
`contracts/actuators/mcp.input` Contract and the `types.MCPServer` declaration stay.

**Pinned invariant:** the rung-3 canonical-hash + drift-blocking pin stays CORE; only the protocol leaves. The
core validates the schema seam (register) and the args seam (call); the shim speaks MCP. Content-blindness is
§1.1 *at the seam*, never its abandonment.

## Charter alignment

- **§1.5 — realized, not merely upheld.** MCP moves from a load-bearing in-core protocol to a genuine transport
  beneath the sovereign contract — exactly the charter's words made structural. The core owns the connector
  contract (rung-3 pin, args validation); MCP is one transport under it, fed by various servers.
- **§2.2 (rung ladder) — the rung-3 seam stays core.** Pinning + drift-blocking is the sovereign-contract
  validation §1.5 mandates stay core-validated (ADR-0052 finding-#3 principle). Only its *computation input* (the
  live schema) now arrives over the port as a governed `derived_contract`.
- **§7.3 (sandbox) — preserved.** The untrusted MCP server still runs in an ephemeral, sandboxed EE pod; EE-Job
  (not gRPC) is chosen precisely to keep that isolation. Tool *descriptions* stay off the pinned Contract
  documents (screening posture, unchanged).
- **§1.4 / ADR-0046 — the last `if <tool> {…}` leaves.** After this, the core holds graph, coordinates,
  contracts, reconcile, authz, audit — and zero domain logic. Dark matter is complete.
- **ADR-0022 invariant preserved:** a rung-3 pin is only ever minted by a deliberate `register`, never as a side
  effect of a `call`.

## Consequences

- **Positive:** the core reaches ZERO domain logic (dark-matter complete); MCP is a genuine transport, any MCP
  server plugs in by declaration; the EE-mcp image ships a generic shim, not core code; the rung-3 seam + sandbox
  posture are unchanged.
- **Negative / trade-offs:** the register path now routes tool schemas over the port's `derived_contract` channel
  at rung 3 (a small generalization of a rung-2 channel — the core must pin at the declaration's `rev`, not
  auto-version); the canonical-hash form must stay byte-identical across the shim (Go/Python) and the core (the
  existing ADR-0022 cross-language-hash risk moves with the code, not away). The EE-Job transport must carry the
  per-server (not per-target) shape — register/call are per-server operations, so the shim's "targets" are the
  single server.
- **Follow-ups:** (the rung-3 channel mode + the parity gate are now Decision invariants MF-1/MF-4, not
  follow-ups.) **F-2** — `MCPServer` is still not a §2 Named Kind (a pre-existing, steward-deferred
  vocabulary-linter flag from ADR-0022); since this ADR closes out dark matter, route that flag to the
  vocabulary-linter for a resolution decision (charter it, or fold into Bundle). **F-3** — the pre-existing
  ADR-0022 egress posture rides along unchanged: HTTP-transport MCP pods need a defined NetworkPolicy egress
  posture before any non-dev deployment, and http-transport pins are not Git-reproducible (re-screen on graph
  rebuild) — named here so the extraction does not silently drop it. The live end-to-end (a real MCP server in a
  dev pod) is the same dev-cluster signal outstanding for ansible/script.

## Reviews

- **charter-guardian, §1.5/§2.2 design review (2026-07-17): SOUND-WITH-CHANGES → folded above.** The direction is
  charter-realizing (MCP becomes structurally the transport §1.5 already names it) and completes dark matter;
  the rung-3-stays-core split is faithful to ADR-0052 finding-#3. Five must-fixes, folded into the Decision:
  **MF-1** (the rung branch is a fail-closed t=0 invariant — host carries `rung`, branches rung-3→blocking /
  rung-2→auto-version / unknown→hard-reject; without it an MCP schema change silently auto-versions, the §1.5
  drift-absorption violation), **MF-2** (the pin rev is core-authoritative from the declaration, never the
  shim-echoed rev — else the untrusted shim mints a fresh pin to escape a drift block), **MF-3** (pin-never-a-
  side-effect-of-a-call needs BOTH gates: shim emits schemas only in register mode AND the core pins only on a
  register-mode Step), **MF-4** (the Go canonical hash STAYS core; the Go↔Python parity test becomes a blocking
  version-pinned cross-artifact gate, since shim and core are now independently released), **MF-5** (register
  emits `derived_contract` only, never `write_back` — keeps MCP a non-Syncer). Flags: **F-1** (§1.8 diagnostic
  floor carried forward, in the Decision), **F-2**/**F-3** (routed above). EE-Job over gRPC confirmed correct for
  §7.3 (untrusted server code). No vocabulary/model tension (MCP-as-Actuator is §2.3-native).

## Alternatives considered

- **A gRPC `stratt-mcp` plugin (like notify)** — rejected: the MCP server (stdio) is *untrusted Git-declared
  code* that must run in a sandboxed ephemeral pod (§7.3, ADR-0022). A long-lived gRPC plugin would either run
  untrusted servers in its own long-lived process (blast radius) or still need a pod per call — so EE-Job (the
  sandbox is native) is the correct transport, matching ansible/script.
- **Move the rung-3 pinning into the shim** — rejected: pinning + drift-blocking is the §1.5/§2.2 sovereign
  schema seam; handing it to the plugin hands the seam to the thing it exists to police (the exact ADR-0052
  finding-#3 error). The core recomputes the canonical hash and pins.
- **Leave mcp in-core as a special case** — rejected: it is the one remaining `if <tool> {…}` in the spine; the
  whole dark-matter thesis is that the core knows *mechanisms* (transports, pinning), never *protocols*.
- **A standalone MCP capability-broker plugin serving other plugins** — out of scope (mirrors the ADR-0052 F-2
  gate): a generic MCP client suffices; a broker concentrating servers for peers is a later, separately-reviewed
  question.
