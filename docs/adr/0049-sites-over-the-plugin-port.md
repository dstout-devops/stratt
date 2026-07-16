# ADR 0049 — Sites over the plugin port: the agent as an authenticated transport relay, never a governor

- **Status:** **Proposed** — extends remote-Site execution (ADR-0032, today only the in-tree pod path) to the
  full plugin-port verb surface (Observe / Plan / Apply / Destroy / Invoke), so a device that must act at the
  edge (ansible against isolated hosts, cert issuance from a leaf, on-prem AD) can run over the sovereign port
  instead of an in-tree pod. One charter-guardian pass (REWORK → folded; see Reviews). Supersedes ADR-0047's
  "Sites deferred (hub-only v1)" and the `PluginActuatorSiteUnsupported` guard.
- **Date:** 2026-07-16
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.2 (projections / two write paths, single writer), §1.5 (schemas pinned,
  verify-don't-trust), §1.6 (one identity / authz / audit), §1.8 (never hide failure), §2.1 (ownership from the
  grant), §2.5 (secrets brokered, never held). Builds on ADR-0032 (Sites/NATS transport), ADR-0044/0045 (Cells
  + the DB home gate), ADR-0046 (the sovereign port + content-blindness), ADR-0047 (the v1 wire surface, §6
  ItemResult fold, §8 plan-as-artifact).

## Context

The port host (`core/internal/pluginhost`) is documented as *"the SOLE graph writer… governs on the operator
Grant, not on anything the plugin asserts."* It runs at the hub: grant-match, the identity/facet/label gates,
the confused-deputy target gate, the core-side `Succeeded` fold, per-verb provenance stamping, and (ADR-0047
§8) content-addressing the saved plan. Today `executePlugin`/`PlanStep`/`InvokeRaw` **refuse** a non-local Site
(`PluginActuatorSiteUnsupported`, `orchestrate.go`), so a plugin can only run hub-local. That blocks the whole
*act-at-the-edge* class — the flagship being ansible, whose hosts are typically reachable only from a leaf.

Sites (ADR-0032) already run remote work as wire-devices: the hub prepares a `RemoteSafe` JobSpec, `sitegw`
publishes it over the outbound NATS leaf, and `stratt-agent` runs it via a named in-agent Interpreter,
resolving credential **pointers** against its own Site-local broker. The charter's substrate framing (ADR-0046
Part 3) is explicit that the end-state is one dispatch model: *"extend the Site model to all devices."* This ADR
draws that boundary.

**The tempting-but-wrong design (rejected).** The obvious move — relocate the `pluginhost.Host` into
`stratt-agent` and run the governor at the Site, then have the hub "project with provenance" what the Site
streams back — **inverts the one trust boundary the port rests on.** The host would run grant-match, the gates,
the fold, and provenance selection inside the least-trusted, NAT-isolated, possibly-compromised Site zone,
while the hub blindly stamps core Run/Syncer provenance on the result. A compromised agent could then forge
provenance for any Source whose grant it can present — an estate-wide §1.2/§2.1 confused-deputy. The governor
must not live at the Site.

## Decision

**The agent is an authenticated transport relay, never a governor. Governance and provenance stamping stay
hub-side; the plugin's raw, provenance-free wire shapes are tunnelled over the existing outbound NATS leaf.**

### The shape

1. **The plugin runs at the Site** (co-located with the estate it manages), serving the port over localhost
   gRPC to the agent — exactly as a hub-local plugin serves the hub today.
2. **The agent proxies that gRPC stream over NATS.** It bridges bytes bidirectionally between the localhost
   plugin and a per-connection NATS channel; it parses nothing and governs nothing. This is **not** the
   rejected inbound-dial (Model B): the agent still connects *out* on its leaf, and NATS decouples
   connection-initiation from message-direction — so the **hub initiates** the virtual gRPC connection (it is
   the client, as always) over subjects the already-connected agent bridges to localhost. No inbound path to
   the Site is ever opened.
3. **The hub's `pluginhost.Host` is unchanged.** It dials "the plugin" through a NATS-backed transport instead
   of localhost/mTLS; every method call (Observe/Plan/Apply/Destroy/Invoke) and every governance step runs at
   the hub over the plugin's **raw** responses. Only the plugin transport gets longer.
4. **`executePlugin`/`PlanStep`/`InvokeRaw` drop the hub-only guard** and select the NATS-backed transport when
   the Step's execution locus is a remote Site; local Steps dial as today.

### The invariants (folding the guardian's must-fixes)

- **V1 — the Grant never leaves the hub (§1.2/§2.1).** The Site→hub wire carries the **same ungoverned,
  provenance-free shapes the plugin emits** (`ObservedEntity`/`ItemResult`/`DerivedContract` — note
  `ObservedEntity` carries *no* provenance field; that structural property is load-bearing, keep it). The hub
  runs the existing `pluginhost` gating on those raw shapes **before** it projects, and validates the relayed
  `Manifest` against the hub-held grant hub-side. The agent registers nothing, matches no grant, stamps no
  provenance.
