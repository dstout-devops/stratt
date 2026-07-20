# ADR 0091 — the UI is a first-party, served-by-default, pure `/api/v1` client (never a port-plugin, never a gated add-on)

- **Status:** Accepted
- **Date:** 2026-07-20
- **Deciders:** steward (dstout)
- **Reviews:** charter-guardian — PASS-WITH-CHANGES: design charter-faithful on every axis; citation
  must-fixes folded (§7.4 mis-cite → §1.3/§7.5/§1.8; lowercase "bundle"→"served UI assets" to reserve the
  **Bundle** Kind).
- **Charter sections:** **§1.6** (agent-native, human-first — UI/CLI/CI/AI are co-equal clients under one
  Principal / one authz / one audit / one cost model), **§3.1** (the interface plane: Web UI · CLI · REST/
  OpenAPI · MCP — "one API, one Principal model"; "community code never executes in the interface plane"),
  **§1.3** (rug-pull-proof — "Apache-2.0 everything — no gated tier, ever") + **§7.5** (no paid tier) +
  **§1.8** (diagnosis is a product surface) + **ADR-0003 L9** (no paywalled diagnosis — every diagnostic
  surface lives in the single Apache-2.0 product), **§7.1** (any-Kubernetes self-host; single-artifact
  posture), **§1.4** (clarified: the sovereign plugin port is for *outbound tool breadth*, not interface
  surfaces — the UI is explicitly **not** a port-plugin).
- **Builds on:** ADR-0012 (Views UI v1 — established "the UI is a pure client of /api/v1, holds no privileged
  path"), ADR-0090 (the greenfield rebuild), ADR-0013 (Helm packaging; strattd serves the UI via
  `STRATT_UI_DIR`), ADR-0046 (the sovereign plugin port — what the UI is NOT).

## Context

Now that the UI is a real, substantial surface (ADR-0090), we need a written answer to a recurring question:
is the UI **built into core**, or is it a **plugin / add-on**? The framing is a category error worth
dissolving first — "plugin" in Stratt means the **sovereign port** (ADR-0046): *outbound* Syncers/Actuators/
Actions that integrate the platform with external tools. The UI is not tool breadth; it is an **interface
surface**, peer to the CLI and the MCP server. So it is never a port-plugin.

The real axes are **ownership** (first-party vs community) and **packaging** (served in-tree vs separate artifact) —
and the coupling question is already settled by construction: the UI imports **nothing** from the Go core;
it depends only on the **OpenAPI contract** (`core/api/openapi.yaml` → generated `ui/src/api/schema.d.ts`),
authenticates as an ordinary Principal, and holds no privileged path (ADR-0012/0090). It is *already* a pure,
decoupled client. What remains is to make that a **binding rule**, so working on the UI vs the backend has a
hard, contract-shaped boundary — shrinking the "areas of concern" for any given change and letting the two
sides iterate independently.

## Decision

**The Stratt UI is a first-party, core-owned, served-by-default, PURE `/api/v1` client. It is never a
sovereign-port plugin, and never a gated / paywalled / optional-for-diagnosis add-on. Its one boundary is the
OpenAPI contract.**

### 1. Coupling — a pure `/api/v1` client (the enforced seam)
The UI may reach the platform through **only** the native `/api/v1` OpenAPI surface, as an ordinary Principal
under the same grants/authz/audit as the CLI and MCP (§1.6). It imports no core Go, shares no in-process
types, and has no back door. Its types are **generated** from `core/api/openapi.yaml`; drift is gated by the
existing `generate:check`. **This contract is the single seam between "UI" and "Stratt."**

### 2. Ownership — first-party, not a community plugin
The UI is first-party (in the monorepo, `ui:ci` in the root `ci` gate, Apache-2.0), like the CLI — **not** a
community-owned or optional component. The diagnosis/descent surface is load-bearing (§1.8, ADR-0003 L8/L9):
a Stratt with no diagnosis UI is a Stratt users route around. This is the deliberate inverse of Connectors,
where the community *owns breadth* via the port — the interface plane stays first-party.

