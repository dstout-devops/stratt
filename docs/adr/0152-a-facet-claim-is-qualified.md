# ADR 0152 — A Facet claim is qualified: many managed applications on one host

- **Status:** Proposed
- **Date:** 2026-07-31
- **Deciders:** platform stewards
- **Charter sections:** §1.1 (type the seams), §1.2 (projections, enforced in the data layer), §1.4
  (boring spine), §1.5 (the port is the contract), §1.8 (never hide diagnosis), §2.1 (Facet), §2.4
  (claim types, the anti-GPO axiom), §9 (ontology creep)

## Context

**One host may run exactly one Stratt-managed application.** That is the whole problem, and it is
structural rather than a config choice. A Facet is keyed `(entity_id, namespace, prov_source_id)`
(`graph.facet` PK — `00001_graph_spine.sql:92`, re-keyed by `00035_multi_source_facet.sql:34`), and
an exclusive claim is exclusive at `(namespace, entity)` — `detectClaimConflicts`,
`core/internal/compiler/compiler.go:884`, literally `key struct{ ns, entity string }`. There is no
instance dimension anywhere in either. So apache converging `app.config` on `web-02` and tomcat
converging `app.config` on `web-02` is a double-claim, and the compiler correctly refuses it.

A host running apache on `:80` and tomcat on `:8080` is ordinary. ADR-0148 **D6** recorded the limit
rather than letting an adopter discover it, and deferred the fix in its own words: _"distinguishing
two applications on one host needs a per-application key in the claim — which is a claim-model
change and belongs in its own ADR, not smuggled in here."_ Its follow-up **(c)** names this ADR.
**This is that ADR.**

### The limit is visible in the estate, in three files that each explain themselves

The workaround shipped, and it is honest about being one:

- `estate/views/managed-app.yaml` — _"Its OWN View, not managed-web's, because the app.config claim
  is EXCLUSIVE: two Blueprints converging one Entity's app.config is the §2.4 double-claim and would
  (correctly) be a compile error."_
- `estate/views/managed-tomcat.yaml` — the same sentence, for the other side.
- `estate/hosts/managed-tomcat.yaml` — _"Its own label, its own View — one application per node,
  because app.config is a closed {port} facet with no application dimension and a claim is
  per-namespace."_

So the reference estate runs **two managed nodes to run two applications**, and says why in three
places. That is the cost, stated in the artifact rather than in a tracker.

`svc-fleet` / `view:svc-servers` — which `docs/declaration-map.md` §6 still describes as living
workarounds whose deletion is this ADR's call — **were already reverted** (`3dcd66a`, register item
12). There is nothing here to delete. The map's phrasing is stale and this ADR corrects it.

### What this is NOT, because two shipped seams look like prior art and are not

- **ADR-0083 already made co-management first-class**, via the route map
  (`mgmt.channels: {apps: intune, certs: ansible, files: ansible}`). That is co-management **across
  namespaces** — several capabilities on one box, each owning a different Facet. This ADR is
  co-instancing **within one namespace** — several claimants on one box wanting the _same_ Facet.
  Different axis; 0083 neither solves nor blocks it.
- **ADR-0060 already migrated the Facet grain once**, adding the source dimension so many sources may
  project one namespace. That dimension exists for **competing signals about one fact** — which is
  why the read path collapses it back to one value via the declared authority
  (`reader.go:174-180`) and why undeclared multiplicity raises a contention Finding. A qualifier is
  the opposite: **genuinely coexisting facts.** apache AND tomcat, not apache OR tomcat. It cannot
  reuse the authoritative-pick machinery, and saying so is load-bearing — the two dimensions must not
  be conflated at the read path or a second application will look like a contended one.

### Three constraints fixed before drafting

`docs/declaration-map.md` §6 settled these from the layer model, and this ADR adopts them:

1. **The key belongs on the application, not the endpoint.** Apache legitimately holds `:80` and
   `:443` under one config; keying by port would either duplicate the config or require electing a
   "primary port", which is assembling a fact by convention (§1.4).