- **V2 — the plan is hashed and stored at the hub, never at the Site (§1.5, ADR-0047 §8).** The Plan **bytes**
  transit to the hub; the **core** computes the sha256, encrypts, and write-once-stores them in `planstore`;
  `VerifyPinnedPlan` re-hashes hub-side at the Apply boundary exactly as today. A Site hashing its own plan and
  echoing the digest is plugin-echo-trust — the confused-deputy hole ADR-0047 §8 pass 3 already rejected.
  **§2.5 asymmetry to state plainly:** unlike the pod path (nothing sensitive crosses hub→Site), the plan bytes
  are secret-bearing (tofu plans embed resolved values) and now cross **Site→hub** — so the leaf transport
  MUST be TLS and the bytes MUST never persist outside the core's encrypted store (transient core-memory
  transit for hashing is accepted, per ADR-0047 §8).
- **V3 — the confused-deputy target gate + the `Succeeded` fold bind hub-side (§1.8, ADR-0047 §6).** Both
  depend on the **hub-computed** resolved View set from `RunAgainstView` (the authz chokepoint). Because
  `ApplyRaw` runs hub-side over the raw `ItemResult` stream (V1), a Site's status for a target outside the
  Step's resolved set is rejected as a confused deputy, and a plugin's terminal `ok` never overrides a per-
  target FAILED — identically to hub-local execution.
- **F1 — the trust bound is the named invariant (§1.6).** The plugin's localhost dial has no end-to-end auth to
  the hub, so `manifest.plugin_id` is a string the Site controls. Either the plugin authenticates
  **end-to-end to the hub** (a plugin-held key/token the agent cannot forge, tunnelled through the relay), or
  the trust model is explicitly bounded (below). Because all gates run hub-side (V1/V3), a compromised Site can
  forge only within the authority its own grants + the Step's resolved View scope already delegate — never
  estate-wide, never cross-Source.
- **F2 — the Envelope name-list is hub-side selection, not a Site capability (§2.5).** The credential
  use-check (the oracle closure) runs hub-side before dispatch, so only authorized names cross. But a
  compromised Site-local plugin can resolve **any name present in its own broker**, so the real authority
  boundary at the Site is the broker's contents — Site-local, operator-provisioned per Site, never estate-wide.
  This is contained and strictly better than shipping material; it is stated, not hidden.
- **F3 — write-back projects at the Source's home-Cell hub (§1.2, ADR-0044/0045).** The DB home gate
  (`enforce_write_path`, migration 00032) rejects a projection whose Source is homed on a peer Cell, so
  single-writer fails **closed** regardless of where the plugin ran. The relay MUST route write-back projection
  to the home-Cell hub via the ADR-0044 write-home-forwarding path; projecting at an arbitrary hub turns every
  cross-Cell Site write into a rejection. Verified, not assumed.

### The highest-risk invariant (pin this)

> The relay never lets a Site cause a core-stamped write outside the authority the hub-held Grant plus the
> Step's resolved View scope already delegate to that Site's plugin. Governance (grant-match, identity/facet/
> label gating, confused-deputy target gate, `Succeeded` fold, plan hashing) and provenance stamping stay
> hub-side; the agent is an authenticated transport relay for the plugin's raw, provenance-free wire shapes —
> never a trusted governor. Site compromise is bounded to delegated authority, never estate-wide provenance
> forge.

## Consequences

- **Positive:** the port and Sites become one abstraction; the *act-at-the-edge* class (ansible, edge cert
  issuance, on-prem AD) runs over the port; `host.go` is unchanged (the rework is narrowly *where the plugin
  transport runs*, not where the host runs); credential-names-only is a strictly better §2.5 story than the
  pod path (no `RemoteSafe` Env-material limitation — the very thing that hub-pins opentofu today).
- **Negative / cost:** a NATS-backed gRPC transport (a virtual, hub-initiated connection bridged by the agent)
  is real new plumbing; secret-bearing plan bytes now cross Site→hub (mitigated: TLS leaf + core-only encrypted
  store, V2); end-to-end plugin auth (F1) is the open hardening item — until it lands, the trust model is the
  bounded one above, which the ADR makes explicit rather than silent.
- **Supersedes:** ADR-0047's "Sites deferred (hub-only v1)"; the `PluginActuatorSiteUnsupported` guard; and
  extends ADR-0032's pod-only Site model to all devices.

## Alternatives considered

- **Model A — run the `pluginhost.Host` at the Site.** REWORK'd out: relocates the governor into the untrusted
  zone; the hub would stamp provenance on ungoverned Site assertions (§1.2/§2.1 forge). Rejected.
- **Model B — the hub dials a Site-local plugin's gRPC directly.** Rejected on sight: Sites are behind
  NAT/firewall; the pull/leaf topology exists precisely to avoid inbound. (The chosen model keeps the outbound
  leaf and lets NATS carry the hub-initiated virtual connection.)

## Reviews

- **charter-guardian, pass 1 — governance across the relay (2026-07-16): REWORK → folded.** Model A moves the
  governor to the Site (§1.2/§2.1 provenance forge, V1); plan-hash-at-Site is plugin-echo-trust (§1.5, V2); the
  target gate + fold only bind hub-side (§1.8, V3). Corrected to the transport-relay model (agent governs
  nothing; raw provenance-free shapes; all gates + provenance + plan hashing hub-side). Flags folded: end-to-end
  plugin auth vs the bounded-trust invariant (F1), Envelope name-list is hub-side selection not a Site
  capability (F2), write-back projects at the Source's home-Cell hub (F3). Highest-risk invariant pinned above.
