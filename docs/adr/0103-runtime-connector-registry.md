# ADR 0103 — Runtime Connector registry: enable/disable Connectors without a strattd restart

- **Status:** **Proposed** (2026-07-23, steward)
- **Date:** 2026-07-23
- **Deciders:** Project steward (dstout)
- **Charter sections:** §2.2 (Sources & Connectors — the Connector is the versioned integration package
  binding a Source) · §1.2 (projections never a second truth — the enabled-Connector set is DESIRED
  state, reconciled like Triggers; the Source projection stays rebuildable) · §2.4 (no implicit
  precedence / single-writer — the Connector CaC owns only the desired half of a Source; Cell/homing stay
  runtime) · §1.5 (sovereign contract, multiple transports — a Connector's transport is a plugin, ADR-0046)
  · §1.6 / §1.8 (one authz + one observable surface across UI/CLI/MCP) · §7.3 (Connector registrations as
  CaC). Builds on ADR-0046 (sovereign plugin port), ADR-0044/0045 (Cells / home-gate), ADR-0060
  (multi-source facet ownership), ADR-0102 (this is its named Phase-2 boundary).

## Context

strattd wires all 19 plugins from `STRATT_<NAME>_PLUGIN_ADDR` env in one `run(ctx)` function
(`core/cmd/strattd/main.go`): each is a hand-written `if addr != "" {…}` block that dials gRPC
(`defer conn.Close()` fires only at process exit), builds a `pluginhost.Grant` inline, and registers into
either the `controllers` slice (Syncers, drained once at boot, leader-gated) or the `pluginActuators`/
`pluginActions` maps (Actuators/Actions). **Enabling or disabling a plugin therefore requires a strattd
restart** — the gap ADR-0102 explicitly booked as Phase 2.

Two facts make a runtime registry tractable without a rewrite:

1. **The CaC payload already exists** as `pluginhost.Grant` (`grant.go:33-62`) — the operator-declared
   authority (Source binding, facet/label ownership, identity schemes, tier, emitter). Today it is
   assembled from env; a declaration just parses it from Git.
2. **The actuator/action maps are already live-shared** with the Temporal `orchestrate.Activities` struct
   (Go maps are reference types; actuators registered *after* the struct is built at `main.go:940` are
   already visible at Run time). So a runtime-added Actuator becomes visible to new Runs the instant it
   lands in the map — the missing pieces are *synchronization* and *connection/controller lifecycle*, not
   a new dispatch path.

## Decision

Introduce **two** CaC desired-state Kinds, matching the charter's two **peer, permanent** Named Kinds
(§2.2/§2.3 — "this distinction is deliberate and permanent"), each modeled **exactly on `Trigger`**
(CaC-only, reconcile engine is sole writer, projected to a table, surfaced read-only via REST + MCP):

- **`Connector`** — the versioned integration package that **binds a Source** (Syncer/Action/Emitter,
  §2.2). Carries the `pluginhost.Grant` desired half (Source `Kind/Name/Endpoint/CredentialRef`, facet/
  label/identity/tombstone allowlists, emitter, tier) + a dial `Address`. Projected to `graph.connector`,
  surfaced at `/connectors[/{name}]` + `connector:<name>` authz. `ValidateConnector` applies (validates the
  Source, rejects runtime homing). This slice migrates the **`declared`** Syncer.
- **`Actuator`** — an execution-engine plugin that **runs tool content** (helm, opentofu, ansible, script,
  mcp), §2.3. It has **no Source** and **no facet/label ownership**. Carries name, dial `Address` (or
  EE-Job command), tier, the Action names it exposes (e.g. `helm/deploy`), dry-run capability. Projected to
  `graph.actuator`, surfaced at `/actuators[/{name}]` + `actuator:<name>` authz. This slice migrates the
  **`helm`** Actuator.

A single **runtime registry** reconciles both declared sets against a live set of dialed plugins,
enabling/disabling each with no restart. Never "plugin" (a transport term) as a Kind; never `Source` (the
SoR noun). "Connector" is not a synonym for "Actuator" — folding one into the other is a §2 violation.

**Scope (this ADR / first slice):** *register/dial only* — a Connector/Actuator registers + dials an
already-running endpoint (the pod is deployed separately, via helm values or the ADR-0102 self-deploy
loop); coupling enable to the workload deploy is a deferred follow-up. The registry coexists with the 17
remaining boot-env plugin blocks (strangler); exactly **`helm`** (Actuator Kind) + **`declared`**
(Connector/Syncer Kind) are migrated onto it — proving both the every-replica map path (Actuator) and the
leader-only supervised path (Connector Syncer). **Emitter** Connectors are out of scope; the Connector
`Class` reserves but does not yet accept `emitter`.

### Binding design invariants

- **D1 — Synchronization.** The actuator/action maps move behind one `orchestrate.PluginRegistry`
  (`sync.RWMutex` + accessors). `Activities` holds `*PluginRegistry`; boot-env and the runtime registry
  both write through it. Concurrent runtime write vs worker read must be a `go test -race`-clean path
  (today it is an unsynchronized data race waiting to happen once anything writes at runtime).
- **D2 — Disable is register/dial teardown ONLY, and differs by Kind.** A **Connector (Syncer)** disable:
  cancel the loop, deregister the plugin's *ownership grant rows* (`facet_owner`/`label_owner` by
  `owner_ref`), close the conn. It does **NOT** delete the `graph.source` row or tombstone Entities —
  `Cell`/`HomeEpoch`/`RehomingTo` and Entity tombstoning are the home-gate/re-home reconciler's
  single-writer domain (§2.4); the Source projection is rebuildable (§1.2). An **Actuator** disable is
  simpler still — it has no Source and no ownership: drop from the actuator/action dispatch map, close the
  conn. `DeregisterSource` **will be added** as a primitive for an explicit tombstone/re-home path but is
  not called on disable.
