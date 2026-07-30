# The Declaration Map

**Status: living reference. It decides nothing.** It writes down what the shipped declaration layers
already are, what each thing's _grain_ is (what it is keyed by), and — the part never written down
anywhere — how a value is permitted to reach each field. Where this document and the code disagree,
**the code is right and this document is the bug**.

**This map is prose. Nothing validates against it, and it never becomes an enforced schema.** The
day the ladder below is emitted as a machine-readable meta-schema that declaration kinds must
conform to, it has become a universal ontology of Stratt's own model (§1.1) and a core-owned schema
imposed on plugin declarations (§1.5). That is the trapdoor; this line is the fence.

Its purpose is to stop one question being re-litigated per-ADR. Three decisions — ADR-0118 D1
(`environments` is a filter, not a selector) · ADR-0142 D4 (a coordinate is observed or caused,
never computed) · ADR-0151 D2 (whose first precedence rule was rejected) — are the _same_ question,
"how may a value reach this field, and at what grain?", asked three times because there was nowhere
to look it up.

## 0. What this is not

Charter §1.1 forbids a universal ontology over the **managed world**: schema attaches at plugin
boundaries and named Facets, never to whole Entities. Typing Stratt's _own_ declaration kinds is not
merely tolerated by the charter — it is **required** by it (§2.4 "each kind has a schema → generated
forms/validation"; §3.1 "Intent forms, Step inputs, Finding tables… generated from JSON Schema";
ADR-0118 D2, which makes `Workflow.inputs` a JSON Schema document precisely so no hand-rolled Go
validator sits beside the real one).

|                                   | Schema'd? | Why                                                                                                                       |
| --------------------------------- | --------- | ------------------------------------------------------------------------------------------------------------------------- |
| **Declaration kinds** (§2.1–§2.5) | **Yes**   | Our own documents; shapes already exist as Go structs + parsers                                                           |
| **Facets** (named)                | **Yes**   | §1.1 permits it — each must be demanded by a shipping Contract                                                            |
| **Plugin port messages**          | **Yes**   | §1.5 — the port _is_ the contract                                                                                         |
| **Entities**                      | **NEVER** | §1.1. An Entity is `{id, kind, identityKeys, labels, observedBy}`; its **Facet set is the typed document** §2.1 refers to |
| **The estate as a whole**         | **NEVER** | §9 ontology creep                                                                                                         |
| **This map**                      | **NEVER** | see the fence above                                                                                                       |

So: **map the seams, not the world.**

## 1. How a value reaches a field

### 1.1 Source classes

| Code  | Source                    | Who supplies it                                                      | Anchor                          |
| ----- | ------------------------- | -------------------------------------------------------------------- | ------------------------------- |
| **A** | **Authored** (estate CaC) | A human, in YAML under an estate root, reviewed in Git               | §1.2 desired state lives in Git |
| **P** | **Port-declared**         | A plugin, over `GetManifest`                                         | §1.5                            |
| **B** | **Bound**                 | Capability-binding resolution at plan time                           | ADR-0151                        |
| **O** | **Observed**              | A Syncer projecting an external system of record                     | §1.2 projections                |
| **C** | **Caused**                | A Run that made it true, stamped with Run provenance                 | §1.2, ADR-0142 D4               |
| **D** | **Derived**               | The compiler, from Intent × Blueprint × Assignment                   | §2.4                            |
| **R** | **Substituted**           | The ADR-0024 substituter at launch (`spec`/`event`/`param`/`entity`) | ADR-0024                        |

### 1.2 The claim rule — declaring vs yielding

Not "one source per field". `core/internal/overlay/overlay.go` already draws the correct line and it
is sharper:

> **At most one _declaring_ claimant per path. A _yielding_ layer fills only what is definitionally
> unset. Two declaring claimants are refused at load, naming both (§1.8).**

A default is overridable by definition — _"unset takes the default" is not a contest_, so it is not
precedence and §2.4 is not engaged. This one formulation subsumes ADR-0118 D1, ADR-0142 D4,
ADR-0151 D2's **rejected** rule _and_ ADR-0151 D2's **surviving** combination (`P ⊆ S ∧ |S| > 1` →
resolve from `P`, because "the substrate left the choice open and the author closed it"). A
one-source-per-field rule could not express the last of those at all, and would condemn three
shipped designs: the spec overlay, `Step.viewName` inheriting from the Assignment, and the L0
grant/manifest intersection below.

**It holds at N layers.** There is no numerical limit — the limit is structural.

