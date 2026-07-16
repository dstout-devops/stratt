# ADR 0046 — Stratt as Substrate: the dark-matter re-centering and the sovereign plugin port

- **Status:** **Proposed** — a re-centering of the project's identity. This ADR proposes an evolution of the
  charter **§0 (Thesis)** and **§1 (Founding Disciplines)** and therefore requires the **highest review bar in
  the project** (charter-guardian + steward). It is a *direction*, not an approved refactor; no code changes
  and no edit to `stratt-charter.md` accompany it.
- **Date:** 2026-07-16
- **Deciders:** Project steward (dstout)
- **Charter sections:** §0 (Thesis), §1.1 (type the seams), §1.2 (projections), §1.4 (boring spine, pluggable
  everything), §1.5 (sovereign contracts, multiple transports), §1.6 (one Principal/authz/audit/cost), §1.8
  (never hide diagnosis), §2 (Vocabulary — adds **band** and **beam** as *coordinates*; the umbrella noun for
  a thing-behind-the-port is **plugin**, deliberately *not* a new Named Kind, per the vocabulary-linter ruling
  below). Builds on ADR-0044 (Cells), ADR-0045 (home-ownership gate), ADR-0032 (Sites), ADR-0015 (Contracts),
  ADR-0009 (identity/authz).

## Context

Stratt began as an **AAP replacement** — a *doer* that runs the automation. That framing is too small. The
more durable thing to build is not a better doer but the **substrate the doers live in**: the spine that holds
every automation domain in relation and does none of their work. The guiding image is **dark matter** — it
emits no light and becomes no star; it is the mass whose gravity holds the luminous structures in place.
Stratt should be that: the backbone, the glue, never the "do."

Two forces make this urgent rather than aesthetic:

1. **Scope / velocity decay.** A coupled codebase's change-cost grows — a 2-minute change becomes 5, then 10,
   then 20. This session was itself the counterexample: the connectors live *in* `core/internal/connectors`,
   wired inline in `main.go` (the auto-cutover touched seven connector blocks), the graph integration suite
   runs ~70s over one shared DB surface, and one OpenAPI change regenerated both Go stubs and UI types. That
   is the monolith pathology, structural not accidental.
2. **A coordinate system worth adopting.** The *Seven Spectra* (from the sibling project Nebula) is an
   *observation*: a coordinate for *where* an infrastructure concern lives — bands **S1 Physical → S7
   Signal**, services as vertical **beams**. `band`/`beam` reconcile with Stratt as **residency coordinates
   alongside `cell`** — but they are *not* `cell`'s equal. `cell` (region) is *semantically empty* about what
   a thing *is*, which is exactly why §1.1 lets it be total and structurally enforced. `band` names
   *concern-kinds* (S1..S7), so a **total, mandatory** S1–S7 classification of every datum would be a
   stratification-by-kind — the CSDM/universal-ontology precondition §1.1 forbids by name. The
   charter-guardian review (2026-07-16) drew the line: `band`/`beam` are admissible only as **optional,
   sparse, platform-*computed* coordinates, demanded by a shipping Contract and never hand-authored as a
   precondition** — the Facet discipline, not the `cell` discipline. On those terms they do not violate §1.1;
   on "total/enforced" terms they would. ADR-0044's `cell` proves a *semantically-empty* residency coordinate
   can be first-class, single-writer, and fence-movable; `band`/`beam` inherit the movable machinery but not
   the free "total" pass.

The full design (disciplines, boundary, repo mapping, port `.proto` sketch, the thirteen t=0 invariants, and the
phased path) is captured in the session design artifact; this ADR records the decision and the review it
requires.

## Decision

Re-center Stratt from *doer* to *substrate*, on three pillars.

### 1. The dark-matter thesis + founding disciplines

The core owns exactly six things — **graph** (what is), **coordinates** (where it lives), **contracts** (the
seams), **reconcile engine** (when to act), **authz** (who may), **audit** (what happened) — and **zero domain
logic**. Everything that *does* — provision, configure, observe, notify, remediate — is a **plugin** behind a
port (a Connector, an Actuator, or a capability-backend; *plugin* is the umbrella word, not a new Named Kind).
The disciplines (twelve; most carried verbatim from the current §1, three promoted, four added):

- *Carried:* projections-never-a-second-truth (§1.2); one-identity/authz/audit/cost (§1.6); secrets brokered
  not held (§2.5); never-hide-diagnosis (§1.8); rug-pull-proof + evergreen (§1.3/§1.7).
