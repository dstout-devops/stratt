# ADR 0119 — Versioned configuration and promotion: one estate, N rings, immutable once pinned

- **Status:** **Proposed** (2026-07-25, steward) — vocabulary-linter **CLEAN**; charter-guardian
  **CHANGES REQUIRED → resolved**. Split out of ADR-0118 D4 on review, because it changes the identity key of a
  Named Kind and that is a materially larger blast radius than plumbing values.
  **vocabulary-linter:** `Intent.version`, `Assignment.intentVersion` and `CompiledOrigin.IntentVersion` all
  clean — no banned term, no Named-Kind collision, consistent with the `blueprintVersion` fields beside them.
  Asked directly whether a FOURTH meaning of "version" is one too many (`Blueprint.version`, `View.version`,
  `Contract.Version` + `.vN.schema.json`), it found the reuse acceptable: each is scoped by its Kind, and
  inventing `configVersion`/`schemaVersion` would obscure the pattern this ADR deliberately copies.
  **charter-guardian:** kept the identity key, **rewrote D5 and D3**, and added the decision that actually
  delivers the steward's requirement (D6). Its two critical findings are recorded in **Review record** below,
  because both showed this ADR claiming properties it did not have.
- **Date:** 2026-07-25
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams), §1.2 (projections, never a second truth), §1.7 (evergreen —
  rolling-upgrade safety), §1.8 (never hide diagnosis), §2 (vocabulary frozen at v1.0), §2.4 (no implicit
  precedence; Assignments pin a Blueprint version), §4.3 (compile-diff and blast-radius gating)

## Context

The requirement, in the steward's words: _"Define versions and be able to promote/replicate these
versions/configurations by environment (test-stage-prod)"_, and _"it's totally fine to have environment (and
some other variables, such as the long term sovereignty) be immutable once they 'pass go'."_

ADR-0118 built the parameter plane — values declared once, layered without precedence, reaching the things
that execute. What it did **not** build is a way to hold a configuration still. Today:

- **Blueprints are versioned.** `(name, version)` is the storage identity, deliberately _"so an upgrade rolls
  through rings alongside the old version"_, and `Assignment.BlueprintVersion` pins it — _"pinned so a
  Blueprint revision cannot silently change what a live Assignment means"_.
- **Intents are not.** `graph.intent` is keyed on `name` alone with `ON CONFLICT (name) DO UPDATE`
  (`core/internal/graph/intentstore.go:25`). So the **HOW** is pinned and the **WHAT** is not.

That asymmetry is the gap. But the review found the gap is deeper than the asymmetry, and that fixing only the
asymmetry would have shipped the ergonomics of rings without the property actually asked for — see D6.

### Why content-hash pinning is not enough

The cheaper design is tempting: have the Assignment pin a **digest** of the resolved spec, and fail the compile
when the Intent changes until the pin is bumped. No new storage identity, no migration, no prune change.

It cannot deliver **rings**. A digest pin detects change; it cannot serve two configurations at once, because
the Intent document has exactly one current content. Staging pinned to the new digest and prod pinned to the
old would mean prod's pin matches nothing — prod would **fail** rather than keep running the old configuration.
The requirement is explicitly test → stage → prod _simultaneously_, so the old version must still **exist**,
not merely be remembered. That is the property `(name, version)` was given to Blueprints for.

### What the survey found

**1. The versioned plan/prune machinery already exists — under a different name than first written.** There is
no `computeBlueprintPlan`; `computeIntentLayerPlan` (`desiredstate.go:3001`) handles Intents, Assignments and
Blueprints as sibling blocks in **one** function. Blueprints there are keyed `fmt.Sprintf("%s@%d", …)`, pruned
by that key, and split back apart on delete. Intent versioning is a transposition inside a function that
already does it for a sibling Kind, which is smaller than first claimed — and the first draft of this ADR cited
a function that does not exist, which is corrected here rather than quietly fixed.

