# ADR 0104 — Capability dependencies: plugins require capability *contracts*, resolved first-class

- **Status:** **Accepted** (2026-07-23, steward) — charter-guardian PASS, vocabulary-linter CLEARED.
- **Date:** 2026-07-23
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.5 (sovereign contracts, multiple transports — a dependency is on a
  capability **contract**, never a named provider; the provider is a swappable transport) · §2.4 (no
  implicit precedence / single-writer — a `requires` is a **gate**, never a precedence rule, and provider
  selection never silently tiebreaks) · §1.8 (the abstraction must never hide diagnosis — an unmet
  dependency is a first-class, queryable *pending* reason, never a silent stall or a crash) · §1.4 (boring
  spine, pluggable everything — Temporal/Postgres/NATS stay named spine and are **not** nodes in the
  capability graph) · §1.2 (projections never a second truth — the resolved dependency edges are derived,
  rebuildable state). Builds on ADR-0046 (sovereign plugin port + the reserved capability classes),
  ADR-0100 (KeyCustodian — the first defined capability class), ADR-0103 (the runtime Connector/Actuator
  registry + its D6 queryable per-declaration status — the surface this ADR resolves *into*).

## Context

The enterprise arc adds four heavyweight integrations — **Temporal** (durable execution), **OpenBao**
(Transit / KV / PKI), **S3** (artifact + tofu state), **EC2** (provisioning). Several of these are not
peers: something that projects secrets wants **Transit** encryption; a tofu Actuator wants a **state
store**; a cert-lifecycle workflow wants a **cert issuer**. Today those relationships would be wired by
hand — boot order in `main.go`, an env var that happens to point at the right endpoint, a code path that
assumes a custodian is configured. That is precisely the Jenkins failure mode: implicit load order,
provider coupling by name+version, and a broken/absent dependency that manifests as a confusing runtime
failure rather than a declared, observable gap.

Two facts make this tractable *now* rather than as a rewrite:

1. **The provider side already exists.** The plugin `Manifest.capabilities` field (`plugin.proto:294`)
   already advertises the capability classes a plugin provides — `"keycustodian"` is live (ADR-0100), and
   the ADR-0046 reserved set (`StateStore`, `EventBus`, `SecretBroker`, `DurableExec`, `ArtifactStore`)
   names the rest. The field is explicitly a *core-owned, versioned* vocabulary and explicitly **"NEVER
   precedence-bearing (§2.4)"** — a constraint this ADR must preserve.
2. **The consumer side has a home.** ADR-0103's runtime registry already reconciles every
   Connector/Actuator against a live set of dialed plugins and already carries a **queryable per-
   declaration status (D6)** — the exact surface an *unmet dependency* should render into.

What is **missing** is the edge between them: nothing lets a declaration say *"I require a KeyCustodian"*,
and nothing resolves, orders, or gates on that. This ADR defines that edge — and **only** that edge
(framework first; the four integrations land as providers/consumers *after*, per the steward's sequencing
decision).

## Decision

A **capability dependency** is a declared requirement, on a Connector or Actuator, for a named
**capability class** — resolved by the ADR-0103 registry against the enabled plugins that *advertise*
that class, gating enablement and surfacing every unmet edge as an observable *pending* reason.

### D1 — Depend on capability **classes** (contracts), never on plugin names (§1.5)

A declaration requires `keycustodian`, **never** `openbao-transit`. The capability class is the
sovereign contract (pinned, hash-verified verb-shape — ADR-0046/0100); the plugin that fulfils it is a
swappable transport. Swapping OpenBao Transit for a cloud KMS provider changes *zero* consumer
declarations. This is §1.5 made structural, and it is the whole anti-Jenkins move: coupling to a contract
cannot version-rot the way coupling to a named provider does.

The requirable vocabulary is the **core-owned capability-class registry** (extends the ADR-0046 reserved
set + ADR-0100). A plugin never mints a capability's *meaning* (§1.5); it only advertises that it
provides one. New classes are added here as their first provider ships — this arc: `certissuer`
(OpenBao PKI, ADR-0098), and `provisioning` (EC2) when defined. Implemented as `types.ValidCapability`,
enforced at desiredstate admission on both Kinds; `DurableExec` is deliberately absent (D6).

