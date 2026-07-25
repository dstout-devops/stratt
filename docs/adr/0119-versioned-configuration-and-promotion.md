# ADR 0119 — Versioned configuration and promotion: one estate, N rings, immutable once pinned

- **Status:** **Proposed** (2026-07-25, steward) — charter-guardian **pending**; vocabulary-linter **CLEAN**.
  **vocabulary-linter:** `Intent.version`, `Assignment.intentVersion` and `CompiledOrigin.IntentVersion` all
  clean — no banned term, no Named-Kind collision, and consistent with the `blueprintVersion` fields they sit
  beside. It was asked directly whether a FOURTH meaning of "version" is one too many, since the word already
  carries three: `Blueprint.version` (a CaC document version), `View.version` (auto-incremented query
  metadata, which it also traced to `Run.ViewVersion`), and `Contract.Version` + the `.vN.schema.json`
  convention (schema SHAPE, resolved newest-wins). Verdict: acceptable reuse, because each is scoped by its
  Kind and inventing distinct terms (`configVersion`/`schemaVersion`) would obscure rather than clarify the
  pattern this ADR is deliberately copying. Recorded because the alternative was a rename, and the reasoning
  is the part worth keeping.
  Split out of ADR-0118 D4 on review, because it changes the identity key of a Named Kind and that is a
  materially larger blast radius than plumbing values.
- **Date:** 2026-07-25
- **Deciders:** steward
- **Charter sections:** §1.2 (projections, never a second truth), §1.8 (never hide diagnosis), §2 (vocabulary
  frozen at v1.0), §2.4 (no implicit precedence; Assignments pin a Blueprint version), §4.3 (compile-diff and
  blast-radius gating), §8 (phasing)

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
  (`core/internal/graph/intentstore.go:24`). So the **HOW** is pinned and the **WHAT** is not: editing an
  Intent changes what every environment means at the next compile pass, with no review gate between the edit
  and production.

That asymmetry is the whole gap. A promoted configuration is not immutable, and "promote to prod" has no
mechanism — only "edit the file and hope the rings agree".

### Why content-hash pinning is not enough

The cheaper design is tempting: have the Assignment pin a **digest** of the resolved spec, and fail the compile
when the Intent changes until the pin is bumped. No new storage identity, no migration, no prune change. It
delivers immutability and a deliberate promotion step.

It cannot deliver **rings**. A digest pin detects change; it cannot serve two configurations at once, because
the Intent document has exactly one current content. Staging pinned to the new digest and prod pinned to the
old would mean prod's pin matches nothing — prod would **fail** rather than keep running the old configuration.
The steward's requirement is explicitly test → stage → prod _simultaneously_, so the old version must still
**exist**, not merely be remembered.

That is precisely the property `(name, version)` storage was given to Blueprints for. So this ADR transposes
proven in-repo machinery rather than inventing a mechanism.

### What the survey found, and how it bounds the decision

Two findings from reading the code before drafting, both of which change the shape:

**1. Blueprint's versioned plan/prune is a complete template.** `computeBlueprintPlan` keys the declared set
by `fmt.Sprintf("%s@%d", name, version)`, carries that as the `PlanEntry.Name`, prunes any stored key not
declared, and `splitBlueprintRef`s it back apart on delete (`desiredstate.go:3052-3070`, `:3240`). Intent
versioning is a direct transposition, which materially shrinks the risk the ADR-0118 review flagged.

It also settles a question that looked like a new invariant. Blueprint prune does **not** check whether an
Assignment still pins the version being deleted: removing `web-server@1` from Git while an Assignment pins it
deletes the row, and that Assignment's next compile fails `blueprint … not found` in `validateRefs`. Loud, not
silent. Intents inherit the same behaviour — so no new "cannot prune a pinned version" rule is needed, and one
is deliberately **not** added, because it would be a second, divergent rule for the same situation.

**2. Versioning is semantically wrong for provisioning Intents, and would have broken them.**
`reconcileProvisioning` iterates **every** declared Intent and keys a desired fleet by Intent **name**
(`controller.go:205-227`: `specs[pi.Name] = pi.Spec`). With versions, `web-fleet@1` and `web-fleet@2` would
both enter `provision.Plan` as live fleets — two declarations deriving the same `stratt.intent/instance` values
(`web-01`…), which ADR-0058 D5 already rejects as an exclusive-claim collision. So a versioned
`Intent/Compute` would either double-provision or, more likely, refuse to plan at all.