2. **The key must be Derived, never Observed.** A key sourced from L2 is undetectable until both Runs
   have executed — both compile green, both dispatch, and the winner is whoever bound the socket
   first. That is execution-order precedence: §2.4 broken _without a field anyone can review_, and
   diagnosis moved later, inverting §1.8.
3. **An endpoint may be a key value. It may never become `Entity/Port`.** That is §9 ontology creep
   and the start of "Stratt models sockets."

### And the word had to be picked first

`resource` — the natural English word for the thing that is scarce — is **banned in core-model
identifiers** (charter §2). The map instructed that the replacement be chosen and linted _before_ it
reaches an identifier or an error string. It was; see D1.

## Decision

### D1 — The word is `qualifier`, and the rejected candidates are recorded

A qualified Facet is one whose namespace alone does not identify it on its Entity: `app.config`
qualified by `apache` and `app.config` qualified by `tomcat` are two facts, not two opinions about
one. The **qualifier** is the string that distinguishes them.

Rejected, each for a reason found by scanning rather than by taste:

| Candidate         | Verdict                                                                                                                                                                                                                                                                                              |
| ----------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `resource`        | **Banned**, charter §2. The word the concept wants, which is why the map made picking a replacement a prerequisite.                                                                                                                                                                                  |
| `instance`        | **Collides twice.** `instance.compute`/`instance.network`/`instance.state` are shipped Facet namespaces meaning facts about an EC2 machine (ADR-0095), and `provision.Instance` is a built unit. In an estate holding both EC2 hosts and web applications, "instance key" would read as the machine. |
| `slot`            | **Informally taken** for a different axis: the provisioning ADRs (0059, 0110, 0113, 0120) use "slot" for the place a capability PROVIDER is bound into. To a reader of those, "slot" says _which provider_, not _which application_.                                                                 |
| `claim`           | **Frozen** (§2.4 Named Kind). ADR-0054 already fought this overload and renamed its concept to write-_scope_ to avoid it.                                                                                                                                                                            |
| `scope` / `grant` | Taken — per-Run write ceiling (ADR-0054) and plugin registration ceiling (ADR-0047/0051) respectively.                                                                                                                                                                                               |
| `coordinate`      | Taken in spirit by ADR-0143/0144/0146/0147 (the resolved reach coordinate).                                                                                                                                                                                                                          |

`qualifier` is free in every core-model surface — no Go identifier, schema property, API route, DB
column, CLI noun or estate field uses it, and it collides with no frozen Named Kind. Verdict from
`vocabulary-linter`: SAFE, recommended.

One honest caveat about the supporting evidence: the corpus's only prior use of the word is a
comment, `types/workflow.go:51`, _"the source qualifier (Controller id)"_ — and that names the
**source** dimension, the very thing D5 insists must never be conflated with this one. It is prose,
not an identifier, so nothing is blocked and the choice stands on the rejection table above. But it
is weak contrary evidence rather than precedent, and recording it as support would have been the
kind of small dishonesty that makes a reader distrust the rest.

### D2 — A claim is qualified: the exclusivity key becomes `(Entity, namespace, qualifier)`

`BlueprintRoute.observe` gains `qualifier`, a **template over the resolved spec** resolved at
compile exactly as `observe.path` / `observe.equals` / `remediationParams` already are:

```yaml
routes:
  - observe:
      namespace: app.config
      qualifier: "{{.spec.package}}" # apache here, tomcat there
      path: port
      equals: "{{.spec.port}}"
    claim: exclusive
```

`claimRecord` (`compiler.go:122`) and `detectClaimConflicts`'s key gain the third component. Two
exclusive claims on one `(Entity, namespace, qualifier)` still fail the compile naming both
Assignments; two claims differing only in qualifier compile and both run.

**Omitted means the empty qualifier**, which is today's behaviour exactly. No existing declaration
moves and no existing claim changes meaning. The qualifier is never defaulted from `Blueprint.
delivers` or anything else: a silent default would change the grain of every shipped estate on
upgrade, which is a §2.4-shaped surprise even when the value chosen is the obvious one.