### 3. Packaging — served by default (but serving ≠ coupling)
strattd serves the built UI (`ui/dist`) via `STRATT_UI_DIR` today; graduating to a `go:embed` single binary
is the follow-up (ADR-0013 deferral). Because the UI is a *pure client*, serving-it-with-strattd is a
**distribution choice, not a coupling** — the same built UI assets can be served standalone. Default: one
artifact, one supply chain
(cosign/SBOM), zero version skew (the served types match the serving API), and the diagnosis surface
*guaranteed present* (§7.1, §1.8).

### 4. Binding rules (the boundary that shrinks areas of concern)
- **UI → only `/api/v1`.** No privileged path, no in-process coupling, no second transport. (The AWX-compat
  `/api/v2` façade is for external AWX tooling, not the UI; MCP is for agents.)
- **API-first for every capability.** A new UI capability requires the corresponding API capability *first* —
  the UI can do nothing the API doesn't expose. This **dogfoods §1.6** (the UI's existence proves the API is
  complete) and means a change is cleanly *either* a backend/contract change *or* a UI presentation change,
  rarely both.
- **Never gated.** The UI — especially diagnosis/descent — is always in the single Apache-2.0 product; never
  a paid tier, never a licensed add-on, never optional-for-diagnosis (§1.3/§7.5, §1.8, L9).
- **Not a port-plugin.** The UI never registers on the sovereign plugin port; that port is outbound tool
  breadth only (§1.4).
- **Contract-typed, drift-gated.** UI types are generated from `openapi.yaml`; `generate:check` fails on skew.

### 5. What this deliberately allows
Because the seam is the OpenAPI contract and the UI is a pure client: an operator MAY deploy the UI
standalone (a static UI build pointed at a Stratt API) for API-only/air-gapped topologies; the community MAY
build an *alternative* first-party-contract frontend; and UI and backend work MAY proceed in parallel against
the frozen contract. None of these change the default (served in-tree) or the ownership (first-party).

## Charter alignment
- **§1.6:** the UI is co-equal with CLI/CI/agents — one Principal, one authz, one audit, one cost model; the
  API-first rule makes UI/API parity structural, not aspirational.
- **§3.1:** matches the interface-plane architecture (Web UI · CLI · REST/OpenAPI · MCP, one API).
- **§1.3 / §7.5 / §1.8 / L9:** first-party + never-gated keeps all diagnosis in the single Apache-2.0 product.
- **§7.1:** served-by-default preserves the single-artifact self-host posture.
- **§1.4:** the UI is explicitly not a port-plugin — dissolving the category error and keeping the port for
  outbound tool breadth.

## Alternatives considered
- **UI as a sovereign-port plugin.** Rejected — a category error. The port carries outbound tool
  integrations (content-blind, gRPC); a browser frontend is an interface surface, not a Connector.
- **UI as a community / optional add-on.** Rejected as ownership — the diagnosis surface is too load-bearing
  (§1.8, L8/L9) to be community-owned or absent; unlike breadth (Connectors), the interface plane is
  first-party. (An *alternative* community frontend against the public contract is welcome — see §5 — but the
  primary UI stays first-party.)
- **Separate repo/artifact as the DEFAULT.** Rejected as default (allowed as an option, §5) — two artifacts
  add deploy friction and version-skew risk against the single-artifact §7.1 posture. The pure-client
  architecture already delivers the decoupling benefits without paying that cost by default.
- **Core-coupled UI (shared in-process types / privileged path).** Rejected — breaks the clean seam, couples
  release cadence, and violates the §1.6 "pure client, no privileged path" posture.

## Consequences / follow-ups
- Record the pure-client + API-first rules in `CLAUDE.md` / the frontend rules as a review checkpoint.
- Graduate strattd's UI serving to `go:embed` for a true single binary (ADR-0013 deferral) — a packaging
  change only.
- The `generate:check` gate (already wired) is the enforcement point for contract drift; keep it in `ci`.