- *Promoted to the thesis:* **boring spine, pluggable everything** (§1.4) and **sovereign contracts, multiple
  transports** (§1.5) become the defining identity, not two of eight.
- *Added:* **(a) Spine, not doer** — a domain-specific code path in the core (an `if ansible {…}`) is a design
  failure. **(b) The unit of work is never the whole** — every plugin is a physically independent
  build/test/CI/release unit; the core stays small and grows *only when an invariant is added, never when a
  capability is*; the contract is the only synchronization surface, so bounded CI and bounded context (human
  *and AI*) fall out of physically-real boundaries. **(c) Coordinates place, they do not classify** — `cell`
  (region) and `kind` are total and structurally enforced because they are *semantically empty* about what a
  thing is; `band` (stack) and `beam` (service) are **optional, sparse, and platform-computed from existing
  Facets, demanded by a Contract** — never a hand-authored precondition (that would be the §1.1/CSDM ontology,
  and a classification DSL against the §7.5 non-goal). Blast radius, authz scope, and governance are
  *computed* from whichever coordinates a datum carries, never intuited. **(d) The spine is boring and named;
  backends are swappable under measured pain** — Postgres/NATS/Temporal remain the blessed,
  §1.7-evergreen-tracked default spine (their known upgrade record is *why* they are named, §1.4/§3). A
  `StateStore`/`EventBus`/`DurableExec` capability-port exists so a backend *can* be swapped under measured
  pain (Temporal's own persistence abstraction, generalized) — it is an escape hatch, **not** a licence to
  make the spine vendor-neutral and re-invite the dependency sprawl §1.4 exists to prevent.

### 2. The core/plugin boundary is **content-blindness**

The core operates on every payload as an **opaque, contract-typed blob** — it schedules, routes, authorizes,
versions, and records provenance on it, and **never interprets what it means**. Only plugins are
content-experts. The one test for every "put it in core" argument: *does this need to know what the payload
is?* If yes, it is a plugin. This keeps the core's size a function of the number of *mechanisms* (small,
fixed), not *domains* (unbounded, at the edge), and makes parallel development trivial — the merge-conflict
surface of the whole platform equals the contract files.