**2. Versioning is semantically wrong for provisioning Intents, and would have broken them.**
`reconcileProvisioning` iterates **every** declared Intent and keys a desired fleet by Intent **name**
(`controller.go:209-228`). With versions, `web-fleet@1` and `web-fleet@2` would both enter `provision.Plan` as
live fleets deriving the same `stratt.intent/instance` values (`web-01`…), which ADR-0058 D5 already rejects as
an exclusive-claim collision. There is no fix by selection: a provisioning Intent has **no Assignment** —
ADR-0058 makes provisioning a _sibling_ reconcile keyed to the Intent itself — so nothing exists to pin a
version with. See D3, reframed on review from a kind list to the structural reason.

**3. Blueprint's in-repo layout is one file per name, not one per version.** Worth stating because the review
assumed otherwise and it changes the conclusion in the _stronger_ direction. `estate/blueprints/access.yaml`,
`fileset.yaml` and `web-server.yaml` each carry an inline `version: 1` — exactly the shape an Intent will have.
So the in-place-bump footgun in F3 is **not** worse for Intents than for Blueprints; it is a latent defect
Blueprints have had all along, which is why D6's guard is one rule covering both rather than a special case.

## Decision

### D1 — `Intent` gains `version`, stored `(name, version)`, exactly as `Blueprint` is

`types.Intent` gains `Version int` (default 1). `graph.intent` moves to a `(name, version)` identity, and
`UpsertIntent`/`GetIntent`/`DeleteIntent` take a version. `computeIntentLayerPlan` keys intents by
`name@version` in the same block that already does so for Blueprints, prunes by that key, and splits it back on
delete — including re-keying the source map (`inByName` → `inByKey`), because Apply looks the declaration up by
`PlanEntry.Name` (`desiredstate.go:3232-3235`).

Deliberately a transposition, not a design: the machinery is proven, its failure modes are known, and one
mechanism for "a versioned CaC document" is worth more than a marginally better second one.

### D2 — The Assignment pins the Intent version, and `@N` is REQUIRED

`Assignment` gains `IntentVersion int`, parsed from `intent: tls-app@3` — the same `name@N` grammar and the
same parser `blueprint: web-server@1` uses (generalized from `splitBlueprintRef`).

**Required, not defaulted.** The first draft made it optional-with-`@1` for backward compatibility; the review
argued that is the wrong default _for this ADR's purpose_, and it is right. With an implicit pin, prod's
configuration identity is not stated in prod's Assignment, so D5's "reviewable diff" degrades to a one-line
change against an invisible baseline. And one parser serving two requiredness rules — erroring without `@N` for
Blueprints, defaulting for Intents — is a grammar that has to be explained. The in-repo estates are migrated in
the same change; the vocabulary is frozen at v1.0 and this is the cheap moment.

An Assignment therefore pins **both** halves of what it means: the WHAT (`intent@N`) and the HOW
(`blueprint@M`). That is the complete statement of "this environment is running this configuration through this
composition", and it is the unit promotion moves.

### D3 — Versionable iff an Assignment selects it: the version lives on the seam that references it

Reframed on review from a kind deny-list to the structural property, which is a §1.1 argument and stronger than
the collision one: **an Intent kind is versionable exactly when a seam exists to carry the pin.** An Assignment
selects application-shaped Intents, so it can pin them. Provisioning kinds are selected **by name** by
`reconcileProvisioning`, so there is nowhere for a pin to live — and two versions of one fleet are not two
rings, they are two claims on the same machines.

`version:` on such a kind is therefore rejected at declaration. Mechanically the check **derives** from
`types.IntentCompute` + `types.SingletonIntentKinds` via one exported predicate —
`types.AssignableIntentKind(kind) bool` — never a fresh literal list in the validator. The ADR-0059 family is
still growing (0110/0111/0112); a new provisioning kind must inherit the rejection automatically or D3 rots
into a hole.

### D4 — `CompiledOrigin` carries the Intent version; `CompiledName` deliberately does not

`CompiledOrigin` gains `IntentVersion int`, for two load-bearing reasons: §1.8 descent would otherwise be
ambiguous the moment two versions exist, and the orphan branch re-reads the Intent to learn its `onRemove`
semantics (`compiler.go:337`) and needs a version to read at.

