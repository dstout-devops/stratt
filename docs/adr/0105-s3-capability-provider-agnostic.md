# ADR 0105 — Capability providers are provider-agnostic: S3 is provider #1 of statestore/artifactstore, never "the provider"

- **Status:** **Accepted** (2026-07-23, steward) — charter-guardian PASS, vocabulary-linter CLEARED (2 findings + 2 flags folded).
- **Date:** 2026-07-23
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.5 (sovereign contracts, multiple transports — a capability is a CONTRACT;
  the provider is a swappable transport, so S3 vs Artifactory vs Garage is an operator choice, never a
  code change) · §1.4 (boring spine, pluggable everything — object storage is community breadth behind a
  core contract, never a named-vendor dependency) · §3 ("artifacts/facts → S3, Postgres stores summaries
  only" — bulk bytes never proxy through the core) · §2.4 (no implicit precedence — ≥2 providers is an
  estate binding, never a platform tiebreak) · §2.5 (secrets brokered, never baked — the provider resolves
  a use-checked credential coordinate) · §1.1 (type the seams — build a provider only when a Contract
  demands it, never speculatively). Builds on ADR-0104 (capability dependencies + provider verification),
  ADR-0103 (the Connector/Actuator registry), ADR-0097 (the awss3 Connector), ADR-0016 (the OpenTofu
  Actuator + its HTTP state backend), ADR-0002/0093 (S3-compatible object storage, never MinIO-by-name).

## Context

The enterprise arc wants object storage as first-class capacity: `statestore` (durable tool state — tofu
remote state) and `artifactstore` (content-addressed artifacts/evidence/plans). The steward's constraint
is explicit and load-bearing: **S3 must never be "the" provider — Artifactory (or GCS, or Garage) must be
a drop-in alternative.** ADR-0104 already gives the shape for that (`requires: [artifactstore]` targets the
CONTRACT, not `s3`), so this ADR's whole job is to make the *contract* genuinely provider-agnostic — else
swappability is aspirational.

Two grounding facts from the codebase (verified):

1. **OpenTofu state has an incumbent core backend — but it is a FLOOR, not a registry provider.** tofu
   remote state does **not** use S3 today: the opentofu Actuator points tofu's **HTTP backend**
   (`-backend-config=address=…`, `plugins/opentofu/server.go:107`) at strattd's own encrypted
   HTTP-over-Postgres state backend (`core/internal/statebackend`, ADR-0016). That backend is
   **core-side, core-mediated** — it does not declare `provides: [statestore]` and is **not** a node in
   the capability graph (ADR-0104 D6: the graph is plugin→plugin only; the core spine is never a node).
   So it is the statestore **floor** — the always-available default, exactly as `localCustodian` is the
   KeyCustodian floor (ADR-0100) — and S3 will be the **first registry provider** of `statestore`, not a
   second. (The framework is still real, not theoretical: `statestore` gets its first swap-in provider;
   the first ≥2 case is S3-vs-Artifactory later.)
2. **`artifactstore` is mostly aspirational.** Only `evidencestore` (now on the shared `objectstore`
   client, ADR-0104 pre-work) actually writes to S3; the plan store and tofu state are Postgres `bytea`.
   The charter's "artifacts → S3" is unimplemented outside Evidence, and there is **no plugin consumer of
   `artifactstore` yet** — the artifact-producers (plans, evidence) are core-side and D7-deferred.

## Decision

### D1 — Capabilities are provider-agnostic CONTRACTS; S3 is provider #1, never "the provider" (§1.5)

`statestore` and `artifactstore` are core-owned capability classes (ADR-0104 `types.ValidCapability`). A
plugin advertises `provides: [artifactstore]`; consumers declare `requires: [artifactstore]`. S3 is the
**first** provider — Artifactory, GCS, Garage, SeaweedFS are later plugins advertising the **same** class,
and a consumer changes **zero** lines to move between them. "The S3 artifact store" is an anti-pattern this
ADR forbids in identifiers and design; there is only "*an* `artifactstore` provider."

### D2 — The contract is a provider-agnostic HANDLE, never a vendor API surface (§1.5, §3)

This refines ADR-0104's coordinate-handback: the resolved coordinate must not leak the provider's API, or
the consumer couples to it.