**Provision is governed CaC, not the plugin's runtime self-claim (implementation refinement, §1.5 /
D3).** A provider declares the classes it fulfils in its Connector/Actuator declaration
(`provides: [...]`), and resolution counts **declared** providers read from the store — *not* the
plugins a given replica has locally dialed. Two forces drive this: (a) "the Manifest is advertisement;
the grant is truth" (§1.5) — provision is the operator's governed assertion (the same shape as
ADR-0103's `pluginhost.Grant`: the operator grants authority, the manifest only advertises), and the
plugin's `Manifest.capabilities` is the *verification* input, not the *resolution* input; (b)
**replica-consistency (D3)** — the Actuator loop
reconciles on *every* replica while the Connector loop is *leader-only*, so a follower could never see a
leader-only connector-provider through local dial state; only a store read gives an identical index
everywhere, so an Actuator that `requires` a Connector-provided capability (e.g. a tofu Actuator needing
`statestore` from the S3 Connector) resolves the same on every replica. This also makes resolution
health-independent by construction (Finding 1): a declared provider counts whether or not it is
currently dialed; actual provider liveness is diagnosed per-Run (D5), never a binding input.

**Provision has a REQUIRED, blocking verification gate — a phantom provider must fail at declaration,
not at Run-time (§1.5 drift-blocks / §1.8 never-hide-diagnosis).** Governing provision by CaC opens
one seam that must be closed by structure, not left to trust: a declared `provides: [keycustodian]`
whose plugin does **not** actually manifest-advertise KeyCustodian (or whose pinned capability contract
fails hash-verification) would be a *phantom provider* — it would satisfy a consumer's gate, flip it
enabled, and defer the failure to Run-time, which is exactly the "silently absorbed drift" §1.5
forbids. Therefore, at provider registration the registry **must** cross-check the dialed plugin's
`Manifest.capabilities` against its declared `provides` and hash-verify each capability's pinned
contract; a `provides` token the plugin does not advertise (or whose contract hash mismatches) makes
that **provider declaration** go PENDING/rejected with a queryable D6 reason (`provides 'X' but the
manifest does not advertise it` / `capability contract hash mismatch`), and it **does not count**
toward any consumer's satisfaction. This is complementary, not either/or: `provides` is truth for
*resolution + replica-consistency*; the manifest + pinned hash is the §1.5 *verification* gate.
Implementation may land as a slice *after* the first resolver slice, but it is a **required, booked
slice that MUST precede the first real provider** (OpenBao/S3) — never an open-ended follow-up, because
until it lands a mis-declared provider could silently satisfy a gate. (In the first resolver slice no
estate declares `provides`, so the window is not yet reachable.)

**Implemented (slice 2):** a leader-only verification reconcile (`connectorregistry.ReconcileProvider­Verification`)
dials each declared provider, reads its `Manifest.capabilities`, and persists the outcome to a runtime
projection `graph.capability_provider` (verified + reason). `buildProviderIndex` counts **only verified**
providers — so the check is store-visible and thus identical on every replica (the D3 property: an
every-replica Actuator consumer resolves a leader-only Connector provider the same everywhere), never from
per-replica dial state. A structural phantom is `verified=false` with a queryable reason and contributes 0.

Two hardening points (charter-guardian slice-2 review):

- **Structural vs transient (§2.4/D3).** A *structural* mismatch (manifest fetched, a declared token
  absent) may zero a verdict; a *transient* dial/fetch failure must **preserve the last-confirmed
  verdict** — else a blip in the leader's pass would drop an established provider, collapsing `≥2 → 1`
  and silently auto-binding a consumer (precedence-by-liveness). Health is never a binding input.