`CompiledName` (`compiler.go:538`) is **not** extended with the version, on purpose: a promotion then updates
Baselines **in place** rather than prune-and-recreate, preserving Finding continuity across a bump. The
consequence, stated so D4 does not over-promise: `CompiledOrigin.IntentVersion` answers _"which version is
expected now"_, not _"which version produced this still-open Finding"_ — `Finding.Diff` is overwritten on each
observation.

### D5 — Promotion is a reviewed Git bump, gated by an expectation-change acknowledgement

Promotion test → stage → prod is editing the target environment's Assignment to pin the new refs.

**The first draft claimed §4.3 coverage this does not have, and that claim is withdrawn.** Verified: the
max-delta gate keys exclusively on **View membership** (`compiler.go:156`, `diffIDs`/`exceedsDelta`), and route
matching never reads the Intent spec — `routeMatch` uses `route.Match` verbatim, while only `observe` and
`remediationParams` are substituted. An intent-version bump changes expected facet **values**, not membership,
so `joins == leaves == 0` and the gate is **structurally incapable of firing**. `AckDelta` is irrelevant to it.
There is also no pre-merge compile-diff at all: `compiler.Compile` runs only inside the daemon reconcile
(`controller.go:507`), and `stratt plan` shows the CaC entry level (`Assignment prod-web: update`) — i.e. the
Git diff a reviewer has already read.

So a version bump would silently rewrite every expectation across that Assignment's whole target set, gated by
nothing but code review. **That gap is now closed** — the blocking follow-up was built rather than left
outstanding, because shipping the ergonomics of promotion without a gate on it was the weakest point of the
design:

- `AssignmentDelta.ExpectationChanges` renders every compiled expectation whose value changes, with its
  Baseline, namespace, path, and both values. "What does promoting this actually change" is answerable from
  the plan instead of inferred from a Git diff of the Intent.
- A change beyond the Assignment's `MaxDelta` fraction **pauses the compile** until `AckDelta` is bumped —
  and while paused the LIVE expectations stay in force, so an unacknowledged promotion changes nothing.
- The same `MaxDelta`/`AckDelta` pair as membership, deliberately, not a second pair. §4.3's acknowledgement
  means "I have reviewed this Assignment's pending change"; two independent acks would let an operator
  acknowledge a membership shift while ignoring a total expectation rewrite, which is the worse failure. One
  ack, both axes.
- A first compile is **not** a change (every expectation is new), and an unchanged recompile reports nothing —
  otherwise the gate would fire on every bootstrap and then continuously on a converged estate.

What remains true, and is the reason this is a §4.3 _sibling_ rather than an extension: the membership gate
still cannot see a value-only change, and the expectation gate still cannot see a membership-only one. They
are two axes over one acknowledgement, not one gate.

**The Crossplane analogy is dropped**, not just softened. `CompositionRevision`s are _system-generated_, which
is where their immutability comes from; Stratt cannot copy that, because an auto-generated revision row would
be pinnable desired state existing only in the graph, which §1.2 forbids. Git-declared versions are the right
call — and therefore immutability has to come from a guard, which is D6.

### D6 — A version an Assignment pins cannot be edited or deleted in place (the decision that delivers the requirement)

Added on review, and it is the one that makes "immutable once it passes go" true rather than aspirational.

The first draft assumed identity implied immutability. It does not: `UpsertIntent` would keep
`ON CONFLICT … DO UPDATE SET spec`, and `computeIntentLayerPlan` emits `ActionUpdate` for a same-version content
edit. So editing `tls-app.yaml` **without touching `version:`** would change what prod is running at the next
reconcile — precisely the failure D5 claimed was structurally impossible. This ADR's own first wording conceded
it ("creates **or updates** a different version") without noticing.

So, at **plan** time:

- An `ActionUpdate` on an Intent or Blueprint version that a **declared Assignment pins** is a plan **error**,
  not an update: _"published version tls-app@3 is pinned by assignment prod-web; declare a new version."_