### 1.3 Attestation — the L0 special case

L0 is neither A nor P alone. It is an **intersection where the authored grant is the ceiling and the
port manifest may only narrow it** (`core/internal/connectorregistry/registry.go`):

| Case                                                 | Outcome                                                                                              |
| ---------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| Declared in Git, **not** advertised by `GetManifest` | **Refused** — `phantom provider: declares provides %q but its Manifest does not advertise it (§1.5)` |
| Advertised by `GetManifest`, **not** granted in Git  | Silently dropped — _"the Manifest advertises, the grant is truth"_                                   |

That asymmetry is deliberate: a plugin cannot widen its own authority by claiming more over the
wire, and cannot silently lose authority the estate granted without the operator being told.

### 1.4 The no-computed corollary — scoped to L2

> **No computed _facts about the world_.** A reach coordinate is observed or caused, never assembled
> from other fields by a convention (ADR-0142 D4): a computed one is Stratt inventing a fact about
> DNS, and when DNS disagrees Stratt connects to the wrong host.

This binds **L2 only**. The compiler computes — that is what source class **D** is. Do not read this
corollary as forbidding derivation.

## 2. The layer ladder

Each layer may read the layers below and never above.

```
L7  ACCESS      CredentialRef · authz tuples · admission controls        A
L6  EXECUTION   Workflow · Step · Trigger                                A + R
              · Run                                                     C
L5  COMPILED    Baseline (+ CompiledOrigin)                              D | A     ← hand-authorable
L4  DESIRE      Intent (what) · Blueprint (how) · Assignment (bind)      A
L3  GROUPING    View                                                     A selector over L2
L2  POPULATION  Entity · Facet · Relation · label values                 O + C ONLY  ← hard wall
              · facet_owner / label_owner registry                      L0-fed (different plane)
L1  COMPOSITION CapabilityBinding · Environment                          A
L0  PORT        Actuator · Connector · Contract · Content                A ∧ P (attested, §1.3)
```

**The one hard wall — L2 attribute writes.** Enforced in the data layer exactly as §1.2 demands, not
by convention: `graph.facet` carries
`prov_writer_kind text NOT NULL CHECK (prov_writer_kind IN ('syncer','run'))` plus the
`facet_write_path` trigger (`migrations/00001_graph_spine.sql`). It scopes to **Entity, Facet,
Relation and label _values_**. The **ownership registry** (`facet_owner`, `label_owner`) is a
different plane — written by the registration path from L0 declarations, and neither Syncer nor Run.

`estate/hosts/` is not a counter-example: that file is a _plugin's own_ system-of-record, and it
reaches the graph as a **projection over the port**, never as a direct write.

## 3. Layer shapes

### L0 · Port `A ∧ P`

**Actuator** (`types/actuator.go`) — grain: `name`, unique per estate.

| Field                                        | Grain                  | Notes                                                                             |
| -------------------------------------------- | ---------------------- | --------------------------------------------------------------------------------- |
| `name`, `pluginIdentity`, `address`, `tier`  | actuator               | identity + reach                                                                  |
| `actionNames[]`                              | actuator               | the dispatch table (ADR-0031/0103)                                                |
| `provides[]` / `requires[]`                  | actuator               | capability advertisement — the **only** thing L1 binds against; attested per §1.3 |
| `provisions{Kind→Workflow}`                  | actuator × Intent kind | build routing. ⚠ targets are **not** resolved at load                             |
| `decommissions{}` / `remediates{}`           | actuator × kind        | same shape, other verbs                                                           |
| `substrate`                                  | actuator               | ADR-0151 — a property of the **provider**; nothing above a provider may name one  |
| `facetNamespaces[]`                          | actuator               | becomes a `graph.facet_owner` grant                                               |
| `labelKeys[]`                                | actuator               | asking for a key owned elsewhere fails the **whole** registration                 |
| `identitySchemes[]`                          | actuator               | how it names Entities                                                             |
| `content{}`, `contentDir`, `contentInputs[]` | actuator               | ADR-0134 content shipping                                                         |
| `outputContract`, `elevatedInputs[]`         | actuator               |                                                                                   |
| `environments[]`                             | actuator               | membership filter (ADR-0118 D1), never a value selector                           |

**Connector** — grain: `name`. Adds `class` (Syncer/Action/Emitter), `source`,
`authoritativeFacetNamespaces[]`, `tombstoneSchemes[]`.