There is no principled fix by selection, either: a provisioning Intent has **no Assignment** — ADR-0058 makes
provisioning a _sibling_ reconcile keyed to the Intent itself — so nothing exists to pin a version with. The
name IS the fleet's identity, and two live versions of one fleet are not two rings; they are two claims on the
same machines.

## Decision

### D1 — `Intent` gains `version`, stored `(name, version)`, exactly as `Blueprint` is

`types.Intent` gains `Version int` (default 1). `graph.intent` moves to a `(name, version)` primary key by
migration, and `UpsertIntent`/`GetIntent`/`DeleteIntent` take a version — the same shape
`graph.blueprint` already has. The desired-state plan keys intents by `name@version`, prunes by that key, and
splits it back on delete, reusing the Blueprint code path rather than a parallel one.

Deliberately a transposition, not a design: the machinery is proven, its failure modes are known, and one
mechanism for "a versioned CaC document" is worth more than a marginally better second one.

### D2 — The Assignment pins the Intent version, in the form it already uses for Blueprints

`Assignment` gains `IntentVersion int`, parsed from `intent: tls-app@3` — the same `name@N` grammar
`blueprint: web-server@1` uses, via the same parser (generalized from `splitBlueprintRef`). Unpinned means
`@1`, so every existing estate keeps working unchanged.

An Assignment therefore pins **both** halves of what it means: the WHAT (`intent@N`) and the HOW
(`blueprint@M`). That is the complete statement of "this environment is running this configuration through
this composition", and it is the unit promotion moves.

### D3 — Versioning applies only to Intent kinds an Assignment pins; provisioning kinds refuse it

`version:` on `Intent/Compute` or any `SingletonIntentKinds` member (`Intent/Subnet`, `Intent/Vlan`,
`Intent/Dmz`, `Intent/DnsRecord`) is **rejected at declaration**, with an error that says why: the provisioning
reconcile keys a desired fleet by Intent name and would read two versions as two exclusive claims on the same
instance identities, and there is no Assignment to select a version with.