- **D3 — Replica-vs-leader split (this is *why* they are different Kinds).** Temporal workers run on
  *every* replica but `controllers` are leader-gated. **Actuators** (and any Connector Action capability)
  do no graph writes → their dispatch-map membership reconciles on **every replica** (idempotent, exactly
  as boot-env actuators are inline on all replicas), or an activity on a non-leader worker would hit
  "actuator not found." **Connector Syncers** are graph writers → they reconcile **leader-only**,
  dial+Register+run under `homegate.Supervise` (single-writer, home-gated). The reconcile axis is
  *per-capability* — writes-the-graph-or-not: a mixed Connector (Syncer + Action) straddles it, its Action
  registered every-replica and its Syncer supervised leader-only. The Kind boundary is a close proxy for
  that axis (Actuators + Connector Actions on the every-replica side; Connector Syncers leader-only), not
  identical to it. **Cross-Kind exclusivity:** the shared `PluginRegistry` name check spans both Kinds — a
  `Connector` and an `Actuator` declared under the same dispatch name collide (§2.4), fail-closed and
  observable (D6).
- **D4 — Collision downgrade, surfaced (§1.8).** The §2.4 exclusive-name check stays an error on
  `PluginRegistry.Register*` (incumbent wins — no last-writer/connection-order tiebreak); boot-env callers
  still crash loud. The runtime registry **rejects + surfaces** a colliding declaration rather than
  crashing the daemon — but the reject is NOT a silent log: it is observable per D6. (The charter's own
  precedent for an exclusive-name collision is a surfaced Finding, `FindingHomeCollision`, never a hidden
  tiebreak.)
- **D6 — Enable failures are observable, not silent (§1.8).** A rejected declaration (D4) or a failed
  enable (dial error, manifest-identity mismatch) must be **queryable in this slice**, not deferred: the
  registry records a per-declaration runtime status (last enable outcome + error) surfaced on
  `GET /connectors/{name}` and `GET /actuators/{name}`. An operator who declared a Connector that silently
  isn't running can see *why* through the one read surface (UI/CLI/MCP) — hiding failure is forbidden.
  (A static registration collision is a §2.4 registration error, not an Entity-scoped Finding, so the
  queryable status is the correct surface here — the `FindingHomeCollision` precedent is *analogized* for
  the "surface, never silently tiebreak" principle, not literally followed.)
- **D5 — Coexistence.** The registry manages only CaC `Connector` Kinds. The two migrated plugins have
  their boot-env blocks deleted; the other 17 keep theirs. A runtime Connector colliding with a surviving
  boot-env plugin (name or Source) is rejected (D4), never a double-registration.