- **Descent surface (§1.8).** The verdict is not just DB-queryable: it is surfaced on the provider's
  `/connectors|/actuators/{name}` D6 status as an additive `capabilityVerification: {verified, reason}`
  (never clobbering the provider's own `enabled` — a plugin can be a working Syncer *and* a provider),
  and the consumer's unmet reason distinguishes "none declared" from "declared but rejected — inspect
  provider status."

**Contract-shape scope (honest):** verification checks capability-**token membership** only. Capability
classes today are sovereign-port verb shapes with **no** per-class JSON-Schema contract and **no** enforced
`min/max_protocol` gate in core — so there is no capability contract hash to verify yet. Enforcing
capability verb-shape compatibility (a per-class hash and/or a blocking protocol-version check) is **booked
hardening**, not performed here; no estate declares `provides` in such a class today.

### D2 — `requires` is a **gate**, orthogonal to §2.4 claim precedence

The single sharpest charter hazard. §2.4 (no implicit precedence) governs **claim resolution** — which
*writer* wins when two assert the same Entity attribute (answer: exclusive-fails or additive-union,
never a priority field). A capability dependency is a different axis entirely: it gates whether a plugin
**enables**, based on whether its required contract has a provider. It introduces **no** ordering over
*claims*, **no** last-writer-wins, **no** priority. `Manifest.capabilities` stays non-precedence-bearing:
we read it only as a set-membership predicate ("does any enabled plugin advertise `keycustodian`?"),
never to rank claims. The ADR must not, and does not, turn the capability field into a precedence
mechanism.

### D3 — Resolution fails **closed** and **observable**; provider selection never tiebreaks (§1.8, §2.4)

For each required class, the registry resolves against the set of **enabled** providers — membership
computed over the **declared/enabled** set, **independent of runtime health**. Health is a per-Run
diagnosis (D5), *never* an input to *which* provider a consumer binds: if the ambiguity count were taken
over the *healthy* subset, a transient outage would silently collapse two providers to one and
last-one-standing-bind — itself a §2.4 precedence-by-liveness, and non-deterministic across a blip.

- **0 enabled providers → the consumer stays PENDING**, enabled=false, with a D6 reason:
  `unmet dependency: no provider for 'keycustodian'`. Never crash, never a silent stall (§1.8).
- **1 enabled provider → auto-bound.** The common case needs no operator ceremony.
- **≥2 enabled providers → PENDING** unless an **estate-level capability→provider binding** selects one.
  The registry **never silently tiebreaks** which KMS you got (§2.4's no-implicit-precedence applied to
  provider selection). Crucially the binding lives **not in the consumer** but in a registry-scoped CaC
  surface **mirroring ADR-0100's domain→custodian (`portCustodian`) pattern** — so a consumer *always*
  declares only `requires: [keycustodian]` and D1 stays structurally true (swapping the provider edits
  **one** estate binding, never any consumer). Ambiguous-and-unbound → PENDING with
  `ambiguous: 2 providers for 'keycustodian'; add an estate binding`.

Every outcome is a queryable reason on the existing ADR-0103 D6 status (`GET /connectors/{name}`,
`/actuators/{name}`, and the MCP mirror) — one-click descent to *why this plugin is not running*.

### D4 — Ordering is **level-triggered convergence**, not a boot-time toposort

The registry reconcile is already level-triggered (controller-runtime idiom; backend-go rule). Ordering
emerges for free: each pass enables whatever is now-satisfiable; a consumer whose provider is not yet up
simply stays PENDING this pass and enables a later pass once the provider lands. Convergence in ≤ depth
passes, with **no explicit DAG walk and no ordering deadlock**. A dependency **cycle** never converges —
its members stay mutually PENDING (safe, observable), and the registry *may* additionally emit a cycle
Finding for a better message, but safety does not depend on cycle detection. This is strictly more robust
to partial failure than a hard boot-time topological enable.

### D5 — Dependencies gate **enablement**, not steady-state liveness (anti-fragility, §1.8)

A required provider must be present **at enable time**. A provider going *unhealthy at runtime* does
**not** cascade-disable its consumers. Tearing down every KeyCustodian consumer on a transient KMS blip
is Jenkins-fragility in the other direction — a small outage amplified into an estate-wide teardown.
Instead, a runtime provider outage surfaces where it actually bites: the individual **Run** that needs
the capability fails with a clear Finding (§1.8), while stable consumers stay enabled and recover when the
provider does. Enablement is gated; liveness is diagnosed, not cascaded.

### D6 — Temporal and the substrate spine are **not** nodes in the capability graph (§1.4)

Per the steward's decision, **durable execution is spine, not a swappable capability.** Temporal (and
Postgres, NATS) are named §1.4 spine that strattd links directly; they are *ambient platform guarantees*,
always present, so they are **not requirable capabilities** and **not nodes** in this graph. The capability
graph is **plugin → plugin only**. Consequences: (a) the reserved `DurableExec` capability class is
**narrowed to reserved-but-not-planned** — we do not intend to abstract Temporal behind a swappable
contract, because that would put a pluggable protocol *under* the deterministic core (against §1.5's "no
external protocol is load-bearing for the deterministic core"); (b) `EventBus`/`StateStore` as *reserved*
classes describe an *estate-facing* alternative backend a plugin might offer, never a replacement for the
core's own NATS/Postgres spine. This keeps the graph small, honest, and about the pluggable breadth — not
the boring spine.

### D7 — The core's own KeyCustodian need stays where ADR-0100 put it (for now)

The core itself consumes `keycustodian` (envelope encryption) via ADR-0100's `portCustodian` config +
`localCustodian` floor. This ADR does **not** re-home that into the plugin→plugin framework in its first
slice; the core-as-consumer path is already solved and has a local floor (encryption is never *blocked*
by an absent custodian). Unifying "the core is also a capability consumer" into this framework is a noted,
deferred follow-up — attractive for one uniform resolution story, but not required to ship the plugin→
plugin edge the enterprise adds need.

## What this looks like (illustrative, non-normative)

A Connector that projects secret metadata and wants its projected values encrypted:

```yaml
# estate/connectors/vault-kv.yaml
name: vault-kv
class: syncer
address: stratt-openbao:9090
requires: [keycustodian]        # ← the new edge; contract, not provider
source: { kind: openbao-kv, name: kv-prod }
```

A tofu Actuator that keeps state in S3:

```yaml
# estate/actuators/tofu.yaml
name: tofu
address: stratt-tofu:9090
actionNames: [tofu/apply]
requires: [statestore]          # S3 (or any statestore provider) must be enabled first
```

The consumer stays provider-agnostic even when two KMSs are enabled — disambiguation is an
*estate-level* binding (D3), mirroring ADR-0100's `portCustodian`, never a field on the consumer:

```yaml
# estate/capability-bindings.yaml   (registry-scoped; the ONE place a provider is named)
statestore: s3-prod                 # ≥2 statestore providers enabled → operator's explicit choice, in Git
# tofu.yaml above is untouched: it still says only `requires: [statestore]`
```

The enterprise adds as *providers* (manifest side, no consumer ceremony):

| Add        | Advertises (`Manifest.capabilities`)          | Role |
|------------|-----------------------------------------------|------|
| **OpenBao**| `keycustodian`, `secretbroker`, `certissuer`  | multi-capability provider (the exemplar) |
| **S3**     | `artifactstore`, `statestore`                 | storage provider |
| **EC2**    | `provisioning`                                | provisions machines other plugins target |
| **Temporal**| — (spine, D6)                                | not in the graph |

## Consequences

- **Positive.** The enterprise adds slot into a declared, resolved, observable dependency fabric instead
  of hand-wired boot order. A missing OpenBao is a queryable *pending* reason on the dependent, not a
  cryptic crash. Providers are swappable by contract (§1.5). No new precedence surface (§2.4). Ordering is
  a property of the existing level-triggered reconcile — little new machinery. The graph stays honest:
  spine is spine, plugins are the pluggable breadth.
- **Negative / cost.** A new `requires` field on the Connector/Actuator Kinds (migration + validation +
  the resolver in `connectorregistry`). A new small **capability index** in the registry (enabled
  class → provider set) that must re-evaluate consumers when a provider enables/disables. Operators gain a
  new failure mode to understand — *pending on unmet dependency* — mitigated by making the reason
  first-class in the D6 status (§1.8) rather than a log line.
- **Scope discipline.** This ADR ships the **framework only**: the `requires` edge, the resolver, the
  pending/ambiguous surface. The four integrations are separate ADRs that consume it. `DurableExec`
  narrows to reserved-not-planned (D6).

## Alternatives considered (rejected)

- **Plugin-to-plugin, version-pinned dependencies (the Jenkins / npm model).** Couple a consumer to a
  named provider + version range. Rejected: it is the exact "janked together" failure the steward called
  out — provider coupling, version hell, and cascade breakage. §1.5 says depend on the contract.
- **Explicit boot-time topological enable with hard ordering.** A one-shot DAG walk that enables in
  dependency order. Rejected in favor of level-triggered convergence (D4): more robust to partial
  failure, no ordering deadlock, and the idiom the registry already uses.
- **Cascade-disable consumers on provider outage.** Rejected (D5): amplifies a transient blip into an
  estate-wide teardown. Gate enablement; diagnose liveness per-Run.
- **Make `DurableExec` a swappable capability so Temporal is one provider.** Rejected per the steward's
  decision and §1.4/§1.5: Temporal is load-bearing spine; a swappable orchestration protocol *under* the
  deterministic core is exactly what the charter forbids.
- **A precedence/priority field to pick among ≥2 providers.** Rejected (§2.4): provider ambiguity fails
  closed and demands an explicit CaC binding; the platform never silently ranks providers.

## Follow-ups (separate ADRs / slices)

1. Implementation slices: `requires` on `types.Connector`/`types.Actuator` + migration; the estate-level
   capability→provider binding surface (D3, mirroring `portCustodian`); the resolver + capability index in
   `connectorregistry` (provider set over the *enabled* set, health-independent — D3/Finding 1); the
   pending/ambiguous D6 reasons; race-tested reconcile. A one-line `plugin.proto` comment marking the
   reserved `DurableExec` class as *reserved-not-planned* (D6), so the code-level vocabulary matches.
2. **Provider-verification slice (D1) — ✅ IMPLEMENTED (slice 2).** Leader-only verification reconcile +
   `graph.capability_provider` projection; `buildProviderIndex` counts only verified providers, so a
   phantom `provides` is recorded verified=false with a queryable reason and never counts toward a
   consumer. Closes the §1.5-drift / §1.8-late-diagnosis seam. (Contract-hash: N/A for today's proto
   verb-shape classes — see D1.)
3. The four enterprise providers/consumers (Temporal-as-spine wiring, OpenBao multi-capability, S3
   storage, EC2 provisioning) — each its own ADR, consuming this framework. **Gated on #2.**
4. (Deferred) unify the core's own `keycustodian` consumption (ADR-0100 `portCustodian`) into the same
   resolution story (D7).
5. (Deferred) a UI/estate view over the resolved capability graph — providers, consumers, and pending
   edges — the descent surface for §1.8.
