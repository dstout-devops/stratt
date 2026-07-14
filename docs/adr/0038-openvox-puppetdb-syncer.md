# ADR 0038 — OpenVox/PuppetDB node Syncer + source-scoped config-mgmt facets

- **Status:** Accepted (Commit 1 — source-scope Chef facets `node.*` → `chef.node.*`; Commit 2 —
  OpenVox/PuppetDB Syncer)
- **Date:** 2026-07-14
- **Deciders:** Project steward (dstout)
- **Charter sections:** §2.2 (Source/Connector/Syncer/Normalizer), §2.1 (facet-ownership registry —
  one owner per namespace), §2.4 (no implicit precedence — the anti-GPO axiom), §1.2 (projections,
  never a second truth; enforced in the data layer), §1.1 (curated Facets), §1.4 (boring spine, few
  boring deps), §2.5 (credential material never persists), §0 (Puppet → OpenVox); ADR-0037 (the Chef
  Syncer this mirrors and corrects), ADR-0014 (connector-breadth precedent)

## Context

Second connector in the config-mgmt Syncer track (Chef ✓ → **Puppet/OpenVox** → Salt). Its explicit
purpose is a **generality test**: prove the Chef Syncer's Normalizer discipline (the `graph.EntityUpsert`
shape, `Kind: host`, `dns.fqdn` correlation, curated-facts mapping) is **not Chef-shaped**. Puppet is
**not** in the org's estate (they run Chef), so this connector serves **OSS product breadth** and the
abstraction proof — it is **harness-only** (`puppetdbsim` + an mTLS round-trip), never dogfooded
against a real internal server.

**OpenVox (verified, 2026).** Perforce moved new Puppet binaries to a EULA-gated channel and declared
open-source Puppet releases EOL; the source stayed Apache-2.0 and the community forked to **OpenVox**
(Vox Pupuli). **OpenVoxDB** is a maintained, Apache-2.0, API-compatible fork of PuppetDB (v8.14.1),
using the same `/pdb/query/v4` API. Upstream PuppetDB is also still Apache-2.0. So the Syncer targets
"a PuppetDB-compatible v4 query API" with a configurable base URL — **never a hardcoded vendor** — and
works against PuppetDB or OpenVoxDB unchanged.

## Decision

1. **The generality test surfaced a real constraint, and the charter resolved it (Commit 1).** Two
   config-mgmt Syncers observing the same host facts **cannot share** the `node.*` Facet namespaces:
   `graph.facet_owner.namespace` is a `PRIMARY KEY` (one owner, by construction), and a shared
   namespace would be **last-writer-wins across Sources** — the implicit precedence §2.4 forbids and
   the §1.2 data-layer rule makes structurally impossible. **Resolution: facets are source-scoped.**
   Chef's `node.*` was renamed to `chef.node.*` (consumer-free — nothing read it) and Puppet owns
   `puppet.node.*`. The generality test thus *validated* the discipline rather than bending it.