**Absent and empty must not render the same, though.** A qualifier key that is PRESENT and resolves
to `""` — a typo in `{{.spec.pakcage}}`, or a spec field the Assignment never set — is a **compile
error** naming the route and the unresolved token. Without that rule the route still compiles and
its exclusivity grain quietly slides from `(entity, app.config, apache)` back to
`(entity, app.config)`: the grain of an exclusive claim moving by accident, which is the same
surprise the paragraph above refuses to inflict deliberately.

The compile error gains the qualifier, because the whole §1.8 motivation is that today's message
says _two Blueprints claim `app.config`_ when the true statement is _apache and tomcat both want to
own the web configuration of web-02_.

### D3 — The qualifier is DERIVED at compile, and the observed listener is drift-only

Constraint 2, adopted as a rule: **a qualifier may only ever come from the resolved spec** —
Blueprint defaults + Intent spec + Assignment values, the same overlay every other compiled value
comes from. It is never read from a Facet, never from a Run's fact-back, never from what is actually
listening on the target.

The satisfiable shape is **two values, not one**: the claim key, derived at compile (legitimate — a
declaration deciding what it manages), and the actual listener, an L2 fact used **only for drift**.
They agree when the Run succeeded and diverge when it did not, which is precisely what ADR-0148 D4's
fact-back exists to catch.

This is the decision that keeps the port out of it (D6).

### D4 — The FACET grain gains the qualifier too, enforced in the data layer

Keying only the claim would be worse than doing nothing. Two Blueprints would compile green, both
dispatch, and both write `app.config` on `web-02` — into **one row**, because the PK does not know
about qualifiers. The last Run wins, silently, and the drift loop then reports the loser as drifted
forever. That converts a compile error (visible, refusable, §2.4-compliant) into execution-order
precedence (invisible), which is exactly what D3 refuses.

Verified against the write path rather than assumed: `upsertFacetTx` does
`ON CONFLICT (entity_id, namespace, prov_source_id) DO UPDATE`
(`core/internal/graph/projector.go:267`), and `ProjectFacts` stamps Run provenance with no
`SourceID` (`orchestrate.go:1917`) — so an apache Run and a tomcat Run target the byte-identical row
and the second overwrites the first. And the §1.8 descent could not untangle it either:
`facet_history` would record the two applications as **one interleaved version chain** flapping
between `80` and `8080`, which reads exactly like one application that cannot hold its config.

So `graph.facet` and `graph.facet_history` gain a `qualifier text NOT NULL DEFAULT ''` column and it
enters both primary keys. §1.2 is explicit that this class of invariant is enforced in the data
layer and not by convention, and that ON CONFLICT target is the literal enforcement point.

`facet_owner` does **not** gain the column. Ownership is a registration of _who may write a
namespace_ (§2.1); it has never been per-Entity and a qualifier is finer than per-Entity. A qualified
write is still gated by the same namespace registration.

### D5 — Multiplicity by qualifier is coexistence, and the suppression it causes must be VISIBLE

The read paths must distinguish the two dimensions, and the safe default is to refuse rather than
pick. But refusing is only §1.8-honest if the refusal is legible, and the first draft of this
decision asserted a fail-closed behaviour the code does not have. Corrected here, because the
correction is the substance:

- `FacetValuesByEntities` (`reader.go:163`) — the scalar routing read behind `mgmt.address` /
  `mgmt.site` — resolves the **empty qualifier only**, so a qualified namespace reads as _absent_
  rather than as one arbitrary member. ADR-0060's authoritative-source join is untouched and still
  resolves _competing sources_ within one qualifier.

  **Absent is not the same as diagnosed, and two of the three callers do not diagnose it.**
  `ResolveTargets` (`orchestrate.go:748`) turns a missing `mgmt.address` into `Target{Address: ""}`
  with no error, no event and no Finding — what eventually surfaces is a connection failure, which
  tells the operator the wrong thing. Routed dispatch (`orchestrate.go:825`) turns a missing
  `mgmt.site` into `types.LocalSite` **silently**, which is a Run executed at the wrong locus with no
  signal at all. Only `reachvia.go:66` genuinely fails closed (it errors at `:72` on an empty hop).

  Worse, the compensation that makes today's omit-rather-than-pick honest is the ownership-contention
  Finding — `FacetValuesByEntities` says so in its own comment (`reader.go:203-206`) — and this
  decision deliberately stops that Finding firing on qualifier multiplicity. Without a replacement,
  a qualified scalar namespace would vanish with **zero diagnosis anywhere in the system**: precisely
  the dropped-reach-coordinate-that-reports-as-nothing failure ADR-0054's own comment warns about.

  So the suppression carries its own signal. `FacetValuesByEntities` returns the qualifiers it
  suppressed per Entity alongside the values, and a scalar read that came back empty **because** the
  namespace is qualified raises a distinct `qualified-scalar-read` Finding naming the Entity, the
  namespace and the qualifiers present — never the `ownership` framework, which means something else.
  `orchestrate.go:748` and `:825` are therefore sites this ADR **changes**, not sites that already
  behave; they are counted in the layer list below.

- `GetFacets` returns every row with its qualifier; the API `Facet` schema
  (`core/api/openapi.yaml:1394`) gains the field, and `EntityDocument.facets` stops implying one
  entry per namespace. This is an OpenAPI-first change, so the generated client and the UI's Facet
  rendering move with it.
- The Baseline evaluator (`core/internal/orchestrate/baseline.go:158`) flattens
  `byNS[f.Namespace] = f.Value`, which would pick one of N qualified rows by row order. It keys by
  `(namespace, qualifier)` and each compiled expectation carries the qualifier its route claimed.
  **This does not also fix the multi-SOURCE order dependence of that same line, and the first draft
  claimed it did.** Two sources projecting one `(namespace, qualifier)` still collide in the map by
  row order after the re-key; fixing that needs the declared-authority collapse
  `FacetValuesByEntities` performs, which this ADR does not extend to the Baseline evaluator. It is
  not reachable today — checked rather than assumed: of the namespaces a shipped Baseline observes
  (`app.config`, `app.deliverable`, `access.grants`, `fileset.content`, `cert.presented`,
  `stratt-apps`), only `app.deliverable` has a Syncer writer (`plugins/kubeservices/server.go:54`)
  and no Run write-back scope includes it, so every observed namespace has exactly one writer. It
  becomes reachable the day someone adds a write-scope, and it is booked as follow-up 6 rather than
  claimed as fixed.
- `WriteFacetContentionFindings` (`core/internal/graph/findingstore.go:338`) groups by
  `(entity_id, namespace)` and reports contention when >1 source projects the pair. It must group by
  qualifier as well, or two applications projected by two different sources report as an ownership
  contention that does not exist. **Grouping alone is not enough**, and this is the second thing the
  first draft got wrong: the Finding's identity is `'ownership/' || namespace` on `target = entity_id`
  with `ON CONFLICT (baseline, target)`, so two genuine contentions on `(web-02, app.config)` under
  different qualifiers would collide on one row and one would silently overwrite the other's `diff` —
  last-writer-wins at the diagnosis layer, §2.4's failure shape in the one place whose whole job is
  to prevent it. The Finding baseline becomes `'ownership/' || namespace || '@' || qualifier`; the
  empty qualifier renders exactly as today, so no shipped Finding re-keys. And
  `ResolveClearedFacetContentionFindings` (`findingstore.go:386`) groups by the same pair and moves
  in the **same change** — if only the write half learns about qualifiers, a contention that clears
  under one qualifier stays open forever while another still has two sources.
- `EntityTemplateNamespace` (`reader.go:487`) — the `{{.entity.*}}` tree — carries the **empty
  qualifier only**. A qualified namespace is refused there, naming the qualifiers present, rather
  than being nested under an invented extra level. Same reasoning charter-guardian applied to the
  reserved-key collision this function already refuses: ADR-0150 D2 binds certificate subjects out of
  this tree, and a silently-shadowed value there is a certificate issued for the wrong subject.