- **The §2.4 Source boundary (load-bearing).** `ValidateConnector` **rejects** any Connector whose
  `Source` carries `Cell`/`HomeEpoch`/`RehomingTo`; the CaC owns only `Kind/Name/Endpoint/CredentialRef`
  (+ the grant allowlists). `RegisterSource` keeps deriving `Cell`/`HomeEpoch` from the registering daemon.
  A Connector must never set homing, or CaC becomes a second truth racing the fleet fence.

## Charter alignment

- **§2.2 / §7.3** — the enable/disable unit is the **Connector** (binds a Source; Syncer/Action/Emitter),
  the charter's own name for a registry CaC entry. The plugin is only its transport (§1.5, ADR-0046).
- **§1.2** — the enabled-Connector set is *desired* state (operator-declared), the same class as Triggers/
  Views; it is reconciled from Git, not observed. It is NOT a projected Entity (that would be a second
  truth). The Source it binds remains a rebuildable projection.
- **§2.4** — the Connector owns only the desired half of a Source; homing stays single-writer; the
  exclusive-name/authority collisions fail (or reject) rather than silently tiebreak.
- **§1.6 / §1.8** — one authz gate (`requireGrant(reader, connector:<name>)` / `actuator:<name>`) and one
  read surface fanned identically to UI/CLI/MCP; a Connector's/Actuator's state — including a failed enable
  (D6) — is observable, not hidden.
- **§2.3** — `Actuator` stays the distinct, permanent Named Kind for tool-content execution engines (helm,
  opentofu, ansible, script, mcp); it is declared and reconciled *separately* from `Connector`, never
  folded into it.

## Consequences

- **Positive:** plugins/services become enable/disable-by-declaration with **no restart**; the boot-env
  spine is stranglable incrementally (17 stay until migrated); the actuator/action map race is closed for
  good; the Connector Kind gives the estate a first-class, queryable list of what integrations are wired.
- **Negative / trade-offs:** new load-bearing concurrency (the map race — mitigated by D1 + `-race` gates)
  and a new controller lifecycle (per-connector cancel + Deregister — new code paths against the
  previously additive-only ownership rows). Disable intentionally leaves the Source projection + Entities
  (D2), so a disabled Connector's stale Entities age out via normal sync boundaries rather than
  vanishing — the honest, §2.4-safe choice.
- **Deferred:** migrating the other 17 plugins; **Emitter** Connectors (`Class` reserves `emitter`);
  unifying enable with the workload deploy (declare → deploy → register); a graph **Finding** for a failed
  enable (D6 ships the queryable status now; a Finding for alerting is the follow-up).

## Alternatives considered

- **atomic copy-on-write for the maps instead of RWMutex.** Rejected — forces a map clone per registration
  and a combined two-map snapshot for no measurable read win (reads are once-per-Step, not a hot loop);
  RWMutex is the smaller, idiomatic change preserving today's reference-sharing.
- **Register runtime actuators leader-only (like syncers).** Rejected — a routing hazard: workers run on
  all replicas, so a non-leader worker would not find the actuator (D3).
- **Delete the Source row / tombstone Entities on disable.** Rejected — races the home-gate single-writer
  fence and makes CaC a second truth on placement (§2.4/§1.2); disable is grant-teardown only (D2).
- **Couple enable to the helm self-deploy now (declare → deploy → register).** Deferred — bigger, couples
  the registry to the Actuator loop; register/dial-only is the clean first slice (steward decision).
- **Model plugins as graph Entities.** Rejected — the enabled set is desired state, not an observation;
  Entities are Normalizer/Run-provenance-written projections (§1.2).
- **A single `Connector` Kind covering Actuators too (`Class: syncer|actuator|action`).** Rejected as a §2
  violation — `Connector` and `Actuator` are peer, permanent Named Kinds (§2.2/§2.3) with different
  contract/drift/sandboxing semantics; a Connector binds a Source, an Actuator does not, so validating
  helm through `ValidateConnector` (which validates a Source) is a category error. Two Kinds it is; they
  share the runtime registry mechanism, not the Kind.
