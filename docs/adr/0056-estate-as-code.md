# ADR 0056 — Estate-as-Code: declaring Sources & Connectors in Git + the `stratt` estate CLI

- **Status:** Proposed
- **Date:** 2026-07-17
- **Deciders:** steward (dstout), charter-guardian
- **Charter sections:** §1.2, §1.4, §2, §2.4, §2.5, §5, §8
- **Frames under:** [ADR-0055](0055-estate-composition.md) (Estate Composition)

## Context

The desired-state reconcile already covers **15 CaC kinds** (Views, Intents/Assignments/Blueprints, Baselines,
Triggers, Workflows, CredentialRefs, Sites, Cells, Emitters, NotifySinks, Subscriptions, SCIM, MCPServers) +
`authz/tuples.yaml`, with the Intent compiler running each pass (`core/internal/desiredstate/controller.go`).
But the one thing that decides **where Entities come from** — the Source + Connector binding — is imperative
env wiring in `core/cmd/strattd/main.go:657–855` (one near-identical block per connector), invisible to Git
review and un-plannable by the `stratt` CLI. `registry.go:362` even carries a "future CaC" comment against it.
Consequences: the rich `deploy/dev/examples/**` trees are reconciled by **nothing** (inert), the turnkey stack
reconciles a 2-View stub, and only Observe (vcenter←vcsim) + Apply (ansible/script) are proven end-to-end.

So the estate is only **half** declarative: policy (Views/Intents/Baselines) is CaC, but the Sources that policy
selects over are not. This ADR is the Git-declarable **foundation** ADR-0055 requires: make the whole estate —
including which Connectors run and what each is authoritative for — one reconciled Git artifact.

## Decision