### D6 — Core stamps the qualifier; a writer never proposes one. **The port does not change.**

Because D3 makes the qualifier a compiled property of the claim, the core already knows it when it
dispatches the remediation Run. The write-back therefore carries no qualifier on the wire: the
plugin reports `app.config` exactly as it always has, and the core writes it into the qualifier the
Baseline claimed.

`ObservedEntity.facets` (`proto/stratt/plugin/v1/plugin.proto:372`) stays
`map<string, bytes> // namespace -> value blob`. This is a real fork and it is decided deliberately
rather than left implicit. The port is pinned and hash-verified (§1.5), so a field there is one-way.
And what a wire qualifier would let a plugin do is **write the observed row another claim's drift
evaluation reads** — making a foreign Baseline report SATISFIED on a fact that Baseline's own Run
never produced. (Stated precisely, because an earlier draft called this "acquiring a claim it was
never granted" and that is wrong: claims live in the compiled Baseline, which is Git-derived, and no
plugin can acquire one. The harm is state spoofing, which is the same harm class the namespace-level
`FacetWriteScope` grant already permits — this would merely make it finer-grained and therefore
harder to see.)

**The middle option is named and rejected rather than excluded by construction**: bound the wire
qualifier to the set the Run's own Baseline claimed, enforced at the same governor as
`FacetWriteScope`. That is implementable. It is declined because it buys nothing — the core already
knows the qualifier, so the field could only ever carry a value the core would otherwise supply, and
it would widen a permanent contract to let a plugin restate a fact core is authoritative for.

**The consequence is booked, and it is smaller than the first draft claimed:** a **Syncer** cannot
project two same-namespace facts for one Entity. That is not a capability this ADR removes — it is
one that has never existed. The Facet PK is `(entity_id, namespace, prov_source_id)` and one Syncer
is one source, so today a Syncer already gets exactly one row per `(entity, namespace)`. This ADR
declines to _add_ the capability, which under §1.1 is the compliant choice: type a seam when
something shipping demands it, and no Connector demands this one. Observing "this host runs two
apaches" therefore stays inexpressible until an observed-key decision is taken on its own merits.

**How the qualifier actually reaches the write is part of this decision, not an implementation
detail** — it is the only thing holding "the port does not change" together. `ProjectFacts`
(`orchestrate.go:1913`) calls `UpsertFacet(ctx, prov, entityID, ns, value)` with no qualifier in
scope, and `RunInput` carries only `FacetWriteScope` (`launch.go:41`). The carrier is a
namespace→qualifier map on `RunInput`, resolved at launch from the Baseline's route (one Baseline,
one route, one resolved spec per Run). It must land at **both** write-back doors: `executeJobPlugin`
maps governed facets into `res.Facts` (`orchestrate.go:1620`), while `executePlugin`'s gRPC path maps
`raw.WriteBack → res.Entities` carrying Kind/IdentityKeys/Labels only (`orchestrate.go:1427-1430`).
Whatever stamps the qualifier must land at both or it becomes transport-dependent — the
sibling-path divergence `orchestrate.go:815` already warns about in its own comment. (That the two
doors differ on facets **today** is adjacent to this ADR and not caused by it; it is booked as
follow-up 7 and needs checking on its own.)

### D7 — An endpoint may be a qualifier value; `Entity/Port` is refused

Nothing stops an estate qualifying by `0.0.0.0:443` where that is the honest discriminator — the
genuinely 1:1 tuple is `(Entity, bind-address, protocol, port)`, and `0.0.0.0:80` and `127.0.0.1:80`
coexist. That is a Facet's business.

Promoting an endpoint to an Entity kind is refused: it is §9 ontology creep, it would give every
socket identity, correlation, presence and tombstone semantics, and Stratt would be modelling
sockets. Recorded as a decision so "nobody looked" and "we looked and said no" do not render the
same. Reopening it requires an ADR that supersedes this one — an ADR cannot make anything permanent,
and the register that can is the charter's non-goals list (§7.5), which this does not join today.

### D8 — `FacetWriteScope` stays namespace-only