> `tombstoneSchemes` decides whether disappearance means _gone_ or merely _unobserved_ (ADR-0042
> union liveness). Absent it, removal is never a deletion.

**Contracts — two kinds, and they are not equally verified.** Plugin Contracts and Facet schemas are
pinned, hash-verified JSON Schema **data**, never Go types (§1.5); drift is blocking. A Workflow's
`inputs`, by contrast, is a **Git-declared Contract, not a plugin schema** — §1.5's registration and
hash-pinning do _not_ apply to it (ADR-0118 D2). Do not describe both as hash-verified: a seam an
operator _believes_ is hash-verified when it is not is worse than an untyped one (§1.8).

**The port surface** (`proto/stratt/plugin/v1/plugin.proto`): `GetManifest` · `Health` ·
`Observe`⇄ · `Plan` · `Apply`⇄ · `Destroy`⇄ · `Invoke`⇄ · `Subscribe`⇄ · `WrapKey`/`UnwrapKey`.
**A plugin is conformant because it speaks this, never because it imports the SDK** (§1.5,
ADR-0141).

### L1 · Composition `A`

**CapabilityBinding** — grain: `(capability, intentKind?, substrate?)` within an environment scope.

| Field                  | Notes                                                                    |
| ---------------------- | ------------------------------------------------------------------------ |
| `entries[].capability` | matches an Actuator's `provides[]` — never a provider name (ADR-0106 D1) |
| `entries[].provider`   | the one place above L0 a provider name is legal                          |
| `entries[].intentKind` | scopes the binding to one Intent kind                                    |
| `entries[].substrate`  | closed set `aws`/`kubernetes`/`vsphere` (`types/substrate.go`)           |
| `environments[]`       | membership filter                                                        |

**This is the only layer that may name a provider or a substrate.** The steer _"we should NOT
connect `aws` to `web-app`, ever"_ is exactly this rule, and it is why changing one line here
migrates a declared intent.

**Environment** — grain: `name`. Carries `{name, description}` and **nothing else, deliberately**:
ADR-0142 D4 refused inheritable facts. An Environment is a **scope**, not a fact-bearer.

### L2 · Population `O` + `C`

| Kind                  | Grain                                                                    |
| --------------------- | ------------------------------------------------------------------------ |
| **Entity**            | `id`; identified by `identityKeys`. Never schema'd (§1.1)                |
| **Facet**             | **`(entityId, namespace)`** — `PRIMARY KEY (entity_id, namespace)`       |
| **Relation**          | `(type, fromId, toId)`                                                   |
| **Provenance**        | per write — `{writerKind, writerRef, sourceId, cell, at}`                |
| **facet_owner**       | `(namespace, ownerKind, ownerRef)` — ADR-0060; many owners per namespace |
| **label_owner**       | `(key, ownerRef)`                                                        |
| **entity_presence**   | `(entityId, sourceId)` — ADR-0042; absence is a union across Sources     |
| single-writer binding | `(Entity, namespace, source)`                                            |

> ⚠ **`(entity, namespace)` is the whole Facet grain — there is no instance key anywhere in L2.**
> That one line is why a host can hold one `app.config`, and therefore one managed application. §6.

**Tombstone / revival** (live-proven 2026-07-30, ADR owed): a tombstone must clear the Entity's
Facets, and a revived Entity is a **new instance**, not a resumed one. Inheriting the dead instance's
facts made the graph assert apache on a pod that never had it _and_ silenced the drift check that
would have caught it.

### L3 · Grouping `A` over `O`/`C`

**View** — grain: `name`. `version` is a monotonic revision counter, one row per name — **not**
identity-forming (unlike `Intent.version` / `Blueprint.version`, where rows coexist).

`selector: {kinds[], labels{}, facets[], relations[]}` reads **L2 only**. A View cannot select on a
desire — only on what is observed or caused. That asymmetry is how the estate notices a host
destroyed out-of-band.

**The View is the authz unit.** `runner`-on-View is the §2.5/ADR-0028 gate, checked **per Step**, at
launch, before any Step runs.

### L4 · Desire `A`

**Intent** — grain: `(name, version)`, both identity-forming; rows coexist, so test/stage/prod can
run three configurations at once.

