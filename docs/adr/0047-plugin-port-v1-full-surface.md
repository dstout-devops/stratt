# ADR 0047 — Plugin port v1 full surface: write-back, relations, and the rung ladder over the wire

- **Status:** **Proposed** — extends the sovereign plugin port (ADR-0046) to cover the full
  Connector/Actuator/Action/Emitter breadth in one additive, non-breaking change, and reconciles the
  ADR-0046 findings (#2/#3/#4) for the Actuator/Action/Emitter verbs. Consequential (Contracts + authz over
  the wire) → carries a charter-guardian review; steward sign-off before extraction begins.
- **Date:** 2026-07-16
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.1, §1.2 (projections / two write paths), §1.5 (schemas pinned, drift blocking),
  §2.1 (ownership), §2.2/§2.4 (the Contract rung ladder, no implicit precedence), §1.8 (never hide failure),
  §3 (Contracts "some tool-derived from tofu plans / MCP declarations"). Builds on ADR-0046, ADR-0017
  (provision→configure), ADR-0019 (Baselines), ADR-0022 (mcp), ADR-0031 (actions), ADR-0032 (Bundles).

## Context

ADR-0046 Phase A/B proved the Syncer verb (Observe → `ObservedEntity`). Extracting the remaining connectors
requires the other verbs — Actuator (Plan/Apply/Destroy), Action (Invoke), Emitter (Subscribe) — to carry
their real return data. Three breadth surveys (all six Syncers, five Actuators, the Actions + Emitters) found
that **every gap is the same shape**: the core must read *structured, governed* data the plugin proposes
(relations, provisioned entities, gathered facts, action outputs, drift, per-target status, event-match
fields, tool-derived schemas). All of it reuses the Syncer write-back carve-out (ADR-0046 finding #3):
envelope-governed **and** core-legible/validated, never opaque desired-state — the plugin proposes, the core
resolves, validates, and stamps provenance. This is not a new trust model; it **projects the existing in-tree
`actuators.Interpreted` seam** (which already carries `Entities`/`Outputs`/`OutputsContract`/`Drift`/`MCPTools`
with charter-clean provenance rules) onto the wire.

Freezing this surface **now**, before extraction, is deliberate: the contract must be stable so per-connector
extractions are mechanical, not a source of proto churn.

## Decision

Extend `proto/stratt/plugin/v1/plugin.proto` additively (new fields at unused numbers, new messages; no
renumbering — `buf breaking` stays green and extracted-vcenter is untouched). The full field map lives in the
proto; the **disciplines** that make it charter-clean are recorded here.

### 1. Relations by identity — resolve, never vivify
`ObservedEntity.relations[]` (`ObservedRelation{type, to_scheme, to_value}`) and `ObserveResponse.gone_relations[]`.
An edge names its target **by identity scheme+value, never a graph id** (the plugin never sees ids). The core
resolves the target against an **existing `entity_identity` row only** — an unresolved target emits a Finding
and drops the edge; it **never auto-creates a placeholder Entity** (that would be a covert write of an
ungranted identity key). The **tier+grant identity gate (ADR-0046 finding #4) applies to the target scheme**
too: naming a target by a shared cross-source scheme (`dns.fqdn`, `mac`) is a correlation claim and is gated
exactly as emitting that scheme is. Edges are core-stamped (mirrors `Projector.UpsertRelation`), §1.2-clean.

### 2. Write-back is provenance-free; the host picks the write path per verb (t=0 invariant)
`ApplyResponse.write_back[]` / `InvokeResult.entities[]` reuse `ObservedEntity` — which has **no provenance
field**, so a plugin structurally cannot choose its `WriterKind`. The **host** selects the write path by verb:
Observe → `WriterSyncer` (enters presence/tombstoning, ADR-0042); Apply/Invoke → `WriterRun` (Run-provenance,
correctly excluded from presence). The facet-ownership registry (`ValidateFacet`, §2.1) runs identically on
both paths. **Per-verb projector selection is a load-bearing invariant** — an extraction must never route
Apply write-back through the Normalizer path.

### 3. `outputs` and `match` are read but never become graph state
- `InvokeResult{outputs:Payload, output_contract:ContractRef, entities[]}`: `outputs` is
  **validated-not-interpreted** against a **pinned** `output_contract` before capture (an Action declaring no
  output Contract is refused), and lands as a **Run output** for cross-Step binding — never directly as an
  Entity/Facet. The only path to graph state is `entities[]`, which goes through §2.
- `EmittedEvent{match:google.protobuf.Struct, subject, type, occurred_at}`: `match` is the Emitter analog of
  Envelope coordinates — a **core-legible routing projection** the trigger engine evaluates CEL against, and
  **never persisted as Entity/Facet state**. The opaque `payload` stays for faithful delivery/hashing; the
  plugin computes `content_hash` **excluding** `occurred_at` (matching `EventHash`'s exclusion of ReceivedAt).
  *(Follow-up judgment: an Emitter may later pin its event shape to a declared Contract so triggers evaluate a
  hash-pinned shape; not required for v1.)*

### 4. The rung ladder over the wire — `DerivedContract` (the finding-#2 reconciliation)
**Rung-1 is sovereign and immutable over the wire; finding #2 stands there unchanged.** A plugin can never
introduce, mutate, satisfy, or shadow a hand-written, core-shipped, `ContractRef`-pinned rung-1 Facet/Contract.
`DerivedContract` carries only **tool-derived rung-2** (tofu plan output schema) or **declared rung-3** (mcp
tool schema) documents describing the plugin's **own** outputs/tools — which charter §3/§2.4 explicitly
contemplate. Structural guardrails (all in the message + registration path):
- **(a)** an explicit `rung` enum whose values are only rung-2/rung-3 — **rung-1 is not representable**;
- **(b)** the plugin asserts **no hash**; the core **recomputes and pins** the sha256 from the bytes
  (verify-don't-trust, as with `Envelope.content_hash`);
- **(c)** a `rev` field — different bytes under the same `(schema_id, rev)` is **blocking drift** (§1.5),
  never silent replacement;
- **(d)** `schema_id` is **namespace-confined to the plugin's granted scope**; registration rejects any id
  that collides with a rung-1 Facet namespace or another owner's namespace, and never overwrites;
- **(e)** a `DerivedContract` may **never** register or satisfy a Facet namespace (§1.1).
With (a)–(e), §1.5 is *upheld*: derived docs obey the same pin-and-block-drift rule; the core is the pinner.

### 5. `ArtifactRef` in the Envelope — governed, verify-don't-trust
`Envelope.artifact` (`ArtifactRef{uri, sha256, media_type}`) is a **pointer**, no inline bytes. It rides the
Envelope (not the opaque Payload) precisely because the core must **cosign/OCI-verify it before hand-off**
(ADR-0032) and cannot parse the payload without breaking content-blindness. The core verifies `uri`/`sha256`
against the signature/digest — the plugin's claim is checked, never trusted.

### 6. `ItemResult` partial-success — honest, non-atomic where it must be
`ItemResult{item_key, Status(OK/CHANGED/FAILED/UNREACHABLE)}` mirrors the in-tree `TargetResult` — a per-target
**outcome**, no ranking/priority/last-writer field (§2.4-clean), FAILED distinct from UNREACHABLE (§1.8). The
root Item's terminal `ok` **must fold** any FAILED/UNREACHABLE child to non-OK — partial success is never a
green Run (§1.8). Per-target status reports outcome **without implying rollback**: atomic Apply on the root
(inv #9) is the tofu class; ansible/script fan out and partial is the norm — the port reports status, it does
not promise atomicity it cannot guarantee for the fan-out class.

### Corrected non-gaps (recorded so they are not re-litigated)
- **No mid-Apply gate.** A Gate is a separate DAG Step between two RPCs joined by `idempotency_key`; the
  stateless per-RPC port is already correct.
- **RemoteSafe/Sites dissolves.** The port carries CredentialRef *names* only; material never crosses (§2.5),
  so the hub-local RemoteSafe gate is unnecessary — wiring, not proto.

### 7. Designed for growth — the USB principle
The port must accommodate systems we have not built yet (full AAP, Terraform, Kubernetes controllers,
Crossplane, cloud providers, Active Directory) the way USB accommodates unknown future devices: a stable bus
+ typed device classes + capability descriptors + a bounded vendor escape-hatch + strictly additive,
negotiated versioning. Analysis of those systems shows **growth never needs a new verb** — the five verbs
(Observe/Apply/Destroy/Invoke/Subscribe) + discovery are the device classes and are complete. Growth lands in
three governed places:

- **(a) Capability negotiation (the descriptor + handshake).** `Manifest.capabilities` is a **forward-
  compatible flag set** (`observe.delta`, `apply.plan-artifact`, `invoke.dry-run`, `emit.match`), and
  `Manifest.min_protocol`/`max_protocol` declare the wire-version range the core negotiates against. Unknown
  capabilities are ignored (old core × new plugin), absent ones are not used (new core × old plugin) —
  graceful degradation both directions; tofu's `GetProviderSchema` / version-negotiation pattern generalized.
  **Guardrails (guardian #2):** the strings are transport, but the **capability *vocabulary* is a core-owned,
  versioned registry** — the core ships the known tokens and their meaning; a plugin never mints contract
  meaning (§1.5, the core is the pinner). A capability **may never be precedence- or ranking-bearing** — it
  toggles whether an already-typed, pinned field/verb is exercised, never which plugin outranks another
  (§2.4). Protocol negotiation carries a **stated, CI-gated N-1 floor** (§1.7): the core drops support below
  N-1 and a genuinely incompatible change is a new negotiated major — a bounded window, never an unbounded
  1..N compatibility matrix (the §1.7 fossil the port must avoid).
- **(b) Schema-carried domain specifics.** Everything system-specific (a CRD spec, an AD GPO, a cloud
  resource shape, a credential-injection type) rides `facets` + `DerivedContract` + `ActionDecl` input/output
  — never new core fields. The core stays domain-blind; the plugin is the content-expert (§1.1).
- **(c) No untyped escape-hatch — the growth spine IS the escape-hatch (guardian #2, RECONSIDER folded).**
  An earlier draft proposed a `google.protobuf.Struct extensions` side-channel on Envelope/Manifest. **Removed.**
  The Envelope is *defined* as the fully-typed governance half (invariant #1); an untyped open map on it is
  §1.1 inside the governance seam, and its "never governance-bearing / never persisted / ignored-when-unknown"
  guardrails are convention, not the data-layer enforcement §1.2 demands (and the core already reads a Struct —
  `EmittedEvent.match` — for routing, so a second "don't read this one" Struct is a firewall one commit from
  collapse). It is also **redundant**: proto3 already preserves unknown fields, the sanctioned growth spine is
  **additive typed fields at new numbers** (`buf breaking`-gated), and private matched-pair data belongs in the
  **opaque `Payload`** the core is structurally blind to. USB class descriptors are themselves *typed and
  versioned per class*, not free-form — the correct analog is a new typed field, not a bag.

**Two fundamental seams these systems reveal (not speculative — each is needed by ≥2 of them):**
- **Plan-as-artifact.** `PlanResponse.plan` (`ArtifactRef`, hash-pinned) + `ApplyRequest.plan_ref`: Apply
  applies **exactly** the plan a Gate approved, closing a plan→apply TOCTOU under §1.8 (Terraform saved plans;
  any gated converge). **Where the TOCTOU actually closes (guardian #2):** (i) the **Gate approval record
  binds the exact `sha256`**, and that is the hash the human sees at approval (§1.8 approve-what-you-see);
  (ii) the **core verifies `plan_ref.sha256` against the Gate-approved digest at the Apply RPC boundary** —
  same cosign/OCI verify-don't-trust path as `Envelope.artifact` — never a plugin merely re-reading its own
  plan; (iii) the plan artifact store is **content-addressed / immutable** so bytes behind the `uri` cannot be
  swapped under a fixed hash. The **unary `Plan` verb is the canonical producer** of the hash-pinned plan that
  `plan_ref` consumes (a streaming dry-run `Apply` is for diagnostics/descent, not the approve-and-pin path) —
  exactly one approve-and-pin path.
- **Provisioned CredentialRef.** `InvokeResult.provisioned_creds[]`: a converge that creates a credential
  (Crossplane connection secret, RDS password, AD service account) returns the **CredentialRef name** — the
  plugin wrote material to its own broker; only the pointer crosses (§2.5). **Anti-hijack guardrail (guardian
  #2, mirrors inv #11 / DerivedContract (d)):** the provisioned name is **namespace-confined to the
  plugin/Source scope of the authenticated channel** (core-prefixed/validated — the plugin cannot choose a
  bare global name); registration **rejects any name colliding with a CredentialRef owned by another owner and
  never overwrites**; and the injection/broker binding is **core-stamped from the channel identity**, so a
  provisioned cred resolves only within the granting scope's authz. The core never copies material into its
  own store to satisfy a later use-without-read Step (§2.5). Without this it is credential-hijack-by-name.

**Two deliberate non-additions** (the model already absorbs them; adding them would be speculative bloat):
- **No async-operation handle.** For the **converge verbs (Apply/Destroy)** the reconcile primitive
  (ADR-0046) IS the handle — a long/eventual converge surfaces as Observe-not-ready and the core re-converges.
  For **Invoke** (imperative, no desired state to re-observe) the absorber is **streaming/persistent
  connection (inv #4) + idempotency-by-contract (#7) + checkpoint/resume** — not reconcile. Either way no
  job-poll shape is needed. (A non-idempotent long Action is a pre-existing property an async handle would not
  fix.)
- **No new coordinate axes.** Region/account/project/workspace/OU are **scopes → `labels`**, not governance
  coordinates (`cell/band/beam/kind`). Minting them as coordinates is the §1.1 ontology creep the charter
  forbids. **But scope-bearing labels carry authz/blast-radius weight via Views**, so a plugin must not be
  able to self-assert a scope label that widens its authz (claim `account=prod` on a channel scoped to
  `account=dev`): **scope-label values are validated against the channel's granted scope (invariant #11)** —
  core-validated, never free-set. (Same rule as identity-scheme gating, on the label axis.)

**The intermittent-update mechanism.** New capabilities/messages are additive-only; `buf breaking` is the
CI gate; a genuinely incompatible change is a **new negotiated protocol major**, never a silent break — USB's
"1.1 → 2.0 → 3.x, negotiated, backward-compatible" made structural (§1.5, §1.7).

## Consequences
- **Positive:** the contract is frozen for every plugin class AND has explicit, governed growth room for
  systems not yet built (AAP/Terraform/K8s/Crossplane/cloud/AD fit without a new verb); extractions become
  mechanical; the in-tree
  `Interpreted` model is preserved 1:1 over the wire; single-writer/provenance/ownership hold unchanged.
- **Negative / trade-offs:** the host grows per-verb handling (write-back projector selection, relation
  resolution, DerivedContract registration, match evaluation) — landed incrementally as each connector class
  is extracted, each behind its own tests. The `google/protobuf/struct.proto` import is added.
- **Deferred (tracked):** host-side cursor wiring for delta Syncers (msgraph); the Emitter/Subscribe path
  itself; pinning an Emitter event shape to a declared Contract; the `observe_mode` advisory.

## Reviews
- **charter-guardian, pass 1 — six seam points (2026-07-16): CHANGES REQUIRED → folded.** Shape sound
  (projects the charter-clean in-tree `Interpreted` seam); the fixes (relations no-vivify + target gating;
  per-verb projector invariant; outputs/match never-persisted; the DerivedContract rung-ladder guardrails a–e;
  ArtifactRef verify-don't-trust; ItemResult fold) are recorded as the binding discipline (§§1–6).
- **charter-guardian, pass 2 — growth machinery (2026-07-16): CHANGES REQUIRED → folded.** The `extensions`
  Struct was a RECONSIDER (an untyped bag on the typed Envelope violates invariant #1, its guardrails are
  convention not data-layer, and it is redundant with additive typed fields + the opaque Payload) — **removed**
  (§7c). Capabilities became a core-owned versioned registry, never precedence-bearing, with a CI-gated N-1
  protocol floor (§7a); plan-as-artifact got explicit Gate-binds-hash / core-verifies-at-boundary TOCTOU
  closure (§7); provisioned CredentialRef got namespace-confinement + no-overwrite + core-stamped binding
  (§7); the async and scope-label non-additions got their justification/guardrail fixes.
- **vocabulary-linter (2026-07-16): CLEAN — no edits.** No banned terms; every new identifier reuses a frozen
  Named Kind (Action/Relation/Contract/Entity/Bundle) or is a carrying/structural type (Result/Fragment/Ref/
  Decl), never a rival Kind. `DerivedContract` reads as "a Contract at a derived rung" (guarded by the Rung
  enum); `ArtifactRef` is a Bundle/OCI pointer; `ActionDecl`/`InvokeRequest.action` use `Action` as a selector.
- **Gated on steward sign-off** before extraction begins — this freezes the v1 wire contract for every plugin.
