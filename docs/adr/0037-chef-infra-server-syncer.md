# ADR 0037 — Chef Infra Server node-API Syncer (first config-mgmt SoR ingest)

- **Status:** Accepted (Commit 1 — go-chef signing transport + signature-verifying chefsim;
  Commit 2 — Syncer + normalizer + wiring + example View)
- **Date:** 2026-07-14
- **Deciders:** Project steward (dstout)
- **Charter sections:** §2.2 (Source / Connector / Syncer / Normalizer), §1.2 (projections, never a
  second truth), §1.1 (type the seams — curated Facets), §1.4 (boring spine, few boring deps), §1.7
  (Evergreen — a dependency's upgrade track record is evaluated before adoption), §2.5 (CredentialRef;
  material never persists), §7.6 (strangler-fig), §0 (market context); ADR-0007 (Syncer SDK + dev
  harness), ADR-0014 (connector breadth — msgraph/EC2, the vendor-native-SDK precedent), ADR-0009
  (CredentialRef brokering for Syncers, the shared follow-up)

## Context

The Phase-3 connector board was reprioritized: a **config-mgmt Syncer track** (Chef → OpenVox/PuppetDB
→ Salt), inbound-data-first, now precedes the Jamf/ConfigMgr MDM connectors (Jamf/Intune stay
Connectors; MDM-protocol impl is a permanent non-goal). **Chef goes first** on three aligned grounds:
it is the org's internal standard with **all devices auto-enrolled** (a Chef Syncer projects the whole
estate into the graph on first sync — the best dogfood of §1.2 project-then-observe at real scale); it
**dissolves the "we already pay for Chef" objection** to the ~15% already on AAP (ride Chef's node
data, drop the AAP per-seat license, keep the clean intent/drift model open — §1.3's no-gated-tier is
the literal answer); and Chef's node API is **open (Apache-2.0)**.

**Not the first Syncer.** The pattern is fully established (vcenter/msgraph/awsec2/certissuer);
`connectors/msgraph/` is a near-exact template (native REST, interval poll, Normalizer → Projector).
This is the first **on-prem config-mgmt system-of-record** Syncer. No new engine, no migration, no new
graph write-path.

**EOL framing (verified, charter §0).** Chef Infra Server (OSS) is **EOL Nov 2026**, consolidating into
**Chef 360** (SaaS / Self-Managed); Chef Infra Client, ohai, and InSpec remain maintained OSS, and
**CINC Server** is an API-compatible OSS rebuild. The org runs **legacy Chef 15**. The node-API surface
read here is stable across legacy Infra Server, Chef 360 Self-Managed, and CINC — so this Syncer is the
**strangler-fig capture wedge** (§7.6): lift the EOL-track estate into the graph now, and Stratt keeps
the estate wherever the org lands. The EOL is a reason the wedge is *urgent*, not a reason to skimp.

## Decision

1. **A Syncer on the shipped spine (§2.2/§1.2).** `connectors/chef/` follows the msgraph shape:
   `Config` + `Register` (RegisterSource kind `chef` + RegisterFacetOwner) + interval `Run` + `Sync`.
   Chef has **no change feed**, so every cycle is a **full enumeration** — list node names, `GET` each,
   normalize, `UpsertEntities`, then `TombstoneAbsent("chef.node.name", seen)`. All writes go through
   `graph.Projector.NormalizerProjector()` and nowhere else; `ErrIdentityConflict` is logged and
   skipped (§1.2). A single node that fails to fetch/normalize is skipped, never fatal (§1.8).

2. **Auth via `github.com/go-chef/chef`, not hand-rolled (§1.4/§1.7 — dependency-scout RECOMMEND).**
   Chef's Mixlib signed-header auth needs raw RSA **private-key encryption** for legacy sign protocols
   1.0/1.1 — `crypto/rsa` deliberately does not expose it, so a correct implementation means
   hand-writing PKCS#1 padding + modexp: a crypto footgun. Legacy Chef 15 negotiates these protocols,
   so 1.0/1.1 cannot be assumed away. The library encapsulates 1.0/1.1/1.3, is **Apache-2.0**, and
   matches this repo's precedent of taking the vendor-native client for nontrivial auth (aws-sdk-go-v2
   for SigV4, govmomi for vim25). `AuthVersion` defaults to `1.0` and is env-overridable.

3. **Dormant-dependency guard (§1.7).** go-chef is dormant (~2yr, single-maintainer, low bus factor) —
   the fossilization pattern §1.7 warns of. Mitigations: **pin `v0.30.1`** (N-1 `v0.29.0`); a scheduled
   staleness check should flag both version drift *and* >12-months-without-a-tag; and an **in-tree fork
   contingency** is on the table if it fully abandons (the auth code is ~150 well-understood LOC). Its
   `ctdk/goiardi` requirement is test-only and does **not** enter our build graph (`go mod why`
   confirms). Use `github.com/go-chef/chef` (community), never the archived `github.com/chef/go-chef`.

4. **Signature-verifying `chefsim` — the harness is first-class.** This is an **out-of-network OSS
   build**; we cannot test against the production Chef estate. So `chefsim` (the graphsim/awxsim
   posture, §1.5 test double) serves the node API **and cryptographically verifies the Mixlib
   signature** on every request by deterministic re-sign — reusing go-chef's own exported primitives
   (`AuthConfig.SignatureContent`, `GenerateSignature`, `Base64BlockEncode`, `HashStr`), **no
   reimplemented crypto**. Chef sign v1.0 (RSA private-encrypt) and v1.3 (PKCS1v15) are both
   deterministic, so identical bytes prove the client signed the correct canonical request with the
   correct key; a wrong key or a tampered signed header → 401. This proves the go-chef path end-to-end
   with no real server.