ADR-0054's per-Run write ceiling is a list of namespaces, checked against the Actuator's registered
grant (`orchestrate.go:1052`, `GovernStream` at `:1533`). It does not gain a qualifier dimension.

This holds only while D6 does. If a wire qualifier is ever admitted, the ceiling must gain the
dimension in the same change — otherwise a Run could write a qualifier its claim never named, and
the namespace-level ceiling would not notice.

A Run's authority is "may this actuation write `app.config` at all", and the qualifier is decided by
the core from the claim, not by the Run — so a qualifier-aware write-scope would be a ceiling on a
value the Run cannot influence. It would add a field that can only ever agree with the claim, which
is a second statement of one fact (§2.4). Unchanged.

### D9 — The estate collapses to one node running both applications, and that is the acceptance test

`managed-tomcat` (host, View) exists only because of this limit. When this ADR is implemented,
`apache-tier` and `tomcat-tier` both bind to `managed-app`, both converge, and both Findings resolve
against **one** Entity — with `managed-tomcat` retired in the same change. Until that has actually
run, this ADR stays **Proposed**: a claim-model change verified only by unit tests would be
verifying the thing that was easy to change rather than the thing that was hard.

**That proof cannot land in the expand release**, and saying so keeps the status honest. While
`facet_pkey` is still `(entity_id, namespace, prov_source_id)`, a second row differing only in
qualifier violates it — so the expand release ships the column, the index and every reader/writer,
and still cannot store two qualified facts. D9 is obtainable only after the contract release folds
the column into the key. Two releases, then Accepted.

### D10 — A DECLARED qualifier is refused at estate load until the contract release

