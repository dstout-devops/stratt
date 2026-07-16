# ADR 0044 — Control-plane Cells / multi-region (partitioned single-writer, one logical estate)

- **Status:** Accepted (design authority for the multi-slice Cells workstream; this ADR pins the COMPLETE
  architecture — later slices implement it without cutting corners). **Slice 1 (this commit):** the Cell as
  a first-class modeled concept + identity + provenance + collision-safe naming, backward-compatible as one
  Cell `local`.
- **Date:** 2026-07-15
- **Deciders:** Project steward (dstout)
- **Charter sections:** §0 (one typed estate graph), §1.2 (projection, not a second truth), §1.3 (rug-pull-
  proof — no gated tier), §1.4 (boring spine), §1.6 (one Principal/authz/audit/cost model), §1.8 (never hide
  failure), §2.1/§2.4 (exactly one answer / no implicit precedence / anti-GPO), §2.3 (Sites; and the new
  **Cell** Named Kind added to §2 alongside Site); realizes ADR-0040 §4 ("live/live/live in aggregate — cells
  + Sites", designed-there-built-here) and mirrors the ADR-0032 Site machinery (`mgmt.site` residency Facet,
  `ResolveTargetsBySite` fan-out, `run.sites`) one level up.

## §2 vocabulary addition (Cell — a new Named Kind)

Cell is admitted as a **new, adjacent** Named Kind. **Site's §2.3 definition is untouched** — Site remains an
execution locus; Cell is the control-plane shard that *contains* Sites. The frozen one-line §2.3 addition
(applied to `stratt-charter.md` by the steward at the highest review bar):

> **Cell** — a region-local, single-writer control-plane shard (its own boring-spine substrate). The fleet is
> many Cells presenting one logical estate, active/active across Cells with no datum multi-master; each datum
> has exactly one home Cell (§2.1). A Cell contains Sites; the built-in default is one Cell (`local`).

## Context

Stratt is one control plane today (single Postgres/NATS/Temporal/OpenFGA/object-store; no region/cell
identity). ADR-0040 rejected multi-master (Temporal is active-in-one-cluster; multi-master Postgres trades
silent LWW for write availability — a correctness regression that violates §2.1/§2.4) and deferred control-
plane cell-awareness. As the successor platform to fleet-scale estate tools (Intune/Jamf/SCCM/AWX), Stratt
needs true multi-region — done as the *most complete correct* form, not the most-available-at-any-cost form.

## Slice-2 refinements (accepted 2026-07-15 — supersede the `mgmt.cell` Facet references below)

Implementing slice 2 surfaced two refinements to the pinned design; the steward approved both:

1. **Residency is a set-once `home_cell` COLUMN on `graph.entity`, NOT a `mgmt.cell` Facet.** A Facet is
   last-writer (`ON CONFLICT DO UPDATE`), so a stray cross-Cell write would *silently overwrite* residency to
   match the writer — defeating the §2.4 placement-mismatch Finding this ADR also requires (the mismatch could
   never be observed). `home_cell` is stamped once at Entity creation (= the creating daemon's Cell), never
   touched on the correlate-UPDATE path, and mutated only by the slice-7 fenced re-home — mirroring the
   `run.cell`/`prov_cell` column precedent (not the soft, re-pointable `mgmt.site` routing hint). The slice-3
   router reads the column directly. **Everywhere below that says "`mgmt.cell` residency Facet", read
   "`home_cell` column".** `mgmt.site` (execution routing) is untouched.

2. **`Source.cell` = the registering daemon's Cell (Sources are env-registered, not CaC).** There is no CaC
   Source declaration; a Source homes to the Cell of the daemon whose `Register()` created it. Entity-inherits-
   Source-cell then holds by construction (the same daemon projects the Entity, stamping `home_cell`). The §2.4
   authority check compares an Entity's `home_cell` against the Cells of the Sources observing it (via the
   ADR-0042 `entity_presence` set) — a divergence is a cross-Cell identity collision (the multi-master
   condition) and raises a `framework='placement'`, `severity='critical'` Finding; it resolves when the
   collision clears (`placement-reconciled`) or the Entity is tombstoned (`entity-tombstoned`, ADR-0043).