- **`artifactstore` → a pre-authorized HTTPS handle.** The capability resolves `put(content-hash) →
  pre-authorized PUT URL` / `get(handle) → GET URL`. The consumer does a **plain HTTPS PUT/GET** — **no S3
  SDK, no Artifactory SDK**. S3 returns an S3 presigned URL; Artifactory returns an Artifactory URL;
  Garage/SeaweedFS return theirs. Content-addressed (sha256) so integrity is provider-independent. Bulk
  bytes flow consumer↔store directly, **never through the core** (§3). (A pure-filesystem/embedded provider
  that can't presign is a later, explicitly-bytes-streaming variant — not this contract.)
- **`statestore` → a tool-backend config handle.** The capability resolves the backend configuration the
  consuming tool feeds its own state engine — for OpenTofu, a backend block (`address`/`bucket`/`key`/… +
  a §2.5 credential coordinate). Provider-agnostic **across the tool's supported backends**: the core
  floor resolves an `http` backend; S3 resolves an `s3` backend (native locking via `use_lockfile` or an
  external lock table — the provider owns lock semantics). This handle is honestly **weaker** than
  artifactstore's: the backend block *names its type* (`http` vs `s3`) and lock semantics differ per
  provider, so swappability here is *across tofu's own backend plurality*, not a fully vendor-blind
  handle. It is resolved and injected **at Run dispatch** (replacing the hard-wired
  `STRATT_STATE_BACKEND_URL`), **never authored into consumer content** — so D1's "zero consumer lines"
  still holds: a provider swap is a dispatch-time config change, not a consumer edit.

### D3 — Capabilities are served as pinned Actions over INVOKE, not new per-capability proto verbs

A capability provider advertises `provides: [X]` **and** exposes a **provider-scoped** resolve **Action**
following the frozen `<plugin>/<op>` convention — e.g. `awss3/statestore-resolve`, `awss3/artifactstore-put`
— **never** a capability-scoped name like `statestore/resolve`, which would **collide across providers**
under the global action-name exclusivity `orchestrate` enforces (§2.4, `orchestrate.go`). The core resolves
`capability → bound provider → that provider's action`, so the capability binding carries the
capability→action mapping; two providers of one class expose *different* action names and never clash. The
Action's **input/output Contract is pinned at the CLASS level** (core-owned, §1.5, hash-verified) so every
provider of a class conforms to one shape — that is what preserves D1's "swap provider, zero consumer
change." The core invokes via the existing `InvokeRaw` path (grant, principal, output-contract reconcile),
**no proto change**. Dedicated port verbs stay reserved for capabilities that can't fit an Action (as
KeyCustodian's `WrapKey`/`UnwrapKey` did, ADR-0100 — justified by hot-path crypto; resolution is low-rate
request→typed-response). This gives ADR-0104 D1's booked verb-shape hash-check a concrete home: reconcile
the provider's resolve-Action output against the core-pinned capability Contract.

### D4 — Build with a consumer, never speculatively (§1.1); `statestore`+OpenTofu ships first