This is a scope boundary, not a deferral — versioning a fleet declaration is not a ring rollout, it is a
double claim on the same machines. Rolling infrastructure through rings is a different mechanism (distinct
Intents, or ADR-0120's keyed spread), and conflating them would put ADR-0058's instance-identity claim in
permanent conflict with this ADR's storage identity.

Rejecting it loudly is the §1.8 choice. Accepting it and having `provision.Plan` fail later with an
exclusive-claim collision would blame the wrong document.

### D4 — `CompiledOrigin` carries the Intent version

`CompiledOrigin` gains `IntentVersion int`, for two reasons that are both load-bearing:

- **§1.8 descent.** It already records `BlueprintVersion`; recording the Intent's name without its version
  would make the descent from a Finding ambiguous the moment two versions exist — "which configuration
  produced this expectation" would be unanswerable from the compiled artifact.
- **The orphan branch needs it.** The withdrawn-Assignment path re-reads the Intent to learn its `onRemove`
  semantics (`compiler.go:338`, `s.GetIntent(ctx, eb.CompiledFrom.Intent)`). With versioned storage that read
  needs a version, and the only correct one is the version the Baseline was compiled from.

### D5 — Promotion is a reviewed Git bump; immutability is the absence of one

Promotion test → stage → prod is editing the target environment's Assignment to pin the new refs. Between
bumps, editing an Intent creates or updates a **different** version and **cannot** change what prod is
running — which is exactly "immutable once it passes go", achieved structurally rather than by policy.

This is Crossplane's `compositionUpdatePolicy: Manual` + `compositionRevisionRef` in Stratt's vocabulary, and
the same discipline `Assignment.BlueprintVersion` already applies to the HOW. Recorded because the alternative
— a branch per environment — is the industry default and is rejected below.

**§4.3 obligation, stated:** bumping a pinned version changes the compiled expectations for that Assignment,
so it goes through the existing compile-diff and max-delta gate like any other change. Promotion is not a
bypass, and this ADR adds no exemption.

## Blast radius (enumerated, per the ADR-0118 review's condition)

| Path                                                       | Change                                                                   |
| ---------------------------------------------------------- | ------------------------------------------------------------------------ |
| `types.Intent`, `types.Assignment`, `types.CompiledOrigin` | new fields (D1, D2, D4)                                                  |
| `graph.intent` + `intentstore.go`                          | migration to `(name, version)`; version on Get/Upsert/Delete             |
| `desiredstate` intent plan/prune/apply                     | key by `name@version`, mirroring `computeBlueprintPlan`                  |
| `desiredstate` Intent validation                           | reject `version:` on provisioning kinds (D3)                             |
| `compiler.validateRefs`                                    | `GetIntent(name, version)` from the Assignment's pin                     |
| `compiler` orphan branch                                   | read the Intent at `CompiledFrom.IntentVersion`                          |
| `compiler.compiledBaseline`                                | stamp `IntentVersion`                                                    |
| `reconcileProvisioning`                                    | unaffected **by construction** — D3 keeps provisioning kinds unversioned |
| `api/recertification.go`, `api/server.go`                  | `GetIntent`/`ListIntents` callers gain the version                       |
| OpenAPI + generated clients + UI                           | `version` on Intent, `intentVersion` on Assignment, `specLayers` sibling |
| `contracts/intents/*`                                      | unchanged — schema-shape versioning is a **different axis** (see below)  |

**The distinction this ADR must not blur:** `contracts/intents/compute.v3.schema.json` versions the _shape_ of
a spec, resolved newest-wins by the contract registry. D1 versions the _configuration document_. Both are
legitimate and orthogonal; conflating them would be a category error, and the naming (`Intent.version` vs a
`.vN.schema.json` file) is deliberately distinct.

## Charter alignment

- **§2.4** — no precedence is introduced: a pin is an explicit reference, and two versions never merge. The
  existing anti-GPO posture is untouched.
- **§1.2** — versions are CaC-only Git desired state; nothing new is written to the graph by a reconcile.
- **§2** — no new Named Kind; `version`/`intentVersion` mirror `Blueprint.version`/`blueprintVersion` exactly.
- **§1.8** — a pinned version that no longer exists fails the Assignment loudly (inherited, verified); a
  version on a provisioning kind is refused at declaration rather than failing later in the wrong place.
- **§4.3** — a promotion is an ordinary compile diff, gated like any other.

## Consequences

- **Positive.** Rings become real: test, stage and prod can run three configurations of one Intent
  simultaneously. Prod stops being one careless edit away from changing. Promotion becomes a reviewable
  two-line diff with a full audit trail in Git.
- **Every estate gains a version axis to think about.** Unpinned defaults to `@1`, so nothing breaks, but the
  concept is now present in every Intent document whether or not an estate uses rings.
- **Provisioning Intents are asymmetric** — versioned configuration for application-shaped Intents, not for
  fleet declarations. That asymmetry is real and must be documented in the estate guide, not just here.
- **Migration is the risky part.** Changing a primary key on a live table is the one irreversible step; it
  needs a backfill (`version = 1` for every existing row) and must be idempotent.

## Alternatives considered

- **Content-hash pinning** (the Assignment pins a digest of the resolved spec) — rejected: it delivers
  immutability but not rings, because one document cannot serve two configurations at once. Recorded at length
  in Context because it is the cheaper design and will be proposed again.
- **A Git branch per environment** — rejected. ADR-0057's whole model is _one_ estate repo with environment as
  a selector inside it; branches would fork the estate, make "what is prod running" a cross-branch diff, and
  reintroduce merge conflicts as a configuration mechanism.
- **Versioning the Assignment instead of the Intent** — rejected: the Assignment is already per-environment, so
  versioning it answers nothing. The thing that needs holding still is the shared WHAT.
- **Versioning provisioning Intents too, with a selector** — rejected (D3): there is no Assignment to select
  with, and the Intent name is the fleet identity, so two live versions are two claims on the same machines.
- **A `promoted: true` flag or a promotion API** — rejected: promotion is a Git edit under review, not a
  runtime verb. A runtime promotion verb would put desired state outside Git (§1.2).

## Follow-ups

- **Unpinned `intent:` as a hard error**, once every in-repo Assignment pins explicitly — the same hardening
  path `blueprint:` never needed because it was born required.
- **An estate-guide section on rings**, since D3's asymmetry (application-shaped Intents version, fleet
  declarations do not) is not discoverable from the schemas alone.
- **Promotion ergonomics** — a `stratt promote <assignment> --to <env>` CLI that edits the pins and shows the
  compile diff, if hand-editing proves error-prone. Deliberately not proposed now: the mechanism should earn
  its sugar.