**Slice-2 implementation-sequencing (deferred where the consumer/test lives, not corners cut — the design
above is unchanged):** the `run.cells` *touched-union* population lands with slice 5 (cross-Cell orchestration,
where a fan-out actually touches multiple Cells and descent consumes it); the `KindCell` CaC loader (declaring
`graph.cell` rows from Git) lands with slice 3 (its consumer is the federation router's peer-endpoint set).
Slice 2 ships the residency/homing data model + placement Findings + `Source.cell`/`run.cell` + `siteFile.cell`
+ the `SetRunCells`/`HomeCellsByEntities` plumbing those later slices consume.

## Slice-3 refinements (accepted 2026-07-15)

Slice 3 = the read-federation `cellrouter`. Steward-approved refinements to the pinned design:

1. **Scope = READ federation; WRITE home-forwarding moves to slice 5.** Cross-Cell writes are vacuous today —
   Syncers write their own Cell by construction (`source.cell`), Runs home to the launching Cell (`run.cell`),
   CaC is partitioned per-Cell, and cross-Cell run *launch* is `RunAcrossCells` (slice 5). Slice-2 placement
   Findings already enforce the no-multi-master guarantee *observably*. A write-forwarder built now would be
   dead code. So slice 3 ships read federation + the shared foundation; write home-forwarding lands with slice 5.
2. **Cross-Cell auth = forward the caller's token.** The `cellrouter` replays the caller's inbound
   `Authorization: Bearer` (or the dev `X-Stratt-Principal`) verbatim on peer calls, so the peer's identical
   `ResolvePrincipal` re-derives the **same Principal** — cross-Cell authz + audit attribute to the *user*, not
   the Cell (§1.6 one-Principal; zero new primitive). Guardrails: forward ONLY to CaC-declared
   `graph.cell.endpoint` (never a caller-supplied address); require a shared OIDC issuer/audience across Cells
   (a token that can't verify at the peer fails the peer call, surfaced as unreachable — never silent); the
   token is request-scoped, never persisted or logged (§2.5). Service-identity rejected (it would collapse
   per-user authz + audit to per-Cell). MCP carries the identity only in context, so `mcpserver.invoke` sets
   the forwardable headers on its in-process request for uniform forwarding.
3. **Partial-result honesty = HTTP 206 + named headers, no body envelope.** Every read body + the oapi contract
   stay unchanged; partial-ness rides `X-Stratt-Cells-Queried` / `X-Stratt-Cells-Unreachable` (named, never
   dropped) and — critically — **HTTP 206** when a Cell is unreachable, so even a header-ignoring client sees a
   non-200 (§1.8 teeth). MCP folds the unreachable set into the tool-result envelope note so agents see the gap
   in-band. A UI/CLI 206 renderer is a fast-follow (the honest signal ships now).
4. **The cellrouter wraps the generated router ONCE**, used by both `/api/v1` and MCP (so MCP `list_*`/`get_*`
   federate for free). Classification is an explicit federated-route table (`/runs`, `/findings`,
   `/views/{}/entities` list reads; `/entities/{id}` point read) — everything else passes through; the merge is
   per-endpoint (id / started_at,id / last_observed,id) with a `sort` — no cross-Cell join/pushdown (§1.4). A
   fan-out call carries `X-Stratt-Cell-Fanout` so peers serve it local-only (no recursion). **Single-Cell (no
   `graph.cell` peers) is a byte-identical pass-through** (empty-peer-set short-circuit). Point read is
   local-first-then-peers: a locally-present Entity is authoritative (single-writer); a local miss asks peers.
5. **`KindCell` CaC loader lands here** (its consumer — the peer set — is now real). Sort tiebreaks (`, id ASC`
   on `ListRuns`/`ListFindings`, backed by composite indexes) make the cross-Cell merge total-order deterministic.

**Slice-3 known limitations (tracked, charter-guardian flags):**
- **§1.5 cross-Cell schema-skew gating is not yet enforced** — the merge unions peer JSON. A peer on a divergent
  Facet/Contract registry whose body doesn't parse as the expected array now surfaces as a **206 (partial),
  never a silent union** (the merge-failure path is honest); explicit registry-version gating (block the merge
  on a version mismatch) lands with the global-registry work (slice 4), before a second Cell with a divergent
  registry is declarable.
- **`X-Stratt-Cell-Fanout` is a peer-internal control signal accepted unauthenticated at the edge.** An external
  client spoofing it only *narrows its own view* to the local Cell (no cross-Cell data leak, no authz bypass —
  the local handler still enforces authz). Closed when peer-to-peer authentication lands (companion to the
  slice-4 global authz).
- **The dev `X-Stratt-Principal` header is forwarded cross-Cell** — safe ONLY because a prod peer with
  `DevPrincipalHeader` disabled ignores it (→ anonymous → denied → named unreachable). Never enable the dev
  header on a prod peer.

## Slice-4 refinements (accepted 2026-07-16)

Slice 4 completes the §1.6 "one model" over Cells (global authz + one logical audit/cost stream) and closes
the slice-3 safety gaps. Steward-approved:

1. **authz-home Cell (the sole OpenFGA tuple writer)** = a CaC `authzHome` flag on the Cell registry
   (`types.Cell.AuthzHome`, `graph.cell.authz_home`, `cells/*.yaml`), validated **exactly-one** across a named
   fleet at CaC compile. The daemon derives its authz-home from the **in-memory decls at boot** (not a DB read
   — races the reconcile); 'local' is authz-home only when no named Cells are declared (a 'local' daemon in a
   named fleet **loud-fails**). The gate wraps the **`SyncTuples` call itself** (which also runs at boot on
   every replica before leader election) — so only the authz-home Cell ever writes the shared store and N Cells
   can't thrash it. Changing the designation requires a restart.
2. **Peer-to-peer auth = HMAC** (`STRATT_CELL_SECRET`, fleet-wide, statebackend idiom): the router signs each
   fan-out (`X-Stratt-Cell-Auth: <ts>:<hmac(method\npath\nrawQuery\nts)>`); a fanout header **without valid auth
   → 401** (a spoof/misconfigured peer — never silently honored). 30s replay window; no secret ⇒ single-Cell,
   the inbound fanout header is stripped. Residuals (recorded follow-ups): symmetric shared secret (any Cell can
   impersonate a peer — mesh-trust; closing it needs asymmetric per-Cell keys, a larger change); no replay nonce
   within the window. **Security hardening (2026-07-16, review-driven):** the signed string now binds the
   asserted **Principal** (`method\npath\nrawQuery\n[bodyHash\n]principalID\nprincipalKind\nts`) — a replay
   within the window can no longer be re-attributed by rewriting `X-Stratt-Principal` to escalate, so the
   no-nonce residual loses its escalation value (a pure replay stays idempotent-GET / write-idempotency-key
   bounded). Separately, the **`/mcp` surface now rejects an inbound fanout header** (it is never a legitimate
   peer target and, unlike `/api/v1`, has no cellrouter HMAC gate in front of `ResolvePrincipal`) — closing an
   unauthenticated principal-impersonation path the slice-5 fan-out assertion had opened on that surface.
3. **Skew gating (§1.5/§1.6) = named `X-Stratt-Cells-Skewed` + 206**, two gates: a **discovery-time** OIDC
   issuer+audience probe of each peer's `/cellinfo` (a mismatch drops the peer AND never forwards a caller's
   token to it), and a **per-response** `X-Stratt-Registry-Version` compare (catches a mid-TTL redeploy). A
   skewed peer is NAMED, its body never unioned. `/cellinfo` is an unauthenticated, non-federated endpoint
   advertising only non-secret coordinates (cell/issuer/audience/registryVersion); the fingerprint is a sha256
   over the sorted (name,version,hash) triples of the pinned registry, stamped on every response by an
   outermost middleware (federated responses drop inner headers).
4. **Federated `/audit`** merges on **`at` DESC, (cell,seq) tiebreak** (per-Cell `seq` is not comparable); `cell`
   rides the wire (NOT the hash chain — hashing it would break `VerifyAudit` on rows already sealed). The
   federated path is **limit-only** (the cross-Cell `seq` cursor is deferred); a single-Cell estate keeps its
   `seq`-ASC cursor unchanged (an accepted merged-view §1.6 split, not a datum-model split). **Federated
   `/usage`** is a **scatter-gather-SUM** (a new `kindAggregate`: group by (principal,tool), SUM calls/errors,
   MAX lastCall) — a client-side merge over per-Cell GROUP BYs, no cross-Cell join/pushdown (§1.4), no
   truncation. **SIEM**: `cell` on the forwarded event → one SIEM dedups on `(cell,seq)`; each Cell runs its own
   forwarder to the one SIEM (deploy).

Single-Cell 'local' stays a byte-identical no-op throughout: `SyncTuples` runs as today, no HMAC signing, the
cellrouter is a pass-through, `/audit` keeps `seq`-ASC.

## Slice-5 refinements (accepted 2026-07-16)

Slice 5 turns cross-Cell READ federation into cross-Cell ORCHESTRATION: a Run can act on the whole logical
estate, a Run/Gate homed elsewhere can be cancelled/approved, and a parent Run descends into its peer-homed
children. Steward-approved:

1. **Scatter, NOT central partition (the load-bearing correction).** Entities are not replicated, so a parent
   Cell is structurally blind to peer-homed targets — `ResolveView`/`HomeCellsByEntities` query only the local
   pool. A central "partition the View's targets by home Cell" (the naïve `ResolveTargetsBySite` mirror) would
   silently drop peer targets while passing single-Cell tripwires. So **`RunAcrossCells` scatters**: a local
   child `RunAgainstView` over this Cell's home entities + one HTTP-launched child Run per peer Cell, each
   **self-scoping** because its `RunAgainstView` re-resolves the same (global CaC) View to only that Cell's home
   subset. It is the write-side mirror of slice-3 scatter-gather reads, not the one-level-up mirror of the Site
   fan-out.
2. **`StayLocal` = the recursion base case.** A forwarded child carries `RunInput.StayLocal`, which makes
   `LaunchRun` always choose `RunAgainstView` (never `RunAcrossCells`) and turns a zero-entity resolution into a
   **benign empty success** (a peer legitimately homes none of a View's targets). `LaunchRun` selects
   `RunAcrossCells` only for a direct launch with peers declared; a single-Cell estate (no peers) runs
   `RunAgainstView` byte-identically.
3. **`partial` — a new terminal RunStatus.** A cross-Cell Run where some Cells succeeded and at least one
   failed/unreachable is `partial`, never a silent green (§1.8) — the failed Cells are NAMED in the summary
   (`failedCells`) and the touched-Cell union (`run.cells`). A View that matched no entity in ANY Cell is a
   failure, not a hollow green. Only a multi-Cell `RunAcrossCells` can produce `partial`; a single-Cell Run is
   only ever succeeded/failed/canceled. The AWX façade maps `partial→failed` (AWX has no partial); the notify
   success-gate treats `partial` as not-success. `partial` is a status VALUE on the frozen Run Kind, not a new
   Named Kind.
4. **Forwarded-write identity = HMAC-verified Principal ASSERTION, resolved at the one seam.** The async child
   launch runs in a Temporal activity with NO live bearer (§2.5 — tokens never persist), so it cannot forward
   one. It asserts the acting Principal's **id** via `X-Stratt-Principal`, honored ONLY when a secret is
   configured AND the request carries the fan-out header — trusted because the cellrouter middleware 401s any
   fan-out whose HMAC does not verify before it reaches a handler. Critically, the assertion is resolved at the
   **one identity seam** (`Server.ResolvePrincipal`, §1.6), NOT injected inside the router — so the ADR-0035 SCIM
   offboarding gate still runs on the asserted Principal (a human deactivated mid-fan-out is denied) and the
   audit access-log attributes the forwarded write. The peer re-evaluates the `runner`/approver authz against the
   global OpenFGA (the assertion is identity, not authorization). No credential MATERIAL is ever forwarded — only
   CredentialRef names ride the child body, expected to be globally CaC-declared like Views. The synchronous
   point-forwards (cancel, gate decision) use the same assertion.
5. **Body-covered HMAC for writes.** The fan-out signature now folds `sha256(body)` for non-GET
   (`method\npath\nrawQuery\nsha256(body)\nts`); the GET form is byte-identical to slice 4 (read federation +
   tripwires unmoved). A tampered/replayed body can no longer launch a different Run under the forwarded
   identity.
6. **Point-forwards for `CancelRun` + `DecideGate`.** A Run/Gate lives only on its home Cell; a cancel/decision
   that landed on the wrong Cell would hit the wrong Temporal namespace (silently, if a same-named execution
   existed). Both forward to the home Cell (the home Cell re-checks the grant/approver policy; an unreachable
   home is a loud 503). A Gate is co-homed with its WorkflowRun via the new **`graph.workflow_run.cell`**
   (migration 00030, set-once at creation like `run.cell`).
7. **Descent federation.** `/runs/{id}` and `/workflow-runs/{id}` join `/entities/{id}` as `kindPoint`
   (local-if-present-else-ask-the-homing-peer-else-503), so a descent from a parent `RunAcrossCells` into a
   peer-homed child Run resolves. The parent's summary lists `childRuns` (`{cell, run, status}`) — the forward
   descent path.

**Honest deferrals (slice 5):** peer-side idempotency-key dedup for a forwarded child launch (the activity
POSTs once then polls internally, so a Temporal retry re-POSTs only when the launch itself failed; the
lost-response double-launch is the recorded residual — launch-level dedup is already a standing follow-up).
Federating the *scoped* child-list and the child event-stream tail across Cells (descent-by-listing is
answerable from the parent's `childRuns`/`run.cells` for now). Reverse child→parent linkage. The slice-4 HMAC
symmetric-secret + no-nonce residuals carry forward.

Single-Cell 'local' stays a byte-identical no-op: `len(peers)==0` runs `RunAgainstView` (never `RunAcrossCells`),
no HMAC-body path, no `run.cells` write, `partial` never arises, writes are always local.

## Slice-6 refinements (accepted 2026-07-16)

Slice 6 is cross-Cell EXECUTION wiring: the NATS dispatch/event plane is Cell-scoped end to end
(slice 1 deferred subject scoping to "where a second Cell consumes it"), and a Site is bound to its
Cell so a Run dispatches only to Sites it can actually reach. Steward-approved:

1. **One env-derived scope token, reconciled against CaC — not a DB read (the load-bearing choice).**
   A Site's `stratt-agent` has NO database, so the runtime NATS scope can only come from config both
   ends share. Hub and agent each derive the IDENTICAL token from env —
   `types.CellScopeToken(STRATT_CELL_ID, STRATT_CELL_DISPATCH_PREFIX)` — so they always exchange on
   the same subjects; a mismatch would make them silently talk past each other. `graph.cell.dispatch_
   prefix` (CaC) stays authoritative as *desired* state: the hub **loud-fails at boot** if its env-
   derived token ≠ its Cell's declared `DispatchPrefix` (`reconcileDispatchScope`, run from the same
   in-memory CaC decls as the slice-4 authz-home gate, before serving). This is §2.4 exactly-one-
   answer applied to deployment config — env is the runtime input, CaC the declaration, and a
   divergence is surfaced, never resolved by silent precedence. Rejected: reading `dispatch_prefix`
   from the DB at boot (the agent can't, so the two ends could still diverge) and dropping the CaC
   column to "informational" (a second, unenforced truth).
2. **Every NATS name is Cell-scoped, gated on `scope != ""` (LocalCell byte-identical).** Streams/KV
   suffix `_<CELL>` (`STRATT_RUN_EVENTS_<cell>`, `STRATT_DISPATCH_<cell>`, `SITE_LIVENESS_<cell>`,
   emitter + notice); subjects insert the token as the second subject token (`stratt.<cell>.run.>`,
   `stratt.<cell>.dispatch.*`, `stratt.<cell>.dispatchresult.>`). The derivation lives once in
   `types` (`ScopedStream`/`ScopedSubjectRoot`); `events.Bus` scopes its three streams at Connect,
   `siteproto.SetScope` scopes the dispatch plane's package-global names (mirror of
   `orchestrate.TaskQueue`) so its pure subject functions stay shared verbatim by hub and agent. A
   single-Cell 'local' estate keeps every stream/subject byte-identical to the pre-Cells plane.
3. **Site→Cell binding = a loud, terminal misroute (§1.8), enforced not assumed.** A Site's dispatch
   work-queue lives on its Cell's NATS, so a daemon can only reach Sites homed to its own Cell.
   `ResolveTargetsBySite` now rejects any target routed to a Site homed elsewhere
   (`SiteCellMisroute`, non-retryable) rather than silently dropping or mis-dispatching it. This is
   the enforced invariant *behind* the slice-5 scatter: `RunAcrossCells` re-resolves the View per
   Cell so each child Run only ever sees its own Cell's home entities (and thus its own Cell's
   Sites); the check makes that a guarantee, not a hope. `LocalSite` is the daemon's in-Cell central
   locus and is always reachable; an unset Site cell is co-located.
4. **The NATS leaf topology needs no subject-filter change.** A remote Site's NATS runs as a
   leaf borrowing the hub's JetStream account and forwards the whole account subject-agnostically, so
   a named Cell's scoped subjects forward with zero config change — the agent and hub only need the
   same `STRATT_CELL_ID`. (The site-local-JetStream-domain case remains the ADR-0032 deferral.)

**Honest deferrals (slice 6):** the Helm chart now templates `STRATT_CELL_ID`/
`STRATT_CELL_DISPATCH_PREFIX`/`STRATT_CELL_SECRET` from a `cell:` values block for the hub; a
per-Cell agent chart (agents are still the ADR-0032 dev manifest) and the full multi-Cell deploy
harness are slice-7 deploy/runbook. Per-Site NATS credentials at the leaf (dev is leaf-open) remain
the ADR-0032 production hardening. The cross-Cell event-stream tail is still answered by federating
at the API layer (slice 5), never by a cross-Cell NATS consumer (§1.4 — no widening the work-queue
across Cells).

Single-Cell 'local' stays a byte-identical no-op throughout: scope "" leaves every stream/subject/KV
name unchanged, `reconcileDispatchScope` has no declared Cell to reconcile, and every Site is
reachable.

## Slice-7 refinements (accepted 2026-07-16)

Slice 7 is the fenced re-home GA + per-Cell DR + failover drill — the last correctness-envelope member
(fenced re-home) and the DR/runbook/evidence that make the 99.99% multi-region claim discharge-able. A
charter-guardian DESIGN review (before any code) reshaped it materially; steward-approved:

1. **The unit of re-home is the SOURCE, not the Entity (the load-bearing correction).** An Entity is a
   projection of a Source (§1.2). Re-homing a bare Entity while its Source keeps syncing on the old Cell
   silently re-projects it there on the next cycle — a durable second writer *below* the fence, worse than
   the instant it prevents; and it breaks mechanically (`entity_presence.source_id` FK, `enforce_facet_owner`,
   a permanent critical placement Finding, and a shipped-but-unrederivable = second-truth Entity in the
   destination). So re-home seals the **Source**, the destination **re-projects** its Entities natively
   (rebuildable, `prov_cell=dest`), and the source Cell **tombstones** its now-unobserved copies. `RehomeSourceWorkflow`
   drives Seal → Adopt → Complete with a compensating Abort.
2. **The seal fence is a DB CONSTRAINT (closes residual tension #4 for the window).** Migration 00031 adds
   `graph.source.rehoming_to` (+ `home_epoch`) and extends `enforce_write_path`: a Normalizer projection
   stamping a sealed Source's `prov_source_id`/`source_id` is **rejected**. After the seal commits, the home
   Cell physically cannot keep projecting the Source — proven on real Postgres (`TestSealFenceRejectsNormalizerWrite`).
   A new `'rehome'` mover write path is exempt (it performs the seal + the tombstone).
3. **Tombstone, never hard-DELETE (must-fix 3).** Complete tombstones the Entities (`deleted_at`) and
   resolves their Findings with a distinct `resolved_reason='entity-rehomed'` (vs ADR-0043's `entity-tombstoned`)
   so descent shows the Entity moved Cells, it did not vanish — and the source Cell's Findings never linger
   open forever. The Source row is removed (its projection now belongs to the destination).
4. **Adopt is the point of no return; the epoch fences replay, not the un-seal (must-fix 4).** There is no
   cross-Postgres CAS, so "un-adopt by bumping an epoch in the other DB" is not a real fence — the Temporal
   history is the ordering authority. Abort is admissible ONLY before the Adopt activity commits; after a
   committed Adopt the workflow is roll-forward-only (retry Complete). `home_epoch` guards a stale/replayed
   adopt (idempotency).
5. **§1.8 teeth: stuck-seal Finding + dual-Cell audit (must-fix 5).** A sealed-but-not-completed Source
   (partition, unreachable destination) is frozen (zero writers — safe) AND surfaced as an open
   `framework='rehome'` Finding, auto-resolved on complete/abort. `cell.rehome` is audited on BOTH Cells'
   per-Cell hash chains — seal/complete/abort on the source, adopt on the destination — never a silent gap.
6. **§2.5: CredentialRef names only.** The adopt snapshot carries the Source's CredentialRef **name**, never
   material; the destination resolves it against its OWN Secrets. Reuses the slice-5 HMAC-body-covered,
   Principal-asserted PeerClient; the destination re-checks the `rehome` grant against the global OpenFGA.
7. **Bounded scope; full auto-cutover deferred to [ADR-0045](0045-db-driven-syncer-home-gate.md).** Syncers
   are env-instantiated, not DB-driven, so a fully-automatic destination cutover needs a Connector
   home-ownership gate (a Connector-architecture change touching every Syncer) — spun out as ADR-0045. Slice
   7 ships the correctness core; deploying/enabling the Source's Connector on the destination Cell is the
   one runbook step, and the fence guarantees no double-writer regardless of timing. DR (per-Cell replica
   promotion) is env-string repoint + the `cell-failover-drill` runbook; the 99.99% evidence map is
   `docs/evidence/multi-region-99_99.md`.

With slice 7 the **correctness envelope is closed**: fenced re-home + home-routed-loud-fail +
per-Cell-audit-federated-read + partial-result honesty all shipped; no slice permits two writers to one
datum, a silent federation drop, or a hidden audit gap. Single-Cell 'local' stays a byte-identical no-op:
no Source is ever sealed (`rehoming_to` always NULL), the seal fence is a cheap indexed miss, and re-home
loud-fails for want of a peer Cell.

## Decision (the complete architecture)

**Partitioned region-local single-writer Cells presenting ONE logical estate.** Not multi-master.

1. **Cell + homing.** Partition key = `cell`; every datum (Entity/Source/Site/Run/Intent/…) has exactly one
   **home Cell** — its sole writer, extending the existing single-writer invariants (`enforce_write_path`,
   `facet_owner` PK, single audit-sealer) from "one control plane" to "one per partition." Homing is
   **CaC-declared** (mirror of Site) with a per-Entity **`mgmt.cell` residency Facet** (exact mirror of
   `mgmt.site`: `{cell}`, unset⇒`local`, run/normalizer-written, read-only for routing). An Entity inherits
   its Source's Cell; a Run-created Entity inherits the Run's Cell.
   - **Authority rule (§2.4 anti-GPO):** CaC-declared Cell = *desired*, `mgmt.cell` = *observed*. Write-routing
     uses the CaC (desired) home. A CaC-vs-observed **mismatch raises a Finding** — never silently resolved
     (that would be implicit precedence). Placement, like Provenance, has exactly one answer.
2. **Re-homing is a FENCED two-phase move.** Moving a datum A→B uses a **fenced** lock (à la the Patroni
   fencing ADR-0040 relies on) so that during a partition the old-home and new-home Cells cannot *both*
   believe they hold write ownership. An advisory/unfenced lock is insufficient — it would reintroduce
   multi-master LWW at the worst moment. Single writer at every instant; the move is audited in both Cells.
3. **One logical estate — the `cellrouter`.** A stateless capability compiled into **every** strattd (not a
   new deployable; keeps §1.6 one-API for UI/CLI/CI/MCP). Reads scatter-gather across `graph.cell` peers with
   a **deterministic k-way merge** (reusing the replay-sort discipline already in `RoutedTargets`/`sites`),
   per-Cell `as-of` stamps, and **partial-result honesty** — an unreachable Cell is *named* in the response,
   never silently dropped (§1.8). Writes forward to the datum's **home** Cell; if the home is unreachable the
   write **fails loudly** (no failover-to-a-second-writer = no multi-master).
   - **Guardrail (§1.4):** the router is **scatter-gather + merge ONLY**. Cross-cell joins, distributed
     transactions, and query pushdown are **forbidden** — a distributed query engine would break the boring
     spine. Cross-cell Relations are **soft references** (by global Entity id, validated at the router), never
     a Postgres FK (different databases); in-cell Relations keep their FK.
4. **One Principal/authz/audit/cost (§1.6).**
   - **OIDC/Zitadel: global** (one issuer; Principals are global identities). strattd already treats OIDC as a
     stateless per-request verifier — a global issuer needs no per-Cell state.
   - **OpenFGA model + tuples: global** (one Git source, projected by the authz-home-Cell leader only, read-
     replicated per Cell; `HIGHER_CONSISTENCY` for must-be-fresh checks). Authz decisions are identical in
     every Cell.
   - **Audit: per-Cell hash-chain** (single sealer per chain — two sealers corrupt it), presented as **one
     logical stream** via federated read + one aggregated SIEM forwarder (ADR-0034). *Accepted (steward):*
     "one audit stream" = one logical/presented stream over N per-Cell tamper-evident chains; each chain is
     independently verifiable, cross-Cell order is not cryptographically linked (a single global sealer would
     put cross-region latency + shared-fate on every append — the wrong trade).
   - **Cost/usage: per-Cell attribution to the global Principal, aggregated at read.**
   - So: **identity + authz-model are globally shared** (a §1.6 requirement, with per-Cell read-replicas +
     ADR-0040 active-passive DR as the shared-fate mitigation); **graph/orchestration/execution/evidence are
     per-Cell**; **audit + cost are per-Cell-written, globally-read.**
5. **Orchestration.** Per-Cell Temporal (namespace `stratt-<cell>`, queue `stratt-runs-<cell>`) — respects
   active-in-one-cluster (a namespace never spans Cells). A Workflow spanning entities in multiple Cells runs
   a parent **`RunAcrossCells`** in the initiating Cell that partitions targets by home Cell (structural mirror
   of `ResolveTargetsBySite`, one level up), fans out **child Runs** to peer Cells' control APIs, awaits and
   merges (`RunOutcome`/`mergeResults`). `graph.run.sites` → add `graph.run.cells` (the union of Cells a Run
   touched). Cross-cell **descent** (Intent→…→Run→task-event) survives a Cell hop; an unreachable peer renders
   as a **named gap** (§1.8 — the ADR-0032 lossy-leaf disclosure, one level up).
6. **Execution.** Sites belong to a Cell (`graph.site.cell`); a Site's `sitegw` NATS work-queue lives on its
   Cell's NATS. The hop hierarchy is **cell-router/parent-workflow (cell→cell control) → cell-local orchestrate
   → sitegw (hub→Site NATS) → agent** — each layer keeps its single-writer/single-substrate assumption. Cross-
   cell is a control hop *above* Site dispatch, never a widening of the Site NATS work-queue across Cells.
7. **Schema skew (§1.5).** All Cells pin the same Facet/Contract registry version, or cross-Cell schema drift
   **blocks the merge** — schema drift is blocking, never silently absorbed into a federated union.
8. **Licensing (§1.3).** All Cell/multi-region/homing/routing code is in the **Apache-2.0 core, never `ee/`**.
   Multi-region affinity is the single most common capability commercial OSS gates behind an enterprise tier —
   the exact rug-pull §1.3 forbids. Cells are never a gated surface.
9. **Identity plumbing.** `STRATT_CELL_ID` (default `local`) is the daemon's own Cell id, stamped into write
   provenance (`Provenance.Cell`) and — for a named Cell — into the collision-prone shared-name control
   resources: leader lease (`strattd-leader-<cell>`), Temporal namespace/queue, and NATS stream/subject
   prefixes. The daemon **never self-registers** into `graph.cell`: that registry is CaC-declared (sole writer
   = the desired-state engine, mirroring Site) — a self-writing daemon would be a second writer to a projection
   (§1.2). All shared-name stamping is **gated on `cell != "local"`** so today's single-Cell deployment is
   byte-identical (namespace `default`, queue `stratt-runs`, lease `strattd-leader`, unprefixed subjects). The
   slice sequence below is the authority on *what lands when* (provenance + lease + Temporal namespace/queue in
   slice 1; NATS-subject scoping in slice 6 where a second Cell consumes it — **landed**, via a single
   env-derived scope token shared by hub and agent and reconciled loud against CaC `dispatch_prefix`).
10. **Substrate HA/DR is deploy/runbook** (per-Cell in-region quorum HA + async cross-region DR replica /
    Temporal XDC / NATS mirror / object CRR — endpoints are already env strings). Cell failover promotes a
    Cell's DR replica set — a *within-Cell* DR event, bounded blast radius (the cell doctrine), human-authorized
    (no auto-flip on transient partition). The **code** is identity/homing/routing/cross-cell-orchestration.

## The correctness envelope (sequencing invariant, §1.8)

Fenced re-home + home-routed-loud-fail + per-Cell-audit-federated-read + partial-result honesty are a **single
atomic correctness envelope** that MUST land *before or with* the first slice where a second Cell owns real
data — never later polish. **No intermediate slice may permit two writers to one datum, a silent federation
drop, or a hidden audit gap.** Slice 1 (single Cell `local`) is safe to ship alone precisely because one Cell
cannot split-brain.

## Slice sequence (pinned — each a shippable increment; `local` keeps earlier slices no-ops)

1. **Cell as a modeled concept (this slice):** `STRATT_CELL_ID` + `graph.cell` registry (CaC-written; CRUD in
   place) + `Provenance.Cell` (`prov_cell` stamped) + homing columns (`site.cell`/`source.cell`/`run.cell`+
   `cells`, `audit.event.cell`) + collision-safe control naming (lease, Temporal namespace/queue), all gated on
   `cell != local`. NATS-subject scoping (slice 6) and the reusable-fan-out / `homeCell` seams (slices 2/3/5)
   are deferred to where a consumer + test exist — not shipped as unconsumed plumbing here.
2. **Homing semantics:** `mgmt.cell` Facet, Entity-inherits-Source-cell, CaC-vs-observed authority rule +
   **Finding-on-mismatch**, `graph.run.cell` home computation.
3. **`cellrouter` federation:** scatter-gather reads + deterministic merge + per-Cell `as-of` + partial-result
   honesty; home-Cell write forwarding; `graph.cell` globally replicated.
4. **Global identity/authz + federated audit/cost:** global OIDC/OpenFGA (authz-home-Cell leader sync); per-
   Cell audit chains + federated `ListAudit` merge + aggregated SIEM forwarder; per-identity cost aggregation.
5. **Cross-cell orchestration (landed 2026-07-16):** `RunAcrossCells` (a **scatter** — see slice-5
   refinements — not the naïve central `ResolveTargetsByCell` partition, which is blind to peer-homed targets) +
   child-Run fan-out to peer control APIs + `graph.run.cells` union + the `partial` RunStatus + write
   home-forwarding (cancel/gate) + `/runs/{id}` descent federation.
6. **Cross-cell execution wiring (landed 2026-07-16):** Cell-scoped NATS fully cut over (run-event,
   emitter, notice, dispatch, result streams + subjects + liveness KV, all gated on `scope != ""`);
   env-derived scope token reconciled loud against CaC `dispatch_prefix`; Site→Cell binding
   (`SiteCellMisroute`); hub + agent scope from one shared env derivation — see slice-6 refinements.
7. **Per-Cell DR + fenced re-home GA + failover drill (landed 2026-07-16):** Source-granular fenced re-home
   (`RehomeSourceWorkflow`: seal → adopt → tombstone, DB seal fence in `enforce_write_path`, migration
   00031), stuck-seal Finding + dual-Cell `cell.rehome` audit, `entity-rehomed` tombstone reason; DR =
   env-string repoint + the `cell-failover-drill` runbook; 99.99% evidence map. Closes the correctness
   envelope. Full auto-cutover deferred to ADR-0045. See slice-7 refinements.

## Charter reconciliation

- **§2.1/§2.4** single-writer homing + no-multi-master + fenced re-home + CaC-authority-with-Finding-on-mismatch
  — "exactly one answer" preserved and *strengthened* per Cell; no LWW anywhere.
- **§1.6** one Principal (global OIDC), one authz model (global OpenFGA from one Git source), one *logical*
  audit stream (per-Cell chains, federated read) + one cost model, one API via the router — identical for
  UI/CLI/CI/MCP.
- **§0/§1.2** one *logical* graph, physically partitioned, globally queryable; each Cell's write-path
  invariants unchanged; rebuildable per Cell — not a second truth.
- **§1.4** zero new dependencies (same boring spine per Cell; a boring Go scatter-gather router; no distributed
  query engine). **§1.3** Apache-2.0 core, never gated.
- **§1.8** partial-result honesty + named-gap cross-Cell descent; nothing hidden.

## Residual tensions (steward-accepted)

1. **Federated cross-Cell reads are eventually consistent** (a global `/entities` unions per-Cell snapshots).
   A globally-linearizable estate read is not offered (CAP/PACELC — ADR-0040 already accepts this currency);
   single-Cell reads stay strongly consistent; per-Cell `as-of` + named-unreachable make it honest.
2. **Global OpenFGA + global OIDC are shared-fate** — the price of §1.6 "one model", mitigated by per-Cell
   read-replication + active-passive DR.
3. **No cross-Cell referential integrity in Postgres** — cross-Cell Relations are soft references (validated
   at the router), inherent to sharding.
4. **A brief fenced-re-home window and the ADR-0040 two-leader lease overlap** are the only moments single-
   writer leans on protocol rather than a DB constraint; the ADR-0034 expected-prev-hash CAS follow-up hardens
   the audit case.

## Reviews

- **charter-guardian:** direction **SOUND**; its must-fixes (fenced re-home, CaC-vs-observed authority +
  Finding-on-mismatch, correctness-envelope sequencing, no-distributed-query guardrail, blocking schema skew,
  core-not-`ee/`, per-Cell-audit acceptance) are captured above; **Cell** admitted as a §2 Named Kind (Site
  untouched). Re-reviewed against this text + the slice-1 diff.
- **vocabulary-linter:** Cell used consistently as a Named Kind; `graph.cell`/`prov_cell`/`mgmt.cell` clean;
  no banned term.
- **No dependency-scout** — zero new dependencies.
- **Slice-6 charter-guardian:** direction **SOUND**, no must-fixes — §2.4 env-vs-CaC reconcile is genuinely
  no-silent-precedence (loud boot-fail, not a winner-pick), §1.8 misroute + reconcile failures are loud and
  terminal, §1.2 `dispatch_prefix` is a validated CaC declaration not a second truth, §1.4 pure-string scoping
  (zero deps, leaf forwards subject-agnostically — no cross-Cell work-queue widening), §1.3 all in Apache
  `core/`+`types/`+`deploy/charts`. Its should-fix flags actioned: **#1** a NATS-safe charset gate on the scope
  token (`types.ValidCellScopeToken`) now loud-fails at CaC compile (`ValidateCell`) AND at boot in both mains
  before any stream is created — a stray `.`/wildcard can no longer silently reshape the subject topology;
  **#2** `stratt-agent` now logs the resolved `natsScope` so a hub/agent scope mismatch is diagnosable directly.
  Flag #3 (an inert orphan stream if the *env-vs-CaC* reconcile fails after streams are created) is left as an
  operational note — it requires a genuine misconfig fix regardless, and the charset gate pre-empts the common
  typo case before any stream exists.
- **Slice-6 vocabulary-linter:** CLEAN — `CellScopeToken`/`ScopedStream`/`SetScope`/`SiteCellMisroute`/
  `graph.cell.dispatch_prefix` and the `stratt.<cell>.*` subjects all use Cell as the frozen Named Kind (Cell
  is the control-plane shard, Site the execution locus — never blurred); no banned term.
- **Slice-7 charter-guardian (DESIGN review, pre-code — the highest bar):** returned CHANGES-REQUIRED and
  caught the load-bearing flaw on paper — re-homing an Entity independent of its Source reintroduces a silent
  durable second writer and a second truth. Its must-fixes are the slice-7 refinements above (Source is the
  unit; DB seal fence; tombstone-not-DELETE with `entity-rehomed`; Adopt = point of no return; stuck-seal
  Finding + complete/abort audit; CredentialRef-names-only). The IMPLEMENTATION review returned PASS — all
  seven embodied, correctness envelope CONFIRMED — with two should-fixes (surfaced the swallowed adopt-audit
  error; stamped the recording Cell) applied. **Slice-7 vocabulary-linter:** CLEAN.

## Consequences

Cell exists as a first-class concept; the complete multi-region architecture is pinned so no later slice cuts a
corner. Slice 1 ships the modeled concept + identity + provenance + homing columns + collision-safe naming, a
no-op for the single `local` Cell (today's deployment byte-identical), with the seams later slices plug
federation, homing semantics, cross-Cell orchestration, and per-Cell DR into — building toward true multi-region
active/active-across-Cells with no datum multi-master.