- An `ActionDelete` of a pinned version is likewise a plan error, rather than a delete that breaks every ring
  pinned to it. Without this, the natural in-place bump (`version: 1` → `2` in one file) produces
  `Create @2` + `Delete @1` in a single plan; `validateRefs` then fails, the Assignment is skipped, and its
  prior Baselines are **retained** (`compiler.go:320`) — so prod freezes on stale expectations while erroring.
  `MaxPruneFraction` does not catch it: it is a per-kind fraction, and one delete among N intents is under any
  sane threshold (versioning inflates the denominator, weakening that guard further).

**One rule, both Kinds.** It applies identically to `Blueprint@N`, which answers the first draft's own objection
about "a second divergent rule" — there is one rule, and Blueprint has been exposed to this all along (see
survey finding 3). Not hosted in ADR-0073 admission: its CEL binds a single `object`, so a cross-manifest pin
check is not expressible there.

**Accepted cost, decided rather than left implicit:** iterating on a pinned version requires bumping it. In dev
that is friction — unpin, or bump per edit. The alternative is prod changing without a pin bump, which is the
thing being fixed.

### D7 — Migration is two releases, expand then contract (ADR-0078)

The first draft's "needs a backfill and must be idempotent" was a **violation**, and `task migrate:lint` — which
runs in `task ci` — would have failed it: `DROP CONSTRAINT` in an `Up` section is refused without an explicit
`-- expand/contract-ok:` marker, and that marker is exactly the acknowledgement that must not be given here.
ADR-0078 D1 runs migrations in a pre-upgrade hook **while old replicas still serve**, and an old replica's
`ON CONFLICT (name)` errors the instant the unique index backing it disappears — every Intent upsert failing
for the whole roll window.

- **Release N (expand, additive only):** `ADD COLUMN version int NOT NULL DEFAULT 1` plus
  `CREATE UNIQUE INDEX … (name, version)`; **keep** the `(name)` primary key. Ship code writing
  `ON CONFLICT (name, version)` and reading `WHERE name = $1 AND version = $2`. Old and new replicas both work.
  **Consequence stated:** while the `(name)` PK survives, two versions of one name **cannot** coexist, so D1's
  actual feature does not light up in release N — and a declaration with more than one version of a name is
  rejected **at load** with a real message rather than surfacing a raw duplicate-key error.
- **Release N+1 (contract, marked):** drop the `(name)` PK, promote `(name, version)`. Rings work here.

The `intent_touch_updated_at` trigger (`00013_intent_layer.sql`) is unaffected. `Down` is written explicitly
(ADR-0078 follow-up MIG-1).

## What two live versions can and cannot collide on

Written down because it is non-obvious, and because the first draft flagged it as an open question the review
answered.

Claims are keyed `(namespace, entity)` and attributed to the **Assignment** (`compiler.go:97-102`, `:241`), so
`tls-app@1` and `tls-app@2` can only collide if two _Assignments_ target overlapping Views with an exclusive
route — which already happens today with a single Intent version, and is the anti-GPO rule working as designed.
Two consequences follow:

- **Across environments the collision cannot occur at all.** `ListAssignments` is environment-scoped
  (ADR-0057), so test/stage/prod running three versions is structurally collision-free. The exposure is only
  two rings **inside one** environment — a canary.
- **When it does fire, both Assignments are poisoned**, and both keep their previously-compiled Baselines
  (`compiler.go:271-275`, `:320`). So a bad canary freezes the stable ring too. That is the anti-GPO rule's
  fail-loud posture, but it is sharper than it looks and belongs in the estate guide.

## Blast radius