Added during implementation, because building the expand release surfaced a shape D2 did not
cover: not the omitted qualifier (which is today's grain, safely) but the **declared** one.

With the claim key widened and the Facet key not yet, a Blueprint route declaring
`observe.qualifier` would COMPILE — `detectClaimConflicts` correctly sees two distinct claims — and
then both Runs would write through `ON CONFLICT (entity_id, namespace, prov_source_id)`. The second
would match the first's row and `DO UPDATE` it, **flipping its qualifier**. Not a constraint
violation. A silent last-writer-wins: precisely the failure D4 widened the Facet key to abolish,
reintroduced by shipping half of the widening.

So `observe.qualifier` is a **load-time refusal** in this release, and the field exists on the YAML
shape purely so the refusal can explain itself — without it, `KnownFields` answers an author who
followed this ADR with `field qualifier not found in type`, and the ADR reads as fiction. The
message names the ADR, the constraint that is not yet widened, what would happen if it were
honoured, and what to do now.

Refusing beats half-honouring: a declaration that reads as permitted and is honoured by something
other than what it says is worse than one that is refused (§1.8). The refusal lifts in the same
change that folds the primary key.

## Charter alignment

- **§2.4 (anti-GPO), upheld and narrowed.** The frozen wording is _"one Assignment may claim it per
  Entity."_ Every implementation reads "it" as `(Entity, namespace)`; this ADR reads it as
  `(Entity, namespace, qualifier)`. That reading is defensible — "it" is _a Facet_, and narrowing the
  claim key introduces no precedence. The axiom is untouched: collisions still fail compile, still
  name every claimant, and there is still no precedence field anywhere.

- **§2.1 (the Named Kind) IS what moves, and an earlier draft of this section had it wrong.** The
  frozen definition is _"Facet — a **named**, schema'd fragment of an Entity's document."_ After this
  ADR two fragments with the SAME name coexist on one Entity as distinct facts. ADR-0060 did not do
  that: its rows collapse back to one effective value at read time via declared authority, so "a
  named fragment" still described one value — which is exactly why this ADR argues so hard that the
  two dimensions differ, and that argument is itself the evidence that §2.1's identity changes.

  So **a charter amendment is proposed** (it is not made here — §1/§2 carry the project's highest
  review bar, and the charter is edited only on explicit steward instruction):

  > **§2.1, Facet** — add: _a Facet is identified on an Entity by `(namespace, qualifier)`; the
  > qualifier defaults to empty and an unqualified Facet is the ordinary case._
  > **§2.4, Claim types** — read "one Assignment may claim it per Entity" as _per Entity, per
  > qualifier_.

  Supporting context, offered as a reason to record this rather than as cover for not recording it:
  §2.1's _"Two writers to one namespace is a registration error"_ **already** diverged from the
  implementation when ADR-0060 shipped multi-owner `facet_owner` rows without a charter edit. One
  undocumented divergence is an argument for writing the next one down.

- **§1.2 (projections, enforced in the data layer).** The grain change lands in the PK and the
  upsert conflict target, not in a review norm.
- **§1.1 (type the seams).** No new schema is attached to anything: `app.config` stays the closed
  `{port}` document it is, and the qualifier is identity, not payload. The Syncer-side observed key
  is deliberately NOT typed, because nothing shipping demands it yet (D6).
- **§1.5 (the port is the contract).** The pinned port is untouched, by a decision with a stated harm
  model and a named-and-rejected middle option, rather than by omission.
- **§1.8 (never hide diagnosis).** The compile error gains the qualifier. Every read path that could
  silently pick one of N returns absent instead — **and, because the first draft of D5 asserted a
  fail-closed behaviour two of the three callers do not have, the suppression now carries its own
  Finding.** Absent is not a diagnosis; absent-plus-a-Finding-naming-the-qualifiers is.
- **§9 (ontology creep).** Explicitly refused for endpoints (D7).
- **Non-goals.** No writable CMDB, no new configuration language, no paid tier. The qualifier adds no
  ontology — it is a key, not a kind.

## Consequences

- **Positive:** the ordinary topology becomes expressible — a host reverse-proxying tomcat behind
  apache, a node running an app and a sidecar-ish second service. The reference estate loses a
  fixture host that exists only to dodge a modelling limit. The compile error starts naming what is
  actually contended.
- **Negative / trade-offs:** a **fourth** dimension on a primary key that has already been re-keyed
  once (ADR-0060). Six layers move: the DB (both PKs, the `enforce_facet_owner` and
  `facet_version_from_history` triggers in `00036`/`00046`), the projector's upsert, the Run's
  write-back carrier at **both** transport doors (D6), four read paths, the compiler's claim record,
  and the OpenAPI Facet schema with its generated UI client. Two call sites that today degrade
  silently on a missing scalar Facet (`orchestrate.go:748`, `:825`) have to learn to say why. A
  Syncer still cannot observe two instances of one namespace — a limit this ADR declines to lift,
  not one it imposes (D6).
- **Migration is expand/contract (ADR-0078), not a one-shot re-key.** `00035` got away with
  `DROP CONSTRAINT` / `ADD CONSTRAINT` in one release only because it was explicitly grandfathered
  (`-- expand/contract-ok: pre-dates UPG-1/ADR-0078`). This one does not get that: `task migrate:lint`
  greps the Up for destructive statements and would flag it, correctly. The sequence is **expand** —
  `ADD COLUMN qualifier text NOT NULL DEFAULT ''` (which the lint does not flag, since it matches no
  destructive pattern), a unique index over the widened key, and every writer and reader taught —
  then **contract** in a later release, folding the column into the PK. The previous release's
  replicas keep working throughout, which is the point of the discipline.

  Two details that are easy to miss and would each be a real defect: `facet_history`'s key is
  **five** columns after this, not four (`version` is part of it), and `00046`'s
  `facet_version_from_history` trigger must gain the qualifier in its `WHERE` — otherwise a second
  application's first write collides with the first's history at version 1, which is precisely the
  defect `00046` was written to fix.

### Follow-ups

1. Implement D2–D5 and D9 across the expand and contract releases; flip to Accepted only on the live
   estate proof (both applications converging one node, both Findings resolving), which cannot land
   before the contract release.
2. **The §2.1/§2.4 charter amendment proposed above needs an explicit steward decision.** It is not
   made by this ADR and must not be made silently by the implementation.
3. `docs/declaration-map.md` §6 and register item 1: close the item, and correct the stale claim
   that `svc-fleet` / `view:svc-servers` are live workarounds awaiting deletion.
4. ADR-0148 follow-up (c) is closed by this ADR; its D1–D5 and D7 are unaffected (still one
   Blueprint per delivered application — that is what makes N claimants on one host happen at all).
5. The observed-instance question D6 leaves open (a Syncer projecting two same-namespace facts)
   needs its own decision if and when a Connector demands it. It is a port change; do not add the
   field before then.
6. **The Baseline evaluator's multi-SOURCE flatten** (`baseline.go:158`) stays order-dependent after
   this ADR — see D5. Not reachable today (every observed namespace has one writer, checked), and it
   becomes reachable the day a write-scope is added to a Syncer-owned namespace. Fixing it means
   giving the evaluator the declared-authority collapse `FacetValuesByEntities` already performs.
7. ~~**The two Actuator write-back doors already differ on facets**~~ — **checked and CLOSED before
   this ADR is implemented, because D6 cannot stand on a door that carries no facets at all.** The
   gRPC Apply door mapped `raw.WriteBack → res.Entities` as Kind/IdentityKeys/Labels and discarded
   `ApplyEntity.Facets`, which the governor had just admitted against grant ∩ write-scope — while
   the EE-Job door routed the same governed shape to `res.Facts` and projected it. Not reachable in
   the shipped estate, and the way it was not reachable is the argument for fixing it: `dns.yaml`
   and `awsec2.yaml` each DECLINE the `facetNamespaces` grant, both saying in a comment that it
   would be "authority granted for a path that does not exist". The estate had routed around a hole
   in core. `EntityObservation` now carries `Facets`, `ProjectFacts` writes them against the Entity
   it upserts, and both doors lift them through one shared function so they cannot drift apart
   again. (The **Action** path still carries no facets, deliberately — ADR-0113 D3.)
8. Audit the UI's `EntityDocument.facets` consumers (`ui/src/lib/data.ts`, `ui/src/lib/schema.ts`,
   `ui/src/screens/graph.tsx`, `ui/src/components/schema-value.tsx`) for anything assuming one Facet
   per namespace per Entity.