5. **Identity + Facets curated charter-down (§1.1).** Entity `Kind: host` (aligns with vcenter).
   Identity keys: `chef.node.name` (always) **+ `dns.fqdn`** from ohai, so a Chef-sourced host
   correlates with the same host from vcenter/msgraph by identity-key overlap (the established pattern
   — not shared Facet namespaces). Selectable, source-attributable data (e.g. `environment`) rides the
   **source-scoped facets**, NOT the shared Entity label bag — a whole-set last-writer projection that
   clobbers across two config-mgmt Sources correlating onto one host (§2.4; corrected in ADR-0038). The
   smart-inventory View selects on `chef.node.identity.environment`. Facets are a **curated** map of ohai automatic
   attributes onto **source-scoped** observed Facets (the msgraph `device.*` precedent): `chef.node.identity`,
   `chef.node.os`, `chef.node.network`. Source-scoped (not a shared `node.*`) because §2.1's
   one-owner-per-namespace registry forbids a second config-mgmt Syncer co-owning them and a shared
   namespace would be last-writer-wins across Sources (the §2.4 implicit-precedence ban); cross-source
   hosts unify via the `dns.fqdn` identity key instead (this source-scoping was settled in ADR-0038).
   These are **left uncovered by a pinned schema** until a shipping
   Contract/Baseline demands one (§1.1 — no speculative schemas), exactly as msgraph's `device.*`.

## Charter posture

- **§1.2** Chef stays the authoritative SoR; the graph is a rebuildable read-model written only through
  the Normalizer path, provenance-stamped; full-enumeration tombstones handle disappearance. Read-only
  ingest — **not a writable CMDB** (we feed CMDBs, we don't become one).
- **§1.1** Facets curated, not dumped; uncovered until demanded. The example View
  (`deploy/dev/examples/chef/views/`) is the §1.1 consumer of the projection.
- **§1.4/§1.7** one narrow, boring, Apache-2.0 vendor client for a hazardous auth surface; pinned +
  N-1 + staleness guard + fork contingency.
- **§2.5** the PEM signing key is read from a mounted file (or inline env for dev), used only to sign,
  never persisted to the graph or logs. CredentialRef brokering for Syncers is the shared ADR-0009
  follow-up (vcenter/msgraph carry the same stub today).
- **§2 vocabulary** Kind `host`, identity scheme `chef.node.name`, labels `chef.*` — data, not Named
  Kinds. The banned `inventory` never appears: Chef says "node"; the AAP-inventory concept maps to a
  **View**.

## Alternatives considered

- **Hand-roll a protocol-1.3-only signer (stdlib).** Clean for 1.3 (`SignPKCS1v15`+SHA-256), but the
  org's **legacy Chef 15** likely negotiates 1.0/1.1, whose raw private-encrypt is the footgun; a
  1.3-only signer would silently fail against the actual servers. Rejected in favor of the
  all-protocol library.
- **Vendor go-chef in-tree from day one.** Deferred to the fork *contingency* — pinning + the staleness
  guard is the lighter posture while upstream is merely dormant, not abandoned.
- **A per-node change-feed / partial-search-driven cursor.** Chef has no change feed; partial-search is
  an efficiency optimization (below), not a delta feed. Full enumeration + tombstone is the honest fit.
- **Shared observational Facet namespaces (e.g. writing `os.kernel`).** Rejected: two Syncers owning
  one namespace is a §2.1 registration error, and cross-source correlation is already handled by
  identity keys. Connector-namespaced Facets + `dns.fqdn` correlation is the established pattern.

## Known behavior

- **Entity labels are a whole-set, last-writer projection** (the Projector replaces an Entity's entire
  label blob per observation; identity keys and per-namespace Facets accumulate, labels do not). So
  config-mgmt connectors do **not** write projected, source-attributable data into the shared label bag
  — it would clobber across two Sources correlating onto one host (§2.4). Selectable data lives in the
  source-scoped facets instead (corrected under ADR-0038 when Puppet made the collision reachable). A
  general per-key/per-writer Entity-**label** ownership model remains a platform deferral.

## Honest deferrals

- Chef **partial-search** bulk fetch (v1 does list + get-each — O(N) requests; fine at dogfood scale,
  flagged for large estates); **environments/roles** (v1 projects `environment` into the identity
  facet; roles/expanded `automatic.roles` deferred — needs facet array-membership View selection);
  **policyfiles / data bags / cookbook-version** Facets; **CredentialRef brokering for Syncers** (shared gap);
  pinning `chef.node.*` Facet schemas once a Baseline demands them; a **real-Chef e2e** (out-of-network
  build — chefsim is the proof surface, and the store/normalizer/tombstone/correlation paths are
  proven by unit + real-DB integration tests); the **OpenVox/PuppetDB second connector** — the
  deliberate generality test that the Normalizer contract is not Chef-shaped.

## Consequences

The estate's Chef-managed fleet becomes one typed, provenance-stamped, View-queryable graph on first
sync — the migration-capture wedge for an EOL-track SoR, and the first proof that Stratt's Syncer spine
extends to on-prem config-management sources. No new engine, no migration, no graph write-path change —
a new connector package, one scouted dependency behind a hazardous-auth surface, and a
signature-verifying harness that stands in for a production Chef server we cannot reach.