| Path                                                       | Change                                                                                                                     |
| ---------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| `types.Intent`, `types.Assignment`, `types.CompiledOrigin` | new fields (D1, D2, D4)                                                                                                    |
| `types`                                                    | new `AssignableIntentKind(kind) bool` predicate (D3)                                                                       |
| `graph.intent` + `intentstore.go`                          | two-release expand/contract (D7); version on Get/Upsert/Delete                                                             |
| `desiredstate.computeIntentLayerPlan`                      | key by `name@version`, `inByName` → `inByKey`, split on delete                                                             |
| `desiredstate` Apply                                       | `KindIntent` delete case gains the split (mirroring the Blueprint case)                                                    |
| `desiredstate` plan                                        | **`PlanEntry.Name` is a wire field** — `name@version` changes `stratt plan` output and the plan API/artifact               |
| `desiredstate` validation                                  | reject `version:` on non-assignable kinds (D3); reject two versions of a name pre-contract (D7); pinned-version guard (D6) |
| `compiler.validateRefs`                                    | `GetIntent(name, version)` from the pin; error text renders `name@version` (F4)                                            |
| `compiler` orphan branch                                   | read the Intent at `CompiledFrom.IntentVersion` (`compiler.go:337`)                                                        |
| `compiler.compiledBaseline`                                | stamp `IntentVersion`; `CompiledName` deliberately unchanged (D4)                                                          |
| `compiler` claim-conflict message                          | name the versions, not just the Assignments (`compiler.go:690`, F4)                                                        |
| `graph/intentstore.go` errors                              | render `name@version` (F4)                                                                                                 |
| `reconcileProvisioning`                                    | unaffected **by construction** — D3 keeps provisioning kinds unversioned                                                   |
| `api/recertification.go`, `api/server.go`                  | `GetIntent`/`ListIntents` callers gain the version                                                                         |
| OpenAPI + generated clients + UI                           | `version` on Intent, `intentVersion` on Assignment                                                                         |
| `contracts/intents/*`                                      | unchanged — schema-shape versioning is a **different axis**                                                                |
| in-repo estates                                            | every Assignment gains `intent: name@N` (D2 makes it required)                                                             |

**Two adjacent meanings of "version", both to be documented at the field:** `Blueprint.version` and
`Intent.version` are **identity-forming** (rows coexist); `View.version` is a **monotonic revision counter**,
auto-bumped by a trigger with one row per name (`types/view.go`, `graph.view_bump_version`). `Contract.Version`
plus `.vN.schema.json` is a third, further away: schema **shape**, resolved newest-wins. Conflating any of them
would be a category error.

## Charter alignment

- **§2.4** — no precedence introduced: a pin is an explicit reference and two versions never merge. D2's
  required `@N` removes even the appearance of a default-resolution rule.
- **§1.2** — versions are Git-declared CaC; nothing new is written to the graph by a reconcile, and no
  system-generated revision row is created (the reason the Crossplane analogy is dropped).
- **§2** — no new Named Kind; fields mirror `Blueprint`'s exactly.
- **§1.8** — D6 fails loudly at plan time instead of at compile; D3 refuses at declaration rather than blaming
  the wrong document later; F4 puts the version in every error an operator will actually hit.
- **§1.7 / ADR-0078** — D7 is expand-then-contract, so a rolling upgrade never has an old replica writing
  against a dropped constraint.
- **§4.3** — **not** claimed. D5 states the gap and books the fix; the existing gate cannot fire on a value-only
  change.

## Review record

| Finding                                                                                                                         | Verdict            | Resolution                                                                                                                             |
| ------------------------------------------------------------------------------------------------------------------------------- | ------------------ | -------------------------------------------------------------------------------------------------------------------------------------- |
| **F1 (critical)** — identity ≠ immutability: a same-version edit still updates prod, so D5's central promise was false          | accepted, verified | **D6** added: a pinned version cannot be updated or deleted in place, for Intent and Blueprint alike                                   |
| **F2 (critical)** — §4.3 coverage claimed but structurally impossible (max-delta keys on membership; no pre-merge compile-diff) | accepted, verified | D5 rewritten; then the gap CLOSED — an expectation-change diff plus a pause-until-acked gate reusing §4.3's own MaxDelta/AckDelta pair |
| **F3** — the in-place bump breaks pinned rings and `MaxPruneFraction` cannot catch it                                           | accepted           | D6 covers delete as well as update                                                                                                     |
| **F4** — errors omit the version, so "intent tls-app not found" while it sits in Git                                            | accepted           | version rendered in `validateRefs`, the store, and the claim-conflict message                                                          |
| **V** — migration violates ADR-0078; `task migrate:lint` would fail it                                                          | accepted, verified | **D7**: two releases, expand then contract, with release-N's limitation stated                                                         |
| Charge 1 — `@N` optional is not a §2.4 violation but is the wrong default here                                                  | accepted           | D2 makes it required, estates migrated in the same change                                                                              |
| Charge 2 — could two versions collide on the exclusive facet claim?                                                             | answered           | No new collision class; cross-environment is structurally impossible; written up in its own section                                    |
| Charge 3 — `version` already has adjacent meanings                                                                              | accepted           | `View.version` disambiguated at the field and in the blast radius                                                                      |
| Charge 5 — D3's kind deny-list is a smell                                                                                       | accepted           | reframed to the seam property + a derived `types.AssignableIntentKind` predicate                                                       |
| Review's own error — "Blueprint versions live in per-version files"                                                             | corrected          | In-repo Blueprints are one file with an inline `version:`; the footgun is shared, which is why D6 is one rule                          |