2. **Entities unify across Sources via `dns.fqdn`, facets stay source-scoped (Commit 2).** A Puppet
   host and a Chef host with the same `dns.fqdn` correlate into **one** `host` Entity (identity-key
   overlap in the Projector) carrying **both** `puppet.node.*` and `chef.node.*` facets, each with its
   own Source Provenance — cross-source unification done right: **unify entities, never overwrite
   facts**. A unified/normalized cross-source fact *query* layer (e.g. "`os.family` across all
   Sources") is a future normalization concern (a normalizer-of-normalizers or a Blueprint), **not**
   two Syncers fighting one namespace. The integration test proves the unification directly.

3. **A Syncer on the shipped spine, mirroring `connectors/chef/`.** `Config` + `Register`
   (RegisterSource kind `puppet` + RegisterFacetOwner) + interval `Run` + `Sync`. `Sync` pages
   **`GET /pdb/query/v4/inventory`** (one row per node with the full fact set inline —
   `order_by=[{"field":"certname"}]`, `limit`/`offset`, `include_total` → `X-Records`), normalizes,
   `UpsertEntities` via `NormalizerProjector()` only, then
   `TombstoneAbsent("puppet.certname", seen)`. The `inventory` endpoint **fixes Chef's list-get-each
   O(N)** deferral — one query per page. ("inventory" appears **only** as the vendor HTTP path — never
   a Stratt identifier; §2 vocab.)

4. **Auth is stdlib mTLS — zero new dependency (§1.4).** PuppetDB validates a client certificate
   against the Puppet CA + a certificate-allowlist. `crypto/tls` handles it natively:
   `tls.LoadX509KeyPair` + a `RootCAs` pool from the CA PEM, on an `http.Transport`. Plain HTTP is used
   for the localhost dev listener. No third-party lib — a deliberate contrast with Chef (whose Mixlib
   RSA signing forced `go-chef`), and itself the point of the generality test: **the abstraction
   generalizes, not a vendor lib.** PE RBAC `X-Authentication` tokens are PE-only and out of scope.

5. **Identity + Facets (charter-down, §1.1); NO Entity labels.** `Kind: host`; identity
   `puppet.certname` (always) + `dns.fqdn` from `networking.fqdn` (the correlation key). Selectable,
   source-attributable data (`environment`) rides the **source-scoped facets**, **not** the shared
   Entity `labels` bag — labels are a whole-set last-writer projection that would clobber across two
   Sources correlating onto one host (§2.4 implicit precedence; the charter-guardian must-fix, see
   below). Curated structured-Facter facets: `puppet.node.identity` (os name/family/release/
   architecture, environment), `puppet.node.os` (kernel/kernelrelease/kernelversion),
   `puppet.node.network` (fqdn/ip/ip6/mac) — the `chef.node.*` mirror, uncovered until a Contract
   demands a schema. The example View selects on `puppet.node.identity.environment`, and the Chef
   connector was moved off labels the same way. A general per-key Entity-label ownership model is a
   platform deferral.

## Charter posture

- **§2.1/§2.4** source-scoped facets: one owner per namespace, no cross-source implicit precedence.
  The registry's one-owner invariant was upheld, not relaxed.
- **§1.2** read-only projection; PuppetDB/OpenVox stays the authoritative SoR; `dns.fqdn` unifies
  entities; full-enumeration tombstones handle disappearance. Not a writable CMDB.
- **§1.4** zero new dependency — stdlib mTLS keeps the boring spine boring; the Chef→Puppet jump proved
  the Normalizer contract (not a vendor lib) is what generalizes.
- **§2.5** cert/key material read from mounted files, never persisted or logged; CredentialRef
  brokering for Syncers is the shared ADR-0009 follow-up.
- **§2 vocabulary** Kind `host`, identity `puppet.certname` + `dns.fqdn`, facets `puppet.*` — data; the
  banned `inventory` appears only as the vendor endpoint/payload names (the `/inventory` HTTP path and
  the `inventoryEntry`/`fetchInventory` transport DTOs), never a Stratt data-model identifier.

## Alternatives considered

- **Shared `node.*` with a multi-owner registry.** Rejected as charter-hostile: it would relax the
  §2.1 one-owner invariant and reintroduce last-writer-wins across Sources — exactly the §2.4 implicit
  precedence the data layer is built to forbid.
- **Keep Chef on `node.*`, Puppet on `puppet.node.*`.** Rejected: asymmetric, a naming wart future
  connectors would inherit. The Chef rename was consumer-free, so symmetry was nearly free.
- **A vendor Go PuppetDB client.** Unnecessary — the query API is plain REST and auth is stdlib mTLS;
  no hazardous surface to encapsulate (unlike Chef). Zero deps is the boring-spine win.
- **The `nodes` + `facts` endpoints (join client-side).** Rejected: `/inventory` returns nodes with
  full facts inline in one query — simpler and cheaper.

## Reviews

- **charter-guardian: CHANGES-REQUIRED → resolved in-slice.** Must-fix: the config-mgmt normalizers
  wrote projected data (`environment`, roles) into the shared Entity `labels` bag, which the Projector
  replaces whole-set per observation — so a Chef+Puppet co-managed host (this slice's celebrated
  unification) would clobber its labels across Sources each cycle (§2.4 implicit precedence). Fixed by
  moving selectable data to the source-scoped facets and selecting Views on facets; both connectors and
  their example Views were updated, and the earlier "benign" framing was corrected here and in ADR-0037.
- **vocabulary-linter: CLEAN** (`inventory` only in vendor endpoint/DTO names; `puppet.*`/`host` as data).
- **No dependency-scout** — zero new dependencies (stdlib mTLS).

## Honest deferrals

- Puppet **classes/roles** (needs the catalogs/resources endpoint, not `inventory`; and facet
  array-membership View selection); environments/reports as **Relations**; a **unified/normalized
  cross-source fact layer** (the deferred cross-Source `os.family` query — a normalizer-of-normalizers
  or Blueprint, never shared namespaces); **PE RBAC-token** auth (PE-only); a **real-OpenVox e2e**
  (harness-only build — `puppetdbsim` + the mTLS round-trip are the proof surfaces); pinning
  `puppet.node.*` schemas once a Contract demands them.
- **Cross-source Entity liveness (charter-guardian flag).** `TombstoneAbsent` soft-deletes the whole
  Entity by identity scheme, so a host co-managed by Chef and Puppet that Puppet stops reporting is
  tombstoned by the Puppet cycle (Chef facets included) and resurrected by the next Chef upsert
  (`deleted_at = NULL`). Entity liveness is thus a cross-source last-writer effect. Not a hard violation
  (the graph is rebuildable; it mirrors accepted ADR-0037 behavior), but per-source liveness is a
  documented platform deferral.
- A general per-key/per-writer **Entity-label** ownership model (the deeper fix for the label bag).

## Consequences

Two config-mgmt Sources (Chef, Puppet/OpenVox) now project into one typed graph, and a host managed by
both is a single Entity with both Sources' facts side-by-side, each provenance-stamped — the
cross-source unified estate the thesis promises, achieved without a second truth. The Normalizer
discipline is proven vendor-neutral (different Source, different auth, different query API, same shape),
and the config-mgmt track's Facet-namespacing convention (`<source>.node.*`, entities unified by
`dns.fqdn`) is now established for Salt and beyond. No new engine, no migration, no new dependency.
