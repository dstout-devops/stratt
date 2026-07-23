# ADR 0100 — The KeyCustodian capability: envelope encryption with a self-sufficient local floor and optional, transport-plural KMS providers

- **Status:** **Accepted** (2026-07-23, steward) — charter-guardian **PASS-WITH-CHANGES** ("textbook
  §1.4/§1.5"). Folded: (1) the local floor is **in-core (compiled-in, in-process), NEVER over the
  plugin port** — the port engages only for a non-local provider (the load-bearing self-sufficiency
  check); (2) renamed `Sealer`→**`KeyCustodian`** to avoid the audit-hash-chain "Sealer" collision
  (ADR-0034/0040/0044); (3) reframed as a **NEW** capability class *extending* the ADR-0046 reserved
  set (SecretBroker already got a backend in 0094 — this is not "the first"); ADR-0046's enumeration is
  amended to add it; (4) the KeyCustodian contract schema is **pinned + hash-verified** like every
  plugin class (§1.5); (5) the **domain→custodian binding is Git/CaC desired state** (§1.2), so a
  rebuilt/cut-off Cell reconstructs its key topology deterministically; (6) partition-survival is
  guaranteed for **local-floor domains** only — agent-cache/replicas *reduce* (not eliminate)
  cold-start coupling for KMS-backed domains; (7) admin ops (rotate/rewrap/bind) are agent-native +
  diagnosable (§1.6/§1.8). vocabulary-linter **PASS**.
- **Date:** 2026-07-23
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.4 (boring spine, pluggable everything — the spine encrypts itself with ZERO
  external services; a KMS is breadth, never spine) · §1.5 (sovereign contracts, multiple transports)
  · §1.3 (rug-pull-proof by structure — no crypto lock-in; always eject-able to local) · §1.2 (desired
  state in Git — the domain→custodian binding) · §2.5 (secrets brokered, never held — the key custody
  upgrade) · defines a **new** capability class (`KeyCustodian`) extending the ADR-0046 reserved set;
  consumes ADR-0094 (SecretBroker) posture; forward-compatible with a future Cell-sovereignty ADR.

## Context

State-at-rest encryption today is **in-core AES-256-GCM** in `core/internal/statebackend` and
`core/internal/planstore` (tofu state keys, saved plans). It works with no external service — good —
but the key handling is hardwired: there is no seam to put key custody in a KMS/HSM, and no seam to
make that **optional**.

The requirement (steward, 2026-07-23), stated as three non-negotiables:

1. **Nothing may be required.** Stratt is the glue that sits *under* everything; the spine must encrypt
   its own state with **no external service at all**. A KMS is an *optional upgrade*, never load-bearing
   for the substrate. (This keeps the spine dependency list at Postgres/NATS/Temporal — §1.4.)
2. **Versatile at DC / distributed-compute scale**, chosen up front to avoid rework. The transport must
   not be pinned to one wire, and the design must minimize how often any external key service is even
   touched.
3. **Sovereignty-forward.** A whole region (e.g. an "India" DC) must eventually be able to run
   self-managed, self-contained, and — cut off entirely — keep operating on its own. F must not
   *preclude* this even though full Cell-sovereignty is a later effort.

**Framing the workload correctly collapses the transport debate.** With envelope encryption the key
service is touched only on: DEK generation (once per encryption context), DEK unwrap (once per process
cold-start, then cached), and rotation (rare). It is **never** on the per-write data path. So this is a
low-rate, availability-and-residency-critical operation — not high-throughput crypto. Optimizing the
wire protocol is optimizing the wrong axis; the levers that matter are **call-rate minimization** and
**reachability/partition-tolerance**.

## Decision

Introduce a **`KeyCustodian` capability** — a **new** capability class *extending* the ADR-0046 reserved
set (`StateStore`, `EventBus`, `SecretBroker`, `DurableExec`, `ArtifactStore`); ADR-0046's enumeration
is amended to list it. It is the core *consuming* a plugin-provided capability (the inverse of the usual
core-drives-plugin flow), and — like every plugin class — its contract schema is **pinned and
hash-verified at registration; schema drift is blocking** (§1.5). Load-bearing properties:

### 1. Envelope encryption — the hot path is ALWAYS local

The core always encrypts state with a local **DEK** (data encryption key) via in-process AES-256-GCM.
A KeyCustodian only ever **wraps/unwraps the DEK** — never the data. Consequences: no per-write network hop;
a KMS outage never halts state writes (the working DEK is cached in memory); "optional" reduces to
"which KEK wraps the DEK" with an identical hot path either way.

### 2. The built-in local-AES floor — self-sufficient, always-on default

Stratt ships a **`localCustodian`** that wraps the DEK under a local KEK (today's mounted
`STRATT_STATE_KEY`, formalized as a KEK). **It is IN-CORE — compiled-in, in-process, and NEVER reached
over the plugin port** (today's `statebackend`/`planstore` in-process AES, preserved exactly). The
KeyCustodian *port/transport* engages **only when a non-local provider is configured for a domain** —
so the spine can encrypt its own state with the gRPC port, NATS, and every plugin process absent. That
is the load-bearing self-sufficiency guarantee: it requires **no external service**, is the default
everywhere including dev, and is the partition-survival floor. **The spine never depends on a KMS — or
on the plugin port — to run.** A total KMS partition degrades a KMS-backed domain to "cannot rotate /
cannot onboard a new context," never "substrate down"; a local-floor domain is wholly unaffected.

### 3. The KeyCustodian is a sovereign CONTRACT, deliberately transport-plural (§1.5)

The contract is `Wrap(domain, dek) → wrappedDEK` / `Unwrap(wrappedDEK) → dek`. The *transport* follows
topology, and the contract is load-bearing on none of them:
- **local provider** (in-cluster) → the direct gRPC plugin port;
- **remote / cross-Site / cross-DC provider** → the **NATS-leaf relay the plugin port already tunnels
  over for remote Sites** (ADR-0049; the Slice-B MF-C relay path) — location-transparent subjects,
  queue-group HA, geo-routing via leaf nodes, all already in the spine;
- **a local custodian-agent cache** (the existing `stratt-agent`) sits in front: the core talks to its
  LOCAL agent (unix socket, always reachable); the agent owns DEK caching + upstream fan-in, driving
  the actual KMS call-rate toward zero and moving the distributed problem to where it belongs.

Call-rate is further minimized by: **the wrapped DEK travels WITH the ciphertext** (stored alongside
state, so unwrap is needed only at cold start), and **regional provider replicas** (OpenBao performance
replicas / KMS multi-region keys) serve unwrap locally.

### 4. Cell/domain-scoped + self-describing envelope — sovereignty-forward, no rework later

Three seams present from day one, trivial in F (one default domain) and load-bearing later:
- **The KeyCustodian is resolved per custody-domain (a Cell), never globally.** A cut-off India Cell runs
  its own provider + its own local floor; partition survival is by construction, not by replication luck.
- **Every `wrappedDEK` is self-describing**: `{domain, providerIdentity, keyVersion, wrappedKey}`. So a
  DEK wrapped by India's KMS is knowably India's — unwrappable only there (residency), partition-
  detectable, key-rotation-aware, and **rewrappable to local** (the §1.3 eject: OpenBao is never a
  one-way door; you can always migrate a domain back to the local floor).
- **The domain→custodian binding is Git/CaC desired state (§1.2), not UI/DB-only config.** The binding
  IS desired state; declaring it in Git is what lets a **rebuilt or cut-off Cell reconstruct its key
  custody topology deterministically** from its own declarations — the concrete seam that makes a
  self-contained Cell recoverable, not just runnable.

Admin ops on custody — **rotate**, **rewrap-to-local** (eject), **bind-domain** — are capabilities
exposed identically to UI/CLI/API/agents under one Principal/authz/audit (§1.6), and their failure
modes (KMS unreachable, wrong-domain DEK, unwrap failure) surface as diagnosable Findings / one-click
descent, never hidden (§1.8) — the same partition-*detectability* the design's thesis relies on.

### 5. OpenBao Transit — one optional provider

The **openbao plugin** provides a KeyCustodian backed by OpenBao Transit (the key never leaves OpenBao — the
§2.5 custody upgrade). It is config-selected per domain; **dev defaults to the local floor** with Transit
opt-in, so the port is exercised against the dev OpenBao without ever requiring it. A future
`aws-kms`/`gcp-kms` plugin implements the identical `KeyCustodian` contract.

## Charter alignment

- **§1.4.** The spine self-encrypts with zero external services (local floor); the KMS is pluggable
  breadth. The required-dependency list stays Postgres/NATS/Temporal.
- **§1.5.** `KeyCustodian` is a sovereign contract with plural transports (gRPC local, NATS-relay remote,
  local-agent socket) — no single wire is load-bearing, exactly the plugin-port discipline.
- **§1.3.** Self-describing envelope + rewrap-to-local = no crypto lock-in; any domain ejects a KMS.
- **§2.5.** Transit keeps the KEK in OpenBao; the DEK is unwrapped only in-process, cached, never
  persisted in the graph. The core never holds the KMS key.

## Consequences

- **Positive:** state encryption becomes pluggable without ever becoming required; the hot path stays
  local (scale + partition tolerance); a concrete capability class is defined as the template for the
  reserved others; the sovereignty seams (domain-scoped + self-describing envelope + CaC binding) are
  in place so Cell-sovereignty is a config expansion, not a rewrite.
- **Negative / trade-offs:** envelope + self-describing DEK is a wire/format change to the existing
  state encryption — needs a versioned envelope + a read-path that accepts the legacy (bare-AES) format
  during migration; the KeyCustodian contract adds a capability-negotiation surface to the port.
- **Honest coupling boundary:** the partition-survival *guarantee* holds for **local-floor domains**
  only. A domain that OPTS INTO a remote KMS accepts a real cold-start coupling — a fresh pod during a
  KMS partition cannot unwrap that domain's DEK, so cannot read its state. That is inherent to choosing
  custody/residency; the agent-cache + regional replicas are coupling-*reducers*, never eliminators.
  The floor is always the escape hatch (rewrap-to-local).
- **Follow-ups (explicitly OUT of F):** full **Cell-sovereignty** (cross-region topology, key hierarchy,
  residency *enforcement*, a cut-off Cell's lifecycle) — its own ADR; per-domain KMS policy/attestation
  (who may unwrap, across a fleet); TPM/enclave-sealed local floor (the ultimate no-key-distribution
  scale answer); the `aws-kms`/`gcp-kms` provider plugins.

## Alternatives considered

- **Make OpenBao Transit the core's state-encryption backend (required).** Rejected — promotes an
  external service to load-bearing-for-the-substrate, the §1.4 fossil-dependency trap; violates
  non-negotiable #1.
- **Direct encryption (KMS encrypts every blob, no envelope).** Rejected — per-write network dependency
  on an external service; a KMS outage halts state writes; the antithesis of "sits under everything."
- **Pin the KeyCustodian to gRPC point-to-point.** Rejected — poor cross-DC reachability (wired endpoints, no
  geo-routing/failover, partitioned cold pods stuck); the contract must be transport-plural (§1.5), and
  the scale win is call-rate minimization, not a faster wire.
- **A single global KeyCustodian/KMS.** Rejected — precludes Cell-sovereignty; the KeyCustodian is domain-scoped
  from day one.