**1. A new `sources/` declaration directory** whose documents are a `types.Source` plus the connector authority
fields the internal `pluginhost.Grant` Go type already assembles from env (`grant.go` documents it as
"operator-declared Config-as-Code authority… the single source of truth for ownership, the Source binding, and
identity schemes"). It reconciles like every other kind (`parseKind`, `ComputePlan`, `Apply`, prune-guard).
`sources/` is a **directory**, not a new Kind — it uses the frozen Named Kinds **Source** and **Connector** (the
same move as `credential-refs/`). The CaC YAML surface exposes these under a `connector:` key and **never a
`grant` keyword** — "grant" is reserved for the authz plane (§2.5 `use`-grant) and must not be overloaded as a
Source↔Connector noun; `pluginhost.Grant` stays an internal implementation type, not a vocabulary term.

**2. Source-of-truth is expressed at Facet/identity granularity — NEVER a scalar `Kind → owner` field.** This
is a binding invariant. A host is legitimately projected by vCenter *and* Chef, merging on `dns.fqdn`; the
correct, existing model is per-Facet-namespace ownership (§2.1 registry) + tier-gated identity-scheme merge
(`grant.allowsIdentity`). A `sources/` doc declares `facetNamespaces` / `identitySchemes` / `labelKeys`, never
an owner scalar. A scalar-precedence field is a compile error the guardian and `ValidateSource` reject (§2.4
no implicit precedence). And two `sources/` docs claiming the **same Facet namespace** is a **plan-time failure**
(the §2.1 "one namespace, one write-owner" rule the data layer already enforces as `ErrOwnerConflict` — surfaced
at CaC time, never a silent runtime write-fight).

```yaml
# estate/sources/vcenter-dev.yaml
name: vcenter-dev
kind: vcenter
endpoint: https://vcsim:8989/sdk        # locator only, NEVER material (§2.5)
credentialRef: vcenter-dev-creds        # pointer into credential-refs/
connector:
  tier: trusted
  pluginRef: vcenter                     # resolves to the Helm-provisioned pod Service (STRATT_VCENTER_PLUGIN_ADDR)
  interval: 30s
  facetNamespaces: [vm.config, vm.runtime, net.guest]
  labelKeys:        [vcenter.name]
  identitySchemes:  [vcenter.uuid, dns.fqdn]     # dns.fqdn honored iff tier=trusted
  tombstoneSchemes: [vcenter.uuid]                # ⊆ identitySchemes
```

**3. Reconcile-driven connector lifecycle.** A `ConnectorManager` (`core/internal/desiredstate/connectors.go`),
keyed on a declaration hash, starts/restarts/stops `pluginhost.Host` supervisors from the declared set via the
same `homegate.Supervise(host.Register, host.SyncLoop)` path env wiring uses today — fed from Git instead of
`os.Getenv`. The Controller calls it after `Apply`, before `compile`.

**4. Pod-vs-binding split, dual-read cutover (no flag-day).** The plugin **pod** (Deployment/Service/
`STRATT_<NAME>_PLUGIN_ADDR`) stays 100% Helm (the `plugins:` chart feature — untouched). The **binding**
(endpoint, credentialRef, tier, facet/label/identity/tombstone schemes, interval) moves to CaC. A Source
declared in `sources/` **supersedes** the env block of the same `Source.Name`; undeclared connectors keep env
wiring until every connector is declared, at which point the per-connector env blocks are deleted.

**5. A file/Git-backed static-inventory Connector** (`plugins/staticinv`) — a real Syncer over the port whose
system-of-record is a host-list file in the estate repo (`estate/hosts/*.yaml`, keyword `hosts:`). It is the
charter-clean answer to "devices via code": it writes only through `NormalizerProjector.UpsertEntities` with
`WriterSyncer` provenance (the sole legal §1.2 seam, already proven by `stratt-dev-seed`), and the file is the
authoritative external SoR (Stratt projects it, never writes back). No create-device API; the graph stays
rebuildable. **The host-list file schema is the `plugins/staticinv` Connector's own boundary Contract projecting
into named Facets (§1.1) — NOT a generic whole-host document schema; "devices via code" must never become a
universal host ontology.** **Removing a host from the file does NOT silently tombstone its Entities**: it raises
an orphan/decommission **Finding**, max-delta-gated (§4.3), so a Git edit can never silently delete estate — a
decommission is a deliberate §2.4 decision, not a reconcile delete (consistent with the follow-up below).

**6. Grow the existing `stratt` CLI** (no new binary, §1.4). `plan`/`apply` already render a terraform-shaped
`+/~/-` diff via `/api/v1/desired-state/{plan,apply}`; extend the render + wire path to cover `sources/`, and
add `diff`/`drift` + `--dry-run`. Consolidate `deploy/dev/examples/**` into one reconciled `estate/` the turnkey
stack applies.

## Charter alignment

- **§1.2 — not a writable CMDB, not a second truth.** `sources/` adds routing/authority *metadata*, not an
  Entity store. Every Entity still arrives via a Normalizer/Run-provenance write; the external system (or the
  static-inventory *file*) stays authoritative. No create-device API is introduced.
- **§2 — frozen vocabulary.** Uses Source/Connector Named Kinds only; new *directory* `sources/`, not a new
  Kind. `vocabulary-linter` gates synonyms (`inventory`/`provider`/`binding`/`registration`/`resource`) and the
  overloaded `grant` noun in keys/dirs/CLI nouns/DB/API, and keeps the host-list keyword `hosts:`. "Binding" and
  "grant" remain descriptive prose only — never an identifier.
- **§2.4 — no implicit precedence.** Enforced by decision 2: SoR at facet/identity granularity; a scalar owner
  or precedence field is rejected at parse.
- **§2.5 — secrets never in config.** `sources/` holds `credentialRef` pointers only; `ValidateSource` rejects
  inline material; material resolves at plugin-pod spawn via the CredentialRef path.
- **§1.4 / §5 — spine thin, port is the abstraction.** The pod stays Helm; connectors still run as plugins over
  the sovereign gRPC port and are governed hub-side; the reconciler feeds the *same* `Register` seam.

## Consequences

- **Positive:** the whole estate (incl. its own dev estate) is one Git-planned artifact; connector onboarding
  becomes a reviewed PR; `stratt plan` shows connector start/stop blast-radius; devices-as-code exists without a
  CMDB. Deletes ~200 lines of per-connector env wiring at the end of the cutover.
- **Negative / trade-offs:** the reconcile now manages **live goroutine lifecycle**, not just DB rows — the
  central new hazard (mitigated by hash-keyed supervision + child-context cancel + reuse of `homegate.Supervise`).
- **Follow-ups:** extend `MaxPruneFraction` to `KindSource` so emptying `sources/` is refused unattended;
  stopping a syncer raises an **orphan Finding**, never a silent tombstone (a decommission is a §2.4 decision,
  not a reconcile delete); `vocabulary-linter` additions above; the `stratt` CLI estate UX (diff/drift/dry-run).

## Alternatives considered

- **Leave Sources env-only; CaC just references them** — rejected: the SoR binding escapes Git review, the
  exact gap §1.2 forbids; `registry.go:362` already commits against it.
- **A new `stratt-estate` binary / rename `stratt`** — rejected under §1.4 (don't proliferate the spine). The
  CLI is an embryo to grow.
- **A scalar `Kind → source-of-truth` field** (the naïve "each object-type declares its SoR") — rejected as a
  §2.4 implicit-precedence / anti-GPO violation; SoR is per-Facet-namespace ownership.
- **A Kubernetes CRD per Source (operator-style)** — deferred to the charter's own Phase-4 "CRD interface";
  CaC-over-Git is primary and must land first so the estate is portable off any single substrate (§7.1).

## Reviews

- **charter-guardian (2026-07-17): SOUND-WITH-CHANGES.** Decision 2 (SoR at Facet/identity granularity; scalar
  `Kind→owner` is a parse error) matches the data-layer reality (`ErrOwnerConflict`); the static-inventory
  Connector is the charter-clean "devices via code" (authoritative file + `WriterSyncer`-only projection), not a
  writable CMDB. **Must-fixes (folded):** (1) the title's "per-object-type Source binding" encoded the rejected
  `Kind→owner` model — retitled "declaring Sources & Connectors in Git"; (2) the host-removal semantics
  contradicted the follow-up — resolved to **orphan/decommission Finding, max-delta-gated, never a silent
  tombstone** in both places. **Flags (folded):** the static-inventory file schema is pinned to the Connector's
  boundary Contract into named Facets (not a universal host ontology, §1.1); overlapping `facetNamespaces`
  across two `sources/` docs now fails at plan time (§2.1 `ErrOwnerConflict` at CaC time); the CaC surface
  exposes `connector:`, **never** a `grant` keyword (reserved for the §2.5 authz plane) — `binding`/`grant` stay
  prose-only, gated by `vocabulary-linter`.
