# ADR 0014 — Connector breadth: MS Graph and EC2 cloud-instance Syncers

- **Status:** Accepted
- **Date:** 2026-07-12
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.1, §1.2, §2.1, §2.2 (Syncer transport fidelity), §2.5, §8 (Phase 1 breadth)

## Context

The last Phase-1 board item: second and third Connectors, exercising the
Connector/Syncer seam the way `script` exercised the Actuator seam. The vCenter
Syncer's shape is the template: Register (Source + Facet-owner claims) → full sync →
delta ingestion → Projector writes with Provenance → SyncCursor persistence;
tool-shape mapping isolated in a pure normalizer.

## Decision

1. **MS Graph Syncer** (`core/internal/connectors/msgraph`) over Entra directory
   devices. Transport: **native Graph REST delta queries** (§2.2 full fidelity) —
   full enumeration pages via `@odata.nextLink`, the `@odata.deltaLink` token
   persists as the sync cursor (restarts resume incrementally — verified), removals
   arrive as `@removed` → tombstone by identity, HTTP 410 (expired token) surfaces
   as an explicit resync — cursor cleared, one clean full enumeration, never silent
   loss. Auth: OAuth2 client credentials via `golang.org/x/oauth2/clientcredentials`
   (already in the module graph — no new dependency).
2. **Model:** Entities kind `device` (domain data, not frozen §2 vocabulary);
   identity key `graph.id` (the immutable directory object id — present on
   removals, unlike `deviceId`); label `graph.name`. Facets owned by the syncer
   (§2.1, registered before first write): `device.identity`, `device.os`,
   `device.state`. Only populated facets project (§1.1: no speculative typing).
3. **Dev stand-in: graphsim** (`core/cmd/graphsim` + importable handler) — the
   vcsim posture: just enough token + `/devices/delta` protocol (paging, delta
   tokens, removals, 410) for the Syncer to run its real code paths, plus `_sim`
   mutation hooks for e2e. Never shipped, never load-bearing (§1.5).
   **Follow-up:** validation against a real Entra tenant before this Connector is
   called production-ready; managed-device (Intune) enrichment is a separate,
   later capability on the same Source.
4. **Credentials via env** (`STRATT_MSGRAPH_*`) — the same stub posture as vCenter;
   brokering Syncer credentials through CredentialRefs is the standing ADR-0009
   follow-up.
5. **EC2 cloud-instance Syncer** (`core/internal/connectors/awsec2`, commit B):
   `DescribeInstances` paginated. EC2 exposes no delta feed, so each cycle is an
   honest **poll full-sync + tombstone-absent** on `aws.instanceId` (cursor stays
   empty — recorded, not hidden). Entities kind `instance`; facets
   `instance.compute` / `instance.network` / `instance.state`. Dev stand-in:
   LocalStack in compose. Dependencies scouted before adoption (see amendment).

## Consequences

- Multi-Source graph is real: `vm`/`host` (vCenter), `device` (Graph), `instance`
  (EC2) coexist with per-namespace single-writer Facets; identity keys are
  per-scheme (`vcenter.uuid`, `graph.id`, `aws.instanceId`) — cross-Source
  correlation happens only through genuinely shared identity (e.g. `dns.fqdn`),
  never guessed (§1.2).
- The delta-protocol integration test drives the real Syncer against the sim
  in-process (paging, rename, removal, restart-resume, 410-resync) — the
  swap-fidelity proof for a future real-tenant run.

## Implementation notes (commit B — EC2)

(appended when commit B lands)