| Field      | Notes                                                                                                                                                     |
| ---------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `kind`     | e.g. `Intent/Application`; drives schema + forms                                                                                                          |
| `spec{}`   | a **declaring** layer in the spec merge. Read by templates via explicit field lookup only (`{{.spec.package}}`) — never an expression language (non-goal) |
| `onRemove` | `retain` (v1) / `revert` / `remove`. Retained-but-withdrawn **always** raises an orphan Finding — never silent                                            |
| —          | carries no targeting; an Assignment binds it to a View                                                                                                    |
| —          | provisioning Intents carry no version (ADR-0119 D3); a pinned version is immutable                                                                        |

**Blueprint** — grain: `(name, version)`. `for`, `delivers`, `defaults{}`, `routes[]`.

| Route field                                     | Grain                     | Notes                                         |
| ----------------------------------------------- | ------------------------- | --------------------------------------------- |
| `match[]`                                       | facet predicates          | which Entities this route serves              |
| `observe`                                       | `(namespace, path)`       | `equals` / `contains` / `notBefore`           |
| **`claim`**                                     | **`(Entity, namespace)`** | exclusive or additive — the grain problem, §6 |
| `remediationWorkflow` / `remediationCapability` |                           | the capability form is the composable one     |
| `remediationParams{}`                           |                           | substituted at launch (R)                     |

**Assignment** — grain: `name`. Binds `intent@version × blueprint@version → view`, scoped by
`environments[]`, contributing `values{}` to the spec merge.

**The spec merge is three layers, not two** (`core/internal/compiler/compiler.go:228`):

| Layer                          | Role                                   |
| ------------------------------ | -------------------------------------- |
| `blueprint:<name>/defaults`    | **yielding** — the sole yielding layer |
| `intent:<name>` (`spec`)       | declaring                              |
| `assignment:<name>` (`values`) | declaring                              |

Two _declaring_ layers setting one path fails the compile, and ADR-0118 D1 states this "holds
unchanged at N layers." The rule is **exactly one yielding layer**, not "at most two layers."

### L5 · Compiled `D | A`

**Baseline** — the compiler's output, _and_ a first-class hand-authorable kind (charter §6's power
gradient names "raw Baseline" as a rung; a hand-written one simply has no `compiledFrom`). Carries
the execution shape — `viewName`, actuator/capability, `params`, `slices`, `credentialRefs`,
`facetWriteScope`, `cron`, severity/damping, remediation — plus:

`compiledFrom: {assignment, intent, intentVersion, blueprint, blueprintVersion, route, specLayers}`

`specLayers` records **which layer supplied each value** (§1.8 descent). It is the shipped,
working instance of the idea this document generalises — and it exists for compiled Baselines and
for nothing else.

### L6 · Execution `A` + `R`; a Run is `C`

**Workflow** — grain: `name`. `steps[]`, `inputs` (Git-declared Contract), optional `adoptedFrom`.

> ADR-0123 D3: a Workflow input that nothing binds, **or that the reconcile never supplies**, is
> refused at load.

**Step** — grain: `name` within the Workflow. The dispatch mode is _inferred from which fields are
set_:

| Mode      | Set                                           | Notes                                                                                       |
| --------- | --------------------------------------------- | ------------------------------------------------------------------------------------------- |
| Gate      | `gate`                                        | approval (§5 Flow 1)                                                                        |
| Policy    | `policy`                                      | admission controls on a mutating Step                                                       |
| Nested    | `workflow` / `workflowCapability` + `forKind` | ADR-0139                                                                                    |
| Actuation | `actuator` / `actionCapability` + `action`    | ⚠ a Step with **only** `actionCapability` falls here with an empty Actuator — latent defect |