## Alternatives considered

- **Key the claim only, leave the Facet grain alone** — rejected: it moves the collision from
  compile time to Run execution order and makes it invisible (D4). Strictly worse than the current
  refusal.
- **Key by endpoint / port** — rejected by map constraint 1: apache holds `:80` and `:443` under one
  config, so this either duplicates the config or requires electing a "primary port", assembling a
  fact by convention (§1.4). An endpoint remains available as a qualifier _value_ where it is the
  honest discriminator (D7).
- **Model each application as its own Entity** — rejected: §9 ontology creep, and it would require
  identity, correlation, presence and tombstone semantics for a thing no Source enumerates. The
  graph would acquire a second population that only Stratt believes in — a second truth (§1.2).
- **Reuse ADR-0060's source dimension** (project apache and tomcat as different "sources") —
  rejected: that dimension exists for competing signals about one fact and collapses to one
  authoritative value at read time. Two applications are not two opinions, and encoding them this way
  would make every co-hosted node report a permanent ownership contention Finding.
- **Let the plugin declare the qualifier on the wire** — rejected (D6): a plugin could then write the
  observed row a foreign Baseline's drift evaluation reads, making that Baseline report SATISFIED on
  a fact its own Run never produced; and it would widen a pinned, one-way port (§1.5) to carry a
  value the core already knows.
- **Let the plugin declare it, bounded to the qualifiers its own Run claimed**, enforced at the
  `FacetWriteScope` governor — rejected (D6): implementable and safe, and still pure cost. The bound
  can only ever admit the value core would have supplied anyway, so it buys a permanent port field in
  exchange for nothing.
- **Split the namespace instead** (`app.config.apache`, `app.config.tomcat`) — rejected: it makes
  ownership registration, write-scope and every Facet schema per-application, and it puts data in a
  namespace, which is the same convention-over-structure mistake in a different place. It also
  collides head-on with the prefix-nesting refusal `EntityTemplateNamespace` already enforces.
