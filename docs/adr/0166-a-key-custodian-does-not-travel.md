# ADR 0166 — A key custodian does not travel

- **Status:** **Proposed** (2026-08-04, steward). Charter review by hand — this session's rules bar
  the subagent; §1.8/§2.5 answered inline. **No new dependency, no port change.**
- **Date:** 2026-08-04
- **Deciders:** steward
- **Charter sections:** §1.8 (never hide diagnosis), §2.5 (credentials brokered — core never holds
  material)
- **Scopes ADR-0100** (KeyCustodian) and **ADR-0164** (MACVerifier) at the Site boundary, and
  **corrects a claim ADR-0164 made about them**.

## Context

ADR-0164 recorded, while building the MACVerifier port verb:

> **`WrapKey`/`UnwrapKey` are not served over the site relay.** The client has them and the routing
> constants exist, but the far-end switch answers "unknown method". That is a pre-existing gap in
> ADR-0100's cross-DC story.

**That characterization was wrong, and correcting it is most of this ADR.** Reading the call graph
rather than the constant list: `siterelay.NewClient` is constructed in exactly one place —
`orchestrate.go`, building a `pluginhost` to dispatch plugin work to a Site under a grant. The
custodian is built separately in `buildKeyCustodian` with its own direct gRPC dial, and the MAC
verifier the same way in `buildMACVerifier`.

**No shipping path routes a custodian call through a relay.** The three methods on
`siterelay.Client` exist to satisfy `pluginv1.PluginServiceClient` and nothing else. They are not a
broken capability; they are interface padding that would fail with a shrug if anyone made them
reachable.

### Why they should stay unreachable, which is the actual decision

**`WrapKey`/`UnwrapKey` would send a DEK across the link.** ADR-0100's whole argument is that the KEK
never leaves the KMS — the DEK travels to the plugin to be wrapped. That is fine when the plugin is a
local pod. Over a relay to an edge Site it means the key that encrypts hub state crosses the WAN to
be wrapped by somebody else's KMS, which inverts the property the port exists to provide.

There is also no use case pulling the other way. Per-Cell key sovereignty (ADR-0100) is served by
each **Cell** running its own control plane with its own local custodian — a Cell is a control plane,
a **Site** is an execution locus with an agent. A Site has no state to encrypt with a hub DEK.

**`VerifyMAC` carries no material** (a body, a signature, a key coordinate) and would be harmless to
relay — but ingest terminates at the hub, so there is no caller either. Serving it would be building
a road to a place nobody goes.

## Decision

### D1 — The custodian verbs are REFUSED at the Site boundary, with a reason

Both ends refuse, and the message says why rather than that something is missing:

- the **client** fails fast, without a round trip — the call is wrong before it reaches the wire;
- the **server** names these verbs explicitly instead of letting them fall into `unknown method`.

**"Unknown method" is the failure this fixes.** It reads like a version skew or a typo and sends the
next engineer looking for a missing case statement. What is actually true — *a custodian does not
travel, on purpose* — is a design property, and §1.8 says the abstraction must not hide the reason a
thing failed.

### D2 — The client stubs stay, because the interface requires them

Deleting them is not available: `siterelay.Client` must satisfy `PluginServiceClient`. What changes
is that they stop pretending. A stub that returns a clear refusal is honest; a stub wired to a
routing constant implies a capability that was never built.

### D3 — If a Site ever needs custody, that is a NEW decision, not this switch case

The shape it would take — a Site-local custodian the Site's own agent consumes, never the hub's DEK
crossing the link — is a different design from "serve `WrapKey` over the relay". Booking it here
means the next person meets the reasoning instead of a tempting one-line `case`.

## Consequences

- **A confusing runtime error becomes an explanatory one**, for a path nothing currently takes.
- **ADR-0164's claim is corrected in place.** The gap it named is not a gap; recording that matters
  more than the code change, because "ADR-0100's cross-DC story is broken" would otherwise sit in the
  record as a known defect that nobody could reproduce.
- **No behaviour changes for anything that ships**, and the tests say so rather than assuming it.

## Verification

- unit: each of the three verbs refuses at the client **without dialling**, and the error names the
  verb and the reason;
- unit: the relay server refuses them by name rather than via the generic `unknown method` arm;
- unit: **the eight verbs that DO travel still travel** — the regression that matters, since this
  change edits the switch every dispatched Step goes through.