Plus `needs[]`, `when`, `viewName` (**yields** to the Assignment's when absent), `params`,
`capabilityArgs`, `slices`, `credentialRefs`, `facetWriteScope`, `plan`/`planFrom`.

**Substitution (R)** — one substituter, field-reference only, namespaces `spec` · `event` · `param`
· `entity` (ADR-0024 as amended). A residual `{{` after substitution is an **error**, never a
passthrough.

**Trigger** — grain: `name`. `kind` (cron/event), `cron` / `emitter`+`when`, cooldown, the execution
shape, `environments[]`.

### L7 · Access `A`

| Kind                  | Grain                      | Notes                                                                                                                |
| --------------------- | -------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| **CredentialRef**     | `name`                     | **a pointer, never material** (§2.5). Resolved at pod spawn only; never logged, never in the graph                   |
| **authz tuple**       | `(user, relation, object)` | `runner`-on-View is the launch gate; `user`-on-CredentialRef is an Action's **use-check**, which _is_ its authz gate |
| **admission control** | `name`                     | judges the kinds in `admissionDirs` — **not all of them**, see §7                                                    |

> A gate-only CredentialRef (binding no material) is the sanctioned way to gate a privileged verb —
> `cert-issuer`, `kubecompute-build`. Deleting `credentialRefs` to "simplify" an Action deletes its
> gate; the core correctly refuses credential-free Actions.

## 4. How a value actually reaches a Run

```
Blueprint.defaults ──yielding──┐
Intent.spec ───────declaring───┼─→ compiler ─→ Baseline.params ─→ substituter ─→ Step.params
Assignment.values ─declaring───┘   (two declaring layers on one path = compile error)   │
                                                                                        ↓
Entity / Facet (L2 — observed or caused) ──→ {{.entity.*}} ─────────────────→ Action input
                                                                                        │
CapabilityBinding ─→ capability → provider (+substrate) ─→ which Actuator receives it    │
                                                                        validated against
                                                                     the pinned Contract
```

Four checkable invariants:

1. **Nothing above L1 names a substrate.** (The provider half is weaker — see rule 3.)
2. **Nothing _in_ L2 is authored.** (`estate/hosts/` is a Source's SoR, projected.)
3. **Every value crossing into an Action is validated against a pinned Contract.** Drift is blocking.
4. **A Step's target View is the Assignment's or explicitly named — never both.** (A converge
   Workflow naming its own View, duplicating the Assignment's, was a real defect.)

## 5. The grain table — "why can't I…?"

| Thing                   | Keyed by                                | Consequence                                    |
| ----------------------- | --------------------------------------- | ---------------------------------------------- |
| Entity                  | `id` / `identityKeys`                   |                                                |
| **Facet**               | **`(entity, namespace)`**               | one value per namespace per Entity — §6        |
| Facet ownership         | `(namespace, ownerKind, ownerRef)`      | many owners per namespace (ADR-0060)           |
| Single-writer binding   | `(Entity, namespace, source)`           |                                                |
| Presence                | `(Entity, Source)`                      | absence is a union (ADR-0042)                  |
| Intent                  | `(name, version)`                       | versions coexist                               |
| Blueprint               | `(name, version)`                       | versions coexist                               |
| View                    | `name`                                  | `version` is a revision counter                |
| Assignment              | `name`                                  |                                                |
| **Claim**               | **`(Entity, namespace)`**               | two Assignments converging one → compile error |
| CapabilityBinding entry | `(capability, intentKind?, substrate?)` |                                                |

## 6. The open grain problem: `app.config`

**This section states a question, not a decision.** ADR-0148 D6 reserved the answer for its own ADR
and that is still where it belongs.

The map shows it is structural, not a product choice:

- A Facet is keyed `(entity, namespace)`. There is **no instance key anywhere in L2**.
- `Blueprint.routes[].claim` is exclusive at that same grain.
- The conflict is computed **at compile, from declarations only** — `compiler.go:879`,
  `key struct{ ns, entity string }`, reading no observed value.
- Therefore one host may run one Stratt-managed application.

ADR-0148 D6 recorded this honestly: _"a limit an adopter will hit (a host running apache on 80 and
tomcat on 8080 is **ordinary**)"_, and _"distinguishing two applications on one host needs a
per-application key in the claim — which is a claim-model change and belongs in its own ADR."_

**What the real world does that this cannot model:** a host runs many applications; one application
may be installed many times on one host. Listening endpoints are the part that is genuinely 1:1.

**Three things the map says about the fix, before anyone drafts it:**

1. **The instance key belongs on the application, not the endpoint.** D6's own wording —
   _a per-application key_ — is right. Apache legitimately holds `:80` and `:443` under one config;
   keying `app.config` by port would either duplicate the config or require electing a "primary
   port", which is assembling a fact by convention (§1.4). Endpoint exclusivity, if wanted, is a
   **second claim over a second namespace with its own grain** — one key cannot carry both.
2. **The claim key must be Derived, not Observed.** A claim key sourced from L2 is undetectable
   until both Runs have executed — apache and tomcat both compile green, both dispatch, and the
   winner is whoever bound the socket first. That is execution-order precedence, which breaks §2.4
   _without a field anyone can review_, and it moves diagnosis later, inverting the §1.8 motivation.
   The satisfiable shape is **two** values: the claim key derived at compile from the resolved spec
   (D — legitimate), and the actual listener as an L2 fact used **only for drift**. They agree when
   the Run succeeded and diverge when it didn't — which is exactly what ADR-0148 D4's fact-back
   exists to catch.
3. **An endpoint may be a Facet instance key. It may never become `Entity/Port`.** That is §9
   ontology creep and the start of "Stratt models sockets." The genuinely 1:1 tuple is
   `(Entity, bind-address, protocol, port)` — `0.0.0.0:80` and `127.0.0.1:80` coexist — which is a
   Facet's business, not an Entity's.

Also unresolved before drafting: **`resource` is a banned term in core-model identifiers (§2).** The
natural word for "the thing that is scarce" is the banned one. Pick the replacement _before_ the
ADR, and run `vocabulary-linter` on it before it reaches an identifier or an error string.

**Diagnosis motivation (§1.8):** today the compile error says _two Blueprints claim `app.config`_.
The true statement is _apache and tomcat both want `:8080` on web-02_.

**Estate consequence, if the fix lands:** `svc-fleet` / `view:svc-servers` exist only to work around
this limit, and the realistic topology is apache reverse-proxying tomcat on one host. Whether to
delete them is the ADR's call, not this document's.

## 7. Register of open seams

Rows carry their status. **CLOSED** rows stay in the table rather than being deleted: the
register is also the record of what this map's own reading found, and a seam that was real
last week is the best evidence that the next one is worth checking too.

| #   | Seam                                                                                                                                                                                                                                                                                                                                                                                                                                           | Layer        | Status                                    |
| --- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------ | ----------------------------------------- |
| 1   | Facet/claim grain has no instance key (§6)                                                                                                                                                                                                                                                                                                                                                                                                     | L2 / L4      | open — ADR owed                           |
| 2   | **Admission judged neither the substrate-selecting layer nor the L0 grant surface nor anything a plugin ships.** Three gaps, one theme: `admissionDirs` listed 15 of 22 estate directories; `admitEstate` walked only the PRIMARY root, so every plugin-supplied declaration was judged by nothing; and `AdmitDeclarations` (the API door) omitted kinds the Git door already judged, so a control could hold on a reconcile and not on a POST | L7 / L1 / L0 | **CLOSED** — see §7.1                     |
| 3   | **A Step declaring no shape at all loaded clean, then dispatched as an actuation with an empty Actuator** — `ValidateWorkflow` classifies positively, `types.Step.IsActuation()` classifies residually (fail-closed), and the two disagreed on exactly that input                                                                                                                                                                              | L6           | **CLOSED** — refused at load              |
| 4   | **An Intent carries no `environments`, so every provisioning Intent is in every environment** — and the load check demands each one satisfy the builder of EVERY substrate. Sharpened from "parse and run-time disagree": they do not disagree by accident, the check deliberately validates all candidates. See §7.2                                                                                                                          | L0 / L1 / L4 | open — needs a decision                   |
| 5   | ~~`Actuator.provisions` targets are not resolved at load~~ — **the map was wrong.** `checkProvisioningBuildInputs` ships and is wired at `desiredstate.go:462`, covering `provisions`, `decommissions` AND `remediates`                                                                                                                                                                                                                        | L0           | **was never open** — corrected 2026-07-30 |
| 6   | The estate cannot express ordering across Assignments ("serve TLS once a certificate is present")                                                                                                                                                                                                                                                                                                                                              | L4           | open                                      |
| 7   | Tombstone/revival semantics unrecorded (ADR owed)                                                                                                                                                                                                                                                                                                                                                                                              | L2           | open — ADR owed                           |
| 8   | ~~The ADR-0151 lint enforcing "nothing above a provider names a substrate" is follow-up 4, unimplemented~~ — shipped as `checkNothingAboveAProviderNamesASubstrate`. Keyed on the FIELD NAME, not the value: a `substrate` key anywhere in an Intent spec / Blueprint defaults+remediationParams / Assignment values / View selector, and a `provider` key whose value names a declared provider or a substrate token                          | L1           | **CLOSED** — see §7.3                     |
| 9   | ~~`substrate:` token collides with demo manifests~~ — the demo manifests' field is renamed `backend:`. It names the simulator or transport a demo talks to (`ec2-floci`, `vsphere-vspheresim`, `ssh`), which is not ADR-0151's closed set at all — and `kubernetes` was a legal value of both                                                                                                                                                  | L1           | **CLOSED**                                |
| 10  | "Report ignored params" is not a port obligation — a plugin may silently drop input                                                                                                                                                                                                                                                                                                                                                            | L0           | open                                      |
| 11  | `software.package` has a bootstrap owner, not the ADR-0080 slice-2 Syncer collector                                                                                                                                                                                                                                                                                                                                                            | L2           | open                                      |
| 12  | ~~`svc-fleet` does not reach `graph.intent` despite being in the ConfigMap~~ — never a ConfigMap gap: the WHOLE estate parse failed, on item 4's check. The estate was reverted (it existed only to work around ADR-0148 D6)                                                                                                                                                                                                                   | L4           | **CLOSED** — diagnosed, cause is item 4   |

### 7.1 What closing item 2 found

The register said admission "does not judge" seven directories. Reading the code to close it found
that the coverage list was the _smallest_ of three gaps stacked on one another:

1. **`admissionDirs` was incomplete** — the one it named. `capability-bindings/` is the single
   place a provider or substrate may be named (ADR-0151), and `actuators/` + `connectors/` are the
   L0 grant surface. Neither could be matched by any control.
2. **`admitEstate` took one `root`, while every kind below it is parsed across `estateRoots()`** —
   the estate plus one root per plugin admitted in `plugins.yaml`. So a plugin's Workflows,
   Actuators, Connectors and Blueprints were judged by nothing **even for the kinds already on the
   list**. That is the inverse of what admission is for: an org's own manifests were policed and
   the third-party ones, which are the reason to have a policy, were not.
3. **The two doors disagreed.** `AdmitDeclarations` (the API door, which exists to close the GOV-2
   bypass) omitted `CredentialRef` and `SCIMIdP` — both of which the Git door had always judged. A
   control over a credential pointer therefore held on a reconcile and silently did not hold on
   `POST /desired-state/apply`. The same function also emitted different `kind` tokens: the Git
   door's fallback was `default: return sub`, so a Cell was `"cells"` on one path and `"Cell"` on
   the other, and `object.kind == 'Cell'` fired on exactly one of them.

All three are closed, and the coverage is now **asserted rather than maintained**: a new estate
directory that admission does not walk fails `TestAdmissionJudgesEveryDeclarationDirectory`, and
the two doors are compared behaviourally by running the real estate through both and diffing the
kinds each presented to the PDP (`TestBothAdmissionDoorsAgreeOnKind`).

**The lesson generalizes and belongs in this map:** a control that never fires is
indistinguishable from a control that always passes. Every "is X judged?" question in this
document should be read as "judged _where_, at _which door_, over _which roots_" — three answers,
and the reference estate had a different one for each.

### 7.2 Item 4, restated: a provisioning Intent has no environment

The earlier wording — "parse-time and run-time builder resolution disagree" — described the symptom
and misnamed the cause. They do not disagree by accident.
`checkProvisioningBuildInputs` validates an Intent against **every** Workflow any provider
advertises for its kind, and says so in its own comment: _"IT CHECKS EVERY CANDIDATE, NOT THE
WINNER, and that is deliberately stronger than the reconcile"_ — because which provider wins
depends on runtime state (which providers are VERIFIED) that Git cannot see.

That reasoning is sound. What makes it bite is a different fact: **`types.Intent` has no
`environments` field.** `ScopeToEnvironment` filters Assignments, Triggers and Baselines; Intents
are not filtered because a provisioning Intent has no Assignment to carry a scope (ADR-0058 makes
it a sibling reconcile selected by name). So every `Intent/Compute` is in force in every
environment, and the candidate set for it is genuinely the union of every substrate's builder.

The consequence is visible in the shipped estate: `app-tier` declares `region`, `instanceType` and
`ami` — AWS coordinates — while `dev` binds Compute to the **kubernetes** substrate, where none of
them are used. They are there to satisfy `compute-build`, a builder that will never run for it in
that environment. And the cost compounds: **admitting a new provisioning provider retroactively
invalidates every existing Intent of that kind**, because each must now also satisfy the newcomer's
builder.

ADR-0151 D4 already booked the limit — _"the line moves the BUILDER, not provider-shaped `params`
an Intent may be carrying"_ — this is the price of it, measured.

**Three candidate shapes, none chosen here:**

1. **Scope the Intent.** Give a provisioning Intent an `environments` filter and check only the
   builders reachable in the environments it is in. Consistent with ADR-0118 D1's prescribed shape
   (one flat declaration per environment) and with `environments` staying a membership filter. It
   does mean two near-identical `Intent/Compute` documents for a fleet that spans two substrates —
   which ADR-0118 D1 would call correct, not duplication.
2. **Type the params.** Make the builder's declared `inputs` the contract and let the Intent's
   `params` stay opaque, checking only the resolvable builder's needs at reconcile. Weaker: it
   moves the diagnosis from load to launch, which is the direction §1.8 forbids.
3. **Accept the union and say so.** Keep today's behaviour and make the error message name it:
   _"`app-tier` must satisfy every registered Compute builder, because an Intent has no
   environment."_ Cheapest, honest, closes nothing.

This is a decision, not a defect, and it is the one blocking a genuinely multi-substrate estate.

### 7.3 Item 8: what the substrate lint does and does not refuse

ADR-0151's selling property is that **one line migrates a topology** — flip `substrate: kubernetes`
to `aws` in a capability-binding and every provisioning Intent resolves to the other landscape's
builders, together, with nothing above the line moving. That is only true if nothing above the line
is _separately_ coupled to a landscape, and a `spec` / `defaults` / `values` map is opaque to core
(§1.5), so nothing typed could notice one that was.

The lint keys on the **field name**, not the value, because that is the unambiguous part:

- A key named **`substrate`** is refused outright. Exactly two declarations may carry it — a
  provider's own and a capability-binding entry — and neither is above a provider.
- A key named **`provider`** is refused when its value names a declared provider or a substrate
  token. That is ADR-0106 D1 ("a route names a capability, never a provider") made checkable.

**What it deliberately does not refuse**, because a lint that fires on prose is a spell-checker with
a veto: a substrate token as a bare value (`ami: ami-0aws-baseline`), or in a declaration's name
(`aws-billing-fleet`). Neither routes anything. The rule is about which _field decides where a thing
gets built_.

**A View selector IS linted, and that one is not obvious.** A selector reads observed labels, and
selecting on an observed fact is ordinarily legitimate (§1.2). But `labels: {substrate: kubernetes}`
makes the View's _membership_ substrate-specific — so the binding line no longer migrates it, and
the Assignment goes on pointing at a set defined by the landscape it used to be on. The coupling
arrives through the one door that looks like an observation.

## 8. Invariants, and where each is actually enforced

Claims only. Where the "Enforced at" column says **not enforced**, that is a seam, not a rule.

| #   | Invariant                                                                                                                                       | Enforced at                                                                                                                                          |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | At most one **declaring** claimant per path; a yielding layer fills only the definitionally unset; two declaring claimants refuse, naming both  | `core/internal/overlay/overlay.go`; `compiler.go:228`                                                                                                |
| 2   | No computed **facts about the world** — a reach coordinate is observed or caused (L2 only)                                                      | ADR-0142 D4 (design rule; no single enforcement site)                                                                                                |
| 3a  | Nothing above a provider names a **substrate**                                                                                                  | `checkNothingAboveAProviderNamesASubstrate` (ADR-0151 follow-up 4), refused at load — see §7.3 for what it does and does not catch                   |
| 3b  | Nothing above L1 names a **provider**                                                                                                           | **False as stated** — `Step.actuator` (`types/workflow.go:105`) and `Baseline.actuator` bind by name. The capability form is preferred, not required |
| 4   | Nothing **in** L2 is authored                                                                                                                   | `migrations/00001_graph_spine.sql` — `CHECK (prov_writer_kind IN ('syncer','run'))` + `facet_write_path` trigger                                     |
| 5   | `environments[]` is a membership filter everywhere — never a value selector, never precedence                                                   | ADR-0118 D1 guardrail (a); ADR-0057 D4; `types/environment.go`                                                                                       |
| 6   | Exactly **one yielding layer** in the spec merge, and it is `Blueprint.defaults`                                                                | `compiler.go:228`                                                                                                                                    |
| 7   | A claim is exclusive-fails-compile or additive-union — never a priority field                                                                   | §2.4 verbatim; `compiler.go:879`                                                                                                                     |
| 8   | A capability route names a capability, never a provider                                                                                         | ADR-0106 D1                                                                                                                                          |
| 9   | Credentials are pointers; the use-check is the gate                                                                                             | §2.5; the core refuses credential-free Actions                                                                                                       |
| 10  | An authored L0 grant is the ceiling; a Manifest may narrow, never widen; declared-but-unadvertised refuses                                      | `core/internal/connectorregistry/registry.go`                                                                                                        |
| 11  | _Goal, not invariant:_ a failure is legible at the layer that caused it — a grain conflict should name what is in contention, not the namespace | §1.8 — **aspiration**; §6 is a live counter-example                                                                                                  |