**One carve-out, load-bearing (guardian finding #3).** Content-blindness is the rule for the **Actuator/Apply
path** — the core never typed that content, so payload-opacity there merely formalizes the permanent
Actuator/Action split (§2.3). It is **not** the rule for the **Syncer→graph write-back path**: a plugin's
returned Facets are the top rung of the §2.4 Contract ladder, and the core **must** keep validating them, at
the data layer, against the pinned, hash-verified Facet schema (§1.5 — "schema drift is blocking, never
silently absorbed"). If that validation moved into the untrusted plugin, §1.5's seam would have been handed to
the very thing it exists to police. So: **the payload is opaque on Apply; on write-back it is
envelope-governed *and* core-schema-validated.** Content-blindness is §1.1 *at the seam*, never the
abandonment of the seam.

### 3. The sovereign plugin port

One versioned, hash-pinned protobuf/gRPC **bus** with typed **plugin classes** (the USB model — one bus,
standard device classes; here the classes are *plugin* classes). A `PluginService` (`Observe/Plan/Apply/
Destroy/Invoke/Subscribe`, capability-negotiated via `GetManifest`); capability classes (`StateStore`,
`EventBus`, `SecretBroker`, `DurableExec`, `ArtifactStore`) are siblings on the same authenticated/versioned
bus. The load-bearing wire shape is **opaque `Payload` + typed governance `Envelope`** (coordinates,
contract-ref, principal, credential-refs, idempotency-key, plugin-computed content-hash) — the opaque message
is **`Payload`, never `Resource`** (`resource` is a §2-banned core-model identifier). The core reads the
envelope and governs; it hands the payload to the plugin untouched. Thirteen invariants must be right at t=0
(any of which, if wrong, means a rewrite) — the original ten plus three the charter-guardian review made
non-negotiable (11–13):

1. opaque payload / typed envelope; 2. content-blind discovery (`GetManifest`); 3. identity **bound to an
authenticated channel** (the §1.6 seam across a process boundary — the direct lesson of the cross-Cell auth
findings hardened this session); 4. streaming + persistent connections; 5. envelope-vs-payload version
decoupling + hash-pinned contracts + blocking drift; 6. provenance **core-stamped, not plugin-claimed**;
7. idempotency-by-contract (SDK-enforced) + optional checkpoint/resume; 8. cursor-delta `Observe` +
plugin-computed content-hash + envelope hoisting (scale); 9. optional `Observe` payload-tree at true bands +
atomic `Apply` on the root (band-honest profiles without breaking atomic apply); 10. one bus + typed plugin
classes; **11. the core enforces the facet-ownership registry (§2.1) and coordinate-scope from the
*authenticated channel identity* before writing** — a plugin may write Facets only within the Source/coordinate
scope its channel owns, so a compromised plugin forges no ownership and opens no §1.2 second-writer path (the
content/ownership guarantee, distinct from the writer guarantee #3/#6 already gives); **12. the
event/diagnostic descent channel (`Subscribe`/`TaskEvent`) is a typed, core-legible Envelope** even while the
desired-state payload stays opaque — else §1.8 one-click descent dies at the port, inside the blob;
**13. the shipped plugin SDK stays permissively licensed and links no copyleft tool** — only a community
plugin *binary* links copyleft on its own side of the wire, which turns the port into a uniform license
firewall (the ad-hoc "Ansible is subprocess-only" carve-out, generalized).

### Migration is Stratt's own doctrine, applied to itself

The boundary **already exists in-process** (the `Actuator`/`Syncer`/`Action`/`Emitter` interfaces; `dispatch`
treats JobSpecs opaquely; **Sites already run as wire-plugins** over `siteproto`). The refactor *promotes the
interface to a port and extends the Site model to all plugins* — import → co-manage → cutover + fenced move:
**A** formalize the port; **B** extract one plugin (`vcenter`) over the wire as the existence proof; **C**
extract the rest in parallel with in-process/wire co-registration; **D** capability-port the backends. None of
this begins until this ADR is accepted at the highest bar.

## Charter alignment

This ADR is unusual: it proposes to **evolve** §0/§1, so it must be honest about upholds, promotions, and
tensions.

- **Upholds:** §1.2 (the graph stays a rebuildable projection; the substrate remembers, it does not own),
  §1.6 (one Principal/authz/audit — *strengthened* by carrying it across a process boundary), §2.5 (secrets
  brokered, never crossing the core or a coordinate boundary), §1.8 (partial-result honesty; a plugin is a
  descendable node, never a silent absorber), §1.3 (Apache-2.0 spine + public contract, no gated tier).
- **§1.1 (type the seams, not the world):** **reinforced on the terms above, not violated** — but only once
  the guardian's two corrections hold: `band`/`beam` are *computed, sparse, Contract-demanded* coordinates
  (not total/enforced classifications, finding #1), and content-blindness is the *Apply-path* seam while the
  *Syncer write-back* stays core-schema-validated (finding #3). The core types the envelope on Apply, and the
  envelope-plus-Facet-schema on write-back — never the whole world, but never *less* than the seam.
- **Promotes §1.4 / §1.5** from disciplines to the thesis — but keeps §1.4's *named boring spine* half intact
  (finding #2): the promotion is "pluggable everything behind a sovereign port," not "vendor-neutral spine."
- **Tensions resolved at review (charter-guardian, 2026-07-16 — SOUND-WITH-CHANGES, folded in above):**
  (1) the capability-ports discipline is an *escape hatch under measured pain*, not "never on vendors" — the
  named spine stays. (2) The **reconcile-seam** (loop-primitive + durable-workflow escape hatch) is the one
  *bet*, not a pure principle; it belongs in a subordinate ADR, not §1. (3) §2 vocabulary (vocabulary-linter,
  2026-07-16 — resolved): `band`/`beam` are admitted **as coordinates** (no collision); **`device` is BLOCKED
  as an umbrella noun** — it collides with the endpoint Entity `Kind: "device"` (`msgraph/normalizer.go`,
  §2.1/§0). Resolution: the umbrella is **`plugin`** (the charter's existing informal word — *not* a new Named
  Kind); a thing-behind-the-port stays a **Connector/Actuator** or a capability-backend. The gRPC service is
  `PluginService` and the opaque message is `Payload` (never `Resource`, a §2-banned term). No new Named Kind
  is minted; only `band`/`beam` are candidate §2 additions, and only at steward sign-off.

## Consequences

- **Positive:** velocity stays flat as the system grows (change-cost is local); N-way parallel development and
  bounded AI context fall out of physically-real boundaries; any backend is swappable (no vendor lock in the
  core); community plugins in any language via the wire contract; the whole ADR-0044/0045 single-writer +
  provenance + audit machinery is *reused*, not rebuilt.
- **Negative / trade-offs:** a large structural refactor of a Phase-3-mature codebase; a per-call wire cost
  (mitigated by streaming + long-lived connections); the core-ceiling needs enforceable teeth or the spine
  re-accretes. **The port protocol is itself a §1.7 fossilization vector** (the guardian's missed tension): a
  load-bearing wire contract pinned by N community plugins is exactly the compat surface that ossifies —
  "never become the fossil AWX did." This is a §1.7 risk, not only a §1.5 one (Terraform provider-protocol
  pain); the mitigations are invariant #5 (envelope/payload version decoupling), protocol versioning from
  Phase A, and extending the evergreen gate to the protocol version — and the §9 risk table must gain a
  *plugin-port protocol fossilization* row.
- **Validation already in hand:** the hardest legal constraint proves the model — **Ansible is already
  subprocess-only** (`ansible-runner`, GPLv3) and it works; the refactor generalizes what the one legally
  required case forced.
- **Follow-ups:** the evergreen gate extends to the port protocol version; `band`/`beam` are the only
  candidate §2 additions (at steward sign-off); the SDK gains a plugin conformance + idempotency test harness
  (and stays permissively licensed, invariant #13); a core-size budget/gate; and the multiplied
  credential-bearing plugin channels are governed by the existing trust-tier + signing + sandbox posture
  (§2.2/§7.3), named explicitly as covering the plugin surface.

## Alternatives considered

- **Bolt band onto Stratt as a Facet, connectors stay in `core/`** — rejected: makes the residency coordinate
  second-class (the exact "add something down-line that doesn't fit" the substrate thesis forbids), and leaves
  the scope/velocity-decay pathology in place.
- **A greenfield project** — rejected: throws away the hardest *solved* problems (multi-region single-writer,
  structural data-layer enforcement, the Connector contract, provenance, one audit stream) that a
  resilient-from-t0 substrate must have and that took real work to get right.
- **One literal port service for all plugins** — rejected: capability-backends (state store, event bus) have a
  different verb-shape and no coordinate envelope; "one bus + typed plugin classes" is more USB-faithful and
  honest.

## Reviews

- **charter-guardian (2026-07-16): SOUND-WITH-CHANGES — folded in.** The *direction* is charter-honoring (the
  maturation of §0/§1.4/§1.5/§7.6), and the writer/provenance guarantee genuinely survives the wire (single-
  writer is the `enforce_write_path` DB constraint keyed on the projector's declared Cell — plugins never hold
  a graph write path, so a compromised plugin forges no provenance). Six findings + two added invariants were
  required and are now reflected above: (1) `band`/`beam` demoted to computed/sparse coordinates, not
  total/enforced classifications; (2) the named boring spine kept, capability-ports reframed as a
  measured-pain escape hatch; (3) the Syncer write-back path carved out of payload-opacity (stays
  core-schema-validated); (4→inv.11) envelope-enforced facet-ownership + coordinate-scope before write;
  (5→inv.12) typed core-legible diagnostic descent; (6) **`device` blocked as a Named Kind** (namespace
  collision with the endpoint Entity) — rename before any §2 edit; plus inv.13 (SDK stays permissive) and the
  §1.7 protocol-fossilization risk logged. Affirmed intact: §2.5 secrets (CredentialRef names only), the
  GPLv3 boundary (a wire port is a *cleaner* arm's-length firewall than a subprocess pipe — under-claimed by
  the ADR), the §1.6 identity model, and all four permanent non-goals.
- **vocabulary-linter (2026-07-16): CLEARED with renames — folded in.** `band`/`beam` admitted as coordinates
  (no collision). `device` **blocked** as an umbrella noun (collides with the `Kind: "device"` endpoint Entity
  in `msgraph/normalizer.go`, §2.1/§0): the umbrella is now **`plugin`** — the charter's existing informal
  word, *not* a new Named Kind. The gRPC service is `PluginService`; the opaque message is `Payload` (never
  `Resource`, a §2-banned identifier). Capability-class names (`StateStore`/`EventBus`/…) and `port`/`bus`/
  `payload`/`envelope` pass as design/transport terms with no Named-Kind claim. Net: the only candidate §2
  *additions* are `band`/`beam` as coordinates; no new plugin Kind is minted. Gate cleared for steward review.
- **No dependency-scout yet** — the port introduces gRPC/protobuf tooling (Phase A); route through
  dependency-scout when that lands.