`statestore` has a real consumer (OpenTofu), so it ships **end-to-end**: the S3 provider advertises
`provides: [statestore]`, an opentofu Actuator that opts in declares `requires: [statestore]`, and at Run
dispatch the orchestration resolves the capability (invokes the provider's resolve Action) and injects the
backend config into the tofu Apply — replacing the hard-wired `STRATT_STATE_BACKEND_URL` **for that
Actuator**. An opentofu Actuator that does **not** require the capability keeps using the core **floor**
(Context #1) — so opting into a plugin state store is additive, never a forced migration off Postgres.
`artifactstore` has **no plugin consumer yet** (its producers are core-side, D7-deferred), so this ADR
**defines** its contract but does **not** ship a speculative provider — the `artifactstore` provider lands
when a consumer Contract demands it (§1.1), and even its class-level resolve-Action Contract is **not
pinned until then** (pinning a guessed I/O shape is itself speculative). Building a provider no Contract
consumes is the anti-pattern §1.1 forbids.

### D5 — Selection among ≥2 providers is an estate binding, never a platform tiebreak (§2.4)

S3 is the **first registry provider** of `statestore` (the core backend is a floor, not a registry
provider — Context #1). So when it lands there is exactly **one** provider → ADR-0104 **auto-binds** it,
and no `estate/capability-bindings.yaml` surface is required for this first slice. The genuine ≥2 case —
which forces that binding surface (ADR-0104's booked follow-up) — arrives with a **second plugin provider**
(Artifactory, GCS, Garage): then `requires: [statestore]` fails **closed** (PENDING, ADR-0104 D3) until the
operator names one provider in Git; the platform never guesses (§2.4). Promoting the core floor itself into
a registry provider is explicitly **out of scope** here — it would need its own reconciliation with ADR-0104
D6 (core spine is not a node).

### D6 — The provider is a plugin; it owns its own object-store client + brokered credentials

The S3 capability provider is a **plugin** (extend `plugins/awss3`, which already builds an S3 client, or a
dedicated object-store provider) advertising `provides` + exposing the resolve Actions. It builds its own
S3 client (separate module — it cannot import `core/internal/objectstore`; per-plugin clients are correct
§1.4 isolation). Its object-store credentials arrive via a §2.5 CredentialRef broker at pod spawn, never
baked. The core `objectstore` package (ADR-0104 pre-work) remains the core-side client (evidence, and any
future core-side coordinate-resolver) — not shared across the module boundary.

## Consequences

- **Positive.** Vendor-agnostic by construction: Artifactory is a future plugin, not a migration; consumers
  never name a vendor (§1.5). Bulk bytes stay off the core **for capability consumers** (§3) — core-side
  producers (evidence, plan/state bytes) remain D7-deferred and are not regressed, so §3 is *not* claimed
  globally satisfied. `statestore` proves the first live provider→consumer capability chain end-to-end. The
  class-level capability Contract gives ADR-0104's booked verb-shape hash-check a concrete home (D3).
- **Negative / cost.** Orchestration must resolve a required capability at Run dispatch and inject the
  handle (new wiring on the opentofu path, replacing `STRATT_STATE_BACKEND_URL` for opting-in Actuators).
  A class-level capability Contract per resolve Action to author + hash-pin. (The estate binding surface is
  *not* needed for this first slice — S3 is the sole provider and auto-binds, D5.)
- **Scope discipline.** Ships: `statestore` S3 provider (sole → auto-bound) + OpenTofu consumer + the
  resolve-Action mechanism. Defines-but-defers: `artifactstore` provider *and its Contract* (no consumer
  yet, §1.1); the estate binding surface (no ≥2 case yet, D5). Unchanged: core planstore/statebackend stay
  Postgres, evidence stays on `objectstore`, and the state-backend floor stays available (D7 — core is
  neither promoted to a provider nor forced off Postgres).

## Alternatives considered (rejected)

- **S3-shaped coordinate handback** (hand back a bucket/key + S3 API). Rejected (D2): couples every
  consumer to the S3 SDK, so Artifactory can't drop in — the exact vendor-lock the steward forbade.
- **Bytes through the core / provider proxies all object data.** Rejected (§3): recreates "the core holds
  the bytes" at scale; the pre-authorized-URL handle keeps bulk off both the core and any SDK.
- **New per-capability proto verbs for statestore/artifactstore.** Rejected for now (D3): proto churn per
  capability; the Action+pinned-Contract path reuses existing governance. Dedicated verbs stay reserved
  for genuinely non-Action shapes (KeyCustodian's precedent).
- **Ship an `artifactstore` provider now.** Rejected (§1.1, D4): no consumer Contract demands it yet;
  building it speculatively is the discipline violation. Define the contract, defer the provider.
- **Make S3 "the" object store and hard-wire it (statebackend → S3).** Rejected (§1.5): that is precisely
  the un-swappable coupling this ADR exists to prevent; S3 is provider #1 behind the contract, not the
  contract.

## Follow-ups (separate slices / ADRs)

1. `statestore` end-to-end (this ADR's first slice): the S3 provider's `awss3/statestore-resolve` Action +
   the class-level pinned `statestore` Contract; orchestration resolves + injects the backend config on the
   opentofu path; an opting-in opentofu Actuator declares `requires: [statestore]`. S3 auto-binds (sole
   provider).
2. The estate `capability-bindings.yaml` surface (ADR-0104 D3 / follow-up) — deferred until a **second**
   `statestore` provider lands (Artifactory/GCS/Garage), which is what first makes `requires: [statestore]`
   ambiguous (D5). Not needed while S3 is sole.
3. `artifactstore` provider **and its Contract** — authored only when a consumer Contract demands it (a
   plugin artifact path, or re-homing a core producer off Postgres per D7); pinning a guessed I/O shape
   beforehand would itself be speculative (§1.1).
4. The capability verb-shape hash-check (ADR-0104 D1 booked): reconcile a provider's resolve-Action output
   against the core-pinned class-level capability Contract.