## Consequences

- **Positive.** Rings become real: test, stage and prod can run three configurations of one Intent
  simultaneously. Prod stops being one careless edit away from changing — because of D6, not because of D1.
  Promotion is a reviewable two-line diff with the full trail in Git.
- **Iterating on a pinned version requires bumping it** (D6). Real friction in dev, accepted deliberately.
- **The feature is dark for one release** (D7): expand ships the column and the code, contract lights up
  coexisting versions.
- **Promotion is gated by an explicit acknowledgement**, not by review alone: an unacknowledged expectation
  rewrite pauses and the live expectations stay in force. The residual honesty is that the two gates see
  different axes — membership cannot see a value change and vice versa — so they are siblings over one ack.
- **Provisioning Intents are asymmetric** — versionable configuration for Assignment-selected kinds only. Real,
  and owed a line in the estate guide, not just here.

## Alternatives considered

- **Content-hash pinning** — rejected: immutability without rings, because one document cannot serve two
  configurations. Recorded at length because it is cheaper and will be proposed again.
- **A Git branch per environment** — rejected. ADR-0057's model is _one_ estate repo with environment as a
  selector; branches would fork the estate, make "what is prod running" a cross-branch diff, and turn merge
  conflicts into a configuration mechanism.
- **System-generated revisions on write (the literal Crossplane shape)** — rejected on §1.2: a revision row that
  exists only in the graph is pinnable desired state outside Git.
- **Versioning the Assignment instead of the Intent** — rejected: the Assignment is already per-environment, so
  versioning it answers nothing. The thing needing to be held still is the shared WHAT.
- **Versioning provisioning Intents with a selector** — rejected (D3): no seam to carry a pin, and the Intent
  name is the fleet identity.
- **A `promoted: true` flag or a promotion API** — rejected: promotion is a reviewed Git edit, not a runtime
  verb; a runtime verb would put desired state outside Git (§1.2).
- **Hosting D6's guard in ADR-0073 admission** — rejected as not expressible: its CEL binds a single `object`,
  and the pin check is cross-manifest.

## Follow-ups

- ~~**BLOCKING: an expectation-diff surface.**~~ — **DONE**, see D5. `AssignmentDelta.ExpectationChanges`
  renders every changed expectation, a change beyond `MaxDelta` pauses the compile until `AckDelta` is bumped,
  and the live expectations stay in force meanwhile. Built rather than deferred because a promotion mechanism
  whose only gate is code review is the design's weakest point, and it was cheap once the prior Baselines were
  already being read for the prune. Served on `GET /compile` and declared in `openapi.yaml`, so the UI, CLI and
  MCP see it identically (§1.6) — the handler marshals the compiler snapshot directly, so a field absent from
  the spec would have been served but invisible to every generated client.
- **An estate-guide section on rings**, covering D3's asymmetry and the canary-freezes-the-stable-ring
  consequence — neither is discoverable from the schemas.
- **Promotion ergonomics** — a `stratt promote <assignment> --to <env>` that edits the pins and shows the
  expectation diff. Now unblocked (the diff exists), still deliberately not now: the mechanism should earn its
  sugar, and the pins are two lines of YAML.
- **Extend D6's guard to any future versioned CaC Kind** by construction, rather than per-Kind.
