# ADR 0118 — The parameter plane: values reach the things that execute

- **Status:** **Proposed** (2026-07-25, steward) — vocabulary-linter **CHANGES REQUIRED → resolved**;
  charter-guardian **CHANGES REQUIRED → resolved** (two violations against the first draft's own reasoning,
  both accepted after verification; one decision split out to ADR-0119). Findings and resolutions are
  recorded inline below and summarized under **Review record**.
- **Date:** 2026-07-25
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams, not the world), §1.2 (one authority per fact), §1.4 (boring
  spine — content-blind), §1.6 (one capability, every surface), §1.8 (never hide diagnosis), §2 (vocabulary
  frozen at v1.0), §2.4 / §4.1 (**no implicit precedence anywhere — the anti-GPO axiom**), §3 (Contracts are
  data, validated by a standard JSON Schema validator, never language classes)

## Context

Stratt's estate is **config-into-hardcoded-workflow**, not config-as-code. The evidence is a single value in
one demo: `port: "443"` is declared **three times** —

| Where                                                        | What it says                                 |
| ------------------------------------------------------------ | -------------------------------------------- |
| `demos/app-cert/estate/intents/tls-app.yaml`                 | `spec: { package: nginx, port: "443" }`      |
| `demos/app-cert/estate/blueprints/tls-app.yaml`              | `defaults: { channel: stable, port: "443" }` |
| `demos/app-cert/estate/workflows/app-install-with-cert.yaml` | `extraVars: { app_tls_port: "443" }`         |

The third is the defect, and it exists because of one line:

```go
// types/blueprint.go:68
RemediationWorkflow string `json:"remediationWorkflow,omitempty"`
```

**A Blueprint route names the Workflow that does the work and passes it nothing.** The compiler substitutes
`{{.spec.X}}` into the route's observe expectation, copies the remediation Workflow's bare _name_ onto the
compiled Baseline (`core/internal/compiler/compiler.go:415`), verifies it exists — and stops. So the
parameter plane **terminates at the observation boundary**: Stratt can state what it expects using the
Intent's parameters, but the thing that _converges_ the estate receives none of them. Every remediation
Workflow must re-declare by hand what its Intent already says. That is not demo sloppiness; it is the only
thing the seam permits.

Four gaps compound it.

**(1) The launch boundary is untyped.** `types.Workflow` is `{Name, Steps, AdoptedFrom}` — it declares no
inputs. `LaunchParams` is a free-form `map[string]any` decoded straight off the request body with **zero**
validation (`core/internal/api/server.go:1965`). `{{.launch.x}}` works, but nothing declares what `x` is: an
author cannot discover a Workflow's parameters, a reviewer cannot check them, and an agent cannot introspect
them. This is open ADR-0117 follow-up **(c)**, and it is why `docs/aap-2.7-parity.md` scoring "Surveys 🟢"
overstates the truth — the input Contract validates an **Actuator's** params, not a Workflow's inputs.

**(2) There is nowhere to declare a value per environment.** `environments:` is, correctly, _"a selector,
never a precedence field"_ (ADR-0057 D2) — it selects **whether** a declaration applies, not **what values**
it carries, so varying a parameter between test and prod means duplicating the entire Intent. Note precisely
what this ADR does and does not fix: `Assignment.Environments` is a **set**, so one Assignment spanning
`[staging, prod]` still carries one value set. D1 trades "duplicate the Intent" for "duplicate the
Assignment" — a real improvement, and less than the first draft claimed.

**(3) Configuration is not versioned.** Blueprints are: `(name, version)` is the storage identity _"so an
upgrade rolls through rings alongside the old version"_, and `Assignment.BlueprintVersion` pins it. Intents
have no version, so a configuration promoted to prod is not immutable. **Deferred to ADR-0119** — see
_Deferred_ below.

**(4) Two shipped reference Workflows are broken.** `estate/workflows/access-apply.yaml` and
`access-revoke.yaml` reference `access_subject`, `access_scope` and `access_kind` — **defined nowhere in the
repository**. They are _Ansible_ `{{ var }}`, not Stratt `{{.ns.field}}`, so `template.Substitute` never sees
them; they pass to a play that fails at run time on an undefined variable. The two syntaxes are visually
identical in YAML and exactly one is checked. The values themselves already exist one hop away:
`estate/intents/alice-wheel.yaml` declares `spec: {subject: alice, kind: group, scope: wheel}`.

### What the first draft got wrong, and what that changed

This ADR was reviewed before finalization. Two findings landed against its own reasoning, and both were
verified in the code before being accepted:

**The overlay engine never implemented the half of ADR-0083 D5 that matters here.** D5 (line 78) mandates
that the defaults/override path resolve _"by §2.4 claim-type merge (exclusive double-claim **fails the
compile**; additive claims **union** with per-element provenance)."_ `overlay.go` shipped `unionAppend` for
lists and, for scalars, _"last explicit layer wins"_ with a silent overwrite
(`core/internal/overlay/overlay.go:101-108`). So the first draft's claim — that adding a third layer merely
_extends a settled decision_ — was false. It would have **deepened an unimplemented one**.

That matters because of _why_ two layers were safe: a **default is definitionally a yielding value**, so
`Blueprint.defaults` under `Intent.spec` is not precedence at all — it is "unset takes the default." Adding
`Assignment.values` introduces a **second non-default declaration**, and `intent: port: 443` +
`assignment values: port: 8443` is two operators declaring the same fact with core silently picking one by a
rung order living in Go. ADR-0083 D5 forbids exactly that, in the same breath: defaults live in the
Blueprint, _"**never in core Go and never as a core-side precedence table**."_ "Explicit, authored,
structural" is not a sufficient defence, and GPO is the proof — LSDOU is also explicit, documented and
structural, and the charter names GPO the enemy anyway.

**The launch boundary is already spoken for by the charter.** The first draft argued §1.1 justified a bespoke
shallow Go shape rather than JSON Schema. That reading was wrong on three counts, each verified: charter
line 34's migration table maps _"**survey → input Contract** with UI hints"_ — the charter already calls this
surface a Contract; line 114 requires that _"Contracts and Facet schemas are **data** — pinned, hash-verified
JSON Schema documents … validated by a standard JSON Schema validator, **never language classes**"_; and
line 120 makes it load-bearing — _"Intent forms, **Step inputs**, Finding tables … are generated from JSON
Schema (Contracts). Plugins extend the UI by shipping **schemas, not React code**."_ A bespoke struct cannot
drive the form generator or an MCP tool schema without a lossy translator in core. §1.1 forbids schemas on
whole Entities and universal ontologies; a launch door is the most seam-like surface in the system.

### What the rest of the industry does, and why the charter picks a side

|                | Declares its inputs?                                | Value sources | Conflict resolution           | Language                 |
| -------------- | --------------------------------------------------- | ------------- | ----------------------------- | ------------------------ |
| Terraform      | ✅ `variable` (type, default, `validation`)         | 6             | **6-level precedence ladder** | HCL expressions          |
| Helm           | ❌ (retrofit: `values.schema.json`)                 | 4             | **4-source deep merge**       | full Go templates        |
| Ansible        | weakly (`defaults/`, the _lowest_ rung)             | 22            | **22-level ladder**           | Jinja2                   |
| Kustomize      | n/a — refuses to parameterize                       | —             | —                             | none                     |
| Tekton         | ✅ `params` (name, type, description, default)      | **1**         | **none needed**               | `$(params.x)` lookup     |
| GitHub Actions | ✅ `workflow_call.inputs` (type, required, default) | **1**         | **none needed**               | `${{ inputs.x }}` lookup |

Charter line 69: _"There is no implicit precedence anywhere in the model. **This is the anti-GPO axiom.**"_
GPO **is** the precedence-ladder pathology, so the charter rules out the core mechanism of Terraform, Helm
and Ansible alike. Ansible's own documentation concedes the point and offers a social fix — _"You should
define each variable in one place."_ Stratt makes that structural: D1 turns it into a compile error.

Kustomize is the strongest argument _against_ parameterizing at all: templating means _"the source yaml gets
polluted with `$VARs` … it's no longer **data**, it's now logic that must be compiled,"_ and _"errors in the
output are disconnected from the edit that caused it."_ The answer is that our tokens stay **field lookups
into a declared namespace** (ADR-0024), never evaluation, so the YAML stays data. Tekton and GitHub Actions
supply the model actually adopted: declared typed params passed explicitly, no ambient inheritance, one
source per value — §2.4 satisfied _by construction_ rather than by a rule.

### One thing Stratt already gets right

At the Ansible boundary a Step's `extraVars` is written to `env/extravars` (`plugins/ansible/shim.go:500`),
which ansible-runner passes as `--extra-vars` — rung **22**, the one that beats all 21 others. Stratt has
already collapsed Ansible's precedence ladder to a single authority without meaning to. The mechanism is
sound; only the declaration is missing.

## Decision

### D0 — The parameter plane is shape-blind, and that is what makes new shapes free

No decision below names a domain concept. There is no `cert`, `gateway`, `hsm`, `routing` or `webserver`
anywhere in the plane — it moves values validated by schemas the plane never reads. This is §1.4's
content-blind spine applied to configuration, and it is the load-bearing property for extensibility: a shape
we have not thought of plugs into the _existing_ seams **without touching D1–D5**.

Adding a shape is a recipe, not a redesign:

1. **Choose the axis.** A **capability** shape (configuring/observing something that exists) gets a
   capability class + Facet schema(s) + Blueprint route(s), and the capability stays a **value** inside a
   frozen Kind — never a new Intent kind (ADR-0083 D2: there is no `Intent/WebServer`, ever). A
   **provisioning** shape (infrastructure that must be built) gets a new Intent kind +
   `contracts/intents/<kind>.schema.json` + a provider's `provisions:`/`decommissions:` mapping.
2. **Add the capability class only when its first provider ships** (ADR-0104); add the Facet schema only if a
   shipping Contract demands it (§1.1).
3. **Depend on the class, never the plugin name** — swapping a provider changes zero consumer declarations.
4. **The sufficiency gate holds** (ADR-0083 D4): a class, Facet schema or route with no shipping consumer is
   rejected at compile admission. This stops "we might need it" becoming ontology sprawl.

Worked against the shapes we can currently name: **transit encryption** is the existing `keycustodian` class;
**certificates** are the existing `certissuer` class with `Intent/Certificate` and the
`cert.identity`/`cert.expiry` Facets (already end-to-end, ADR-0030/0050/0098); an **HSM** is a new _provider_
of `keycustodian` selected by a `CapabilityBinding`; an **API gateway** is a new capability class plus a Facet
namespace for listener/route state, still under `Intent/Application`; **routing** splits — L3 network routing
is a provisioning Intent kind in the ADR-0059 family with placement as a Relation, while L7 request routing
belongs to the gateway capability. None touch this ADR's mechanism.

### D1 — Two co-equal declarations of one value is a compile error, not a precedence rung

`types.Assignment` gains `Values map[string]any`, parsed strictly (`KnownFields`), and the compiler appends a
third layer — but the merge rule changes, and that is the substance of this decision:

- **`Blueprint.defaults` is the sole _yielding_ layer.** A default is definitionally overridable; "unset
  takes the default" is not precedence.
- **`Intent.spec` and `Assignment.values` are co-equal non-default declarations.** A path set by **both** is
  an **exclusive double-claim and fails the compile**, naming both layers and the path (§1.8). There is no
  rung order between them, because there is never a contest to resolve.

This finally implements ADR-0083 D5's exclusive half, holds unchanged at N layers, and needs no
`priority`/`order`/`weight` field anywhere. It also inverts the ergonomics deliberately: to set a value per
environment, the Intent must **omit** it — which is the honest expression of "this value is an
environment-level decision" rather than a silent override of a fleet-level one.

**Relationship to ADR-0083, stated honestly.** This does not merely complete D1's promised "optional
overrides". ADR-0083 D5 specified the override _site_ as overlay **directories**; this relocates it to a field
on the Assignment, because an Assignment is already the environment-scoped instantiation
(`types/envscope.go`) and a directory convention would be a second composition mechanism competing with the
Kinds. So: **this ADR lands the missing exclusive half of ADR-0083 D5 and relocates the override site, with
reasons** — not "extends a settled decision."

**Validation timing must move, and it is a prerequisite rather than a follow-up.** `ValidateIntent` validates
a spec as **complete** at declaration (`core/internal/desiredstate/desiredstate.go:1515`) while
`Blueprint.defaults` validate as **partial** (`:1730`). Under the omit-to-override rule an Intent that leaves
a required field to its Assignment would fail declaration. So: **partial** validation per layer at
declaration, **complete** validation of the **merged** spec at compile (`ValidateIntentSpec(resolvedSpec)`,
which the compiler does not do today). That mirrors ADR-0024 D4's precedent — validate resolved data, not
placeholders.

**Lists can only grow.** `unionAppend` means no layer can narrow a list an earlier layer set. §2.4's additive
semantics are correct and unchanged, but the consequence must be stated: **list-valued per-environment
variation requires the Intent not to set the list** — the same omit-to-override rule, arrived at from the
other side.

**Two guardrails written down because they will be proposed.** (a) `values: {prod: {…}, staging: {…}}` —
environment-keyed value maps — are **forbidden**: ADR-0057 D4 and `types/envscope.go` already bind this
(`EnvironmentValues()` is forbidden; env-conditional config values would be the new-config-language non-goal).
(b) **Never coerce across types.** The demo declares `port: "443"` as a string and the play compensates with
`{{ app_tls_port | int }}`; if substitution into a number-typed input silently coerced, that would be
evaluation semantics entering by the back door. Cross-type mismatch **fails at compile**, consistent with
`overlay.go`'s existing cross-type failure and `template.go`'s type-preserving single-token rule — and the
demo's spec is corrected to a real number.

**The layer lineage stops being thrown away.** `overlay.Merge` already returns a map from each dotted path to
the layers that set it; the compiler discards it into `_`. It is now persisted as **`specLayers` on the
existing `CompiledOrigin`** (`types/baseline.go:86`, the §1.8 descent stamp that already carries
Assignment/Intent/Blueprint/route), so _"why is this 443, and which layer decided"_ is answerable without
re-deriving the merge. Naming: both reviews rejected the drafted `specProvenance` because **Provenance** is a
frozen Named Kind (§2.1) meaning the graph-plane write stamp — which Run or Syncer wrote an attribute, when,
from which Source. Layer lineage has no Run and no Source; shipping it as "provenance" would teach two
meanings for a frozen word. `specLayers` under `compiledFrom` avoids the term and puts the fact where descent
already lives.

**No migration.** `graph.assignment` stores the whole declaration as a JSON `spec` column
(`core/internal/graph/intentstore.go:90`), so `values` rides the existing blob.

### D2 — A Workflow's inputs are a JSON Schema Contract, validated by the standard validator

`types.Workflow` gains `Inputs`, a **JSON Schema object document** — `properties`, `required`,
`additionalProperties: false`, `default` where wanted — declared inline in the Workflow or beside the existing
`contracts/` families, and validated by the validator already vendored and already used for exactly this job
on partial value layers (`santhosh-tekuri/jsonschema/v6`; `core/internal/contract/contract.go:421-447`).

The first draft proposed a bespoke Go struct with a five-value type enum and hand-rolled
required/default/unknown-key checking. That is "language classes with a hand-rolled validator" beside the real
one — charter line 114 forbids it, line 34 already names this surface an **input Contract**, and line 120
requires Step inputs to be **generated from JSON Schema** so the form generator and MCP tool schemas work
without a translator in core. The closed-world instinct survives intact as `additionalProperties: false`; only
the dialect changes, and it is _less_ code.

**It is a Git-declared Contract, not a plugin schema.** §1.5's registration and hash-pinning apply to plugin
Contracts; a Workflow's inputs are estate-authored desired state reviewed in Git. The struct doc comment and
the OpenAPI description must say so, because a seam an operator _believes_ is hash-verified when it is not is
worse than an untyped one (§1.8). Charter §2.3 already says "any Step's inputs/outputs", so extending
"Contract" here is a small, deliberate widening — recorded rather than left to happen.

Two consequences follow. Declaration-time checking gets **field-wise**: `checkTemplateNamespaces` validates
that `{{.launch.x}}` names a legal _namespace_ but never that `x` exists, so a typo survives to dispatch; with
a schema it fails at parse. And `GET /workflows/{name}` publishes the schema, so the UI form, CLI and MCP are
generated from one document (§1.6, charter line 120).

This closes ADR-0117 follow-up **(c)** and makes the parity doc's "Surveys" claim honest.

### D3 — A route passes `remediationParams`, and remediation gets a real door

`types.BlueprintRoute` gains `RemediationParams map[string]any`, substituted from the resolved spec at compile
using the existing substituter (no second engine), and written to the compiled Baseline's field of **the same
name** — one concept, one name, following the precedent already set two lines away
(`types/blueprint.go:68`: _"Same field name as Baseline.RemediationWorkflow (one frozen concept, §2)"_). The
drafted `with` is dropped: it is a GitHub Actions DSL keyword, semantically empty in isolation, and a synonym
for the already-frozen `params` used on `Step`, `Baseline` and `Trigger`.

**Compile-time cross-check.** Every key must validate against the named `remediationWorkflow`'s input schema,
and every `required` input must be supplied. A route wired to a Workflow it does not fit **fails the compile**
— the failure lands on the person editing the declaration, not on the operator at 3am.
**`Blueprint.removeWorkflow` now gets the same treatment, via `removeParams` — and the reasoning that
deferred it was looking in the wrong place.** The original text said giving the withdrawal path a typed param
channel "needs the withdrawn Intent's **effective** spec — defaults plus Assignment values — which the orphan
branch does not have". True of the orphan branch, and irrelevant: the **compile** has the effective spec. The
withdrawal path never needed to resolve anything; it needed to read what the compile already knew.

So `Blueprint.removeParams` is substituted from the resolved spec at compile, cross-checked against
`removeWorkflow`'s declared input schema exactly as a route's `remediationParams` are, and **stamped onto every
Baseline the Assignment compiles**. The orphan branch reads them off the Baseline row.

The storage is the load-bearing part, and it is a genuine asymmetry with the remediation path rather than an
implementation convenience. `removeWorkflow` is read **live** from the still-declared, version-pinned Blueprint
at withdrawal; `removeParams` **cannot** be, because the resolved spec includes the Assignment's own values and
withdrawal is precisely the moment the Assignment stops existing in Git. The compiled Baseline is the only
surviving record of what the retired configuration said. Storing the ref as well would give one fact two
authorities (§1.2); storing only what cannot be recovered is the line.

Blueprint-level rather than per-route, matching `removeWorkflow`: a withdrawal retires the Assignment's whole
compiled set, not one route's expectation.

**And the withdrawal is launchable, not merely readable.** `Apply` writes the orphan Finding and then **prunes**
the Baseline, so the params have to leave the Baseline with the Finding: `Finding.removeWorkflow` /
`removeParams` (migration 00042) carry them in typed columns, and `resolveFindingLaunch` consults them **before**
attempting a Baseline read that by construction cannot succeed. `GET`/`POST /findings/{id}/remediation` serve
orphans through the same door as drift, with `withdrawal: true` marking which act it is. See the follow-up list
for why the values are not scraped back out of the Finding's `diff`, which carries the same information for
display, and for the `onRemove: retain` case that now explains itself instead of reporting a missing row.

**Remediation needs a named launch path, and today there isn't one.** Remediation is a ref only
(`core/internal/api/server.go:746`); nothing server-side reads `Baseline.RemediationWorkflow` and launches it.
With D2's `required` in force, an operator hand-calling `POST /workflows/{name}/runs` would now **fail** where
they previously ran with wrong values — a regression this ADR must not ship. So D3 includes an explicit
**remediate-from-Finding** path that exists identically on API, CLI, UI and MCP (§1.6) and **renders the
params it will pass before launching** (§1.8).

**One binding site per input per launch.** Three sources could supply an input: the compiled
`remediationParams`, the schema's `default`, and an operator's request body. Default-for-unset is definitional.
Compiled-vs-operator is a **new co-equal collision**, and resolving it by "operator wins" would be D1's
violation again at a worse boundary — so a launch that supplies a key the compiled params already set is
**rejected**, not merged.

**Params are not copied onto the Finding.** A Finding already references its Baseline; copying would be a
second, staleable copy of a Git-derived fact for no gain, since nothing in the plane is per-Entity. They are
read from the Baseline at launch, and the Baseline stays compiler-written only
(`core/internal/graph/baselinestore.go` — no API write path onto a `compiledFrom` row).

### D4 — Input resolution lives in one chokepoint below every transport

Four paths launch a Workflow and each reaches Temporal directly: `POST /workflows/{name}/runs`
(`core/internal/api/server.go:1960`), MCP `start_workflow_run` (`core/internal/mcpserver/mcpserver.go:492`),
the event Trigger (`core/internal/triggerengine/engine.go:141`) and the schedule Trigger
(`core/internal/triggers/reconcile.go:161`). Validating inputs in the HTTP handler would leave the other three
bypassing it.

So resolution-and-validation is **one function below all transports**, in the shape
`contract.ResolveActuatorParamsFor` already takes for the Actuator seam: one capability, one validation model,
one audit stream (§1.6). Concretely it is called in **`RunDAG`, immediately after `LoadWorkflow`** — the line
every transport reaches — as an _activity_ rather than inline, because pinning replay determinism to a
validator library's behaviour across upgrades would be a latent trap. The API door calls the **same function**
eagerly so a human gets a 400 naming the offending input instead of a created-then-failed Run: one
implementation, two call sites, one for the error and one that nothing can skip. And **MCP can now supply
inputs** — `start_workflow_run` previously POSTed a `nil` body, so the moment inputs existed an agent could
_see_ declared inputs and never provide them, a §1.6 violation on the door §1.6 exists to protect.

#### D4a — Launch inputs and change context are separate fields (found while implementing D4)

Implementing the chokepoint surfaced a collision that invalidates D2's "rejects unknown keys" as originally
written: **`DAGInput.LaunchParams` already had a second consumer.**
`orchestrate.assembleChangeContext` reads it to build the policy `ChangeContext` (ADR-0063) — `environment`
sets the policy environment, `changeClass` drives break-glass (ADR-0070), `committers` feeds SoD (ADR-0068),
and **every string param becomes a policy label**. One bag carried two concepts: a Workflow's own parameters,
and facts about the change being made. Closing the world over that bag would have forced every policy-gated
Workflow to declare `environment` as one of _its_ inputs, which it is not — a Workflow does not declare facts
about the change applied to it.

So they are split: `LaunchParams` is the Workflow's declared, closed, defaulted interface; a new
`DAGInput.Context` carries what the launcher asserts about the change, and the request body becomes
`{inputs, context}` (a `WorkflowLaunch` schema — the endpoint previously declared **no** `requestBody` at all,
so the flat body was never a documented contract). `{{.launch.x}}` binds only the former. Nothing in-repo
produced a flat body: every demo and genesis script POSTs none, the UI does not launch with params, and
`awxfacade`'s `LaunchParams` is an unrelated struct of the same name.

Two properties fall out, both tested. `additionalProperties: false` finally bites, because a stray key can no
longer be mistaken for policy context. And a Workflow **input can no longer spoof the policy decision** — a
parameter named `environment` does not reach `ChangeContext`, which it did before the split.

#### D4b — The change context is still untyped, and that is a live gap

Recorded rather than fixed. `environment` remains a bare string, so `environment: "prd"` silently produces a
different policy outcome than `"prod"`: a prod freeze window (ADR-0067) simply does not match and the change
proceeds. Same exposure for `changeClass` (break-glass) and `committers` (SoD). This predates the parameter
plane — the split neither introduces nor worsens it — but the split is what makes the fix expressible: a
core-owned `ChangeContext` schema with enums, validated at the same chokepoint. Booked deliberately, because
it is a security-relevant seam that deserves its own decision rather than riding in on a plumbing change.

### D5 — A Trigger declares `inputs` for a Workflow target — a new field, not a resurrected one

**Corrected during implementation.** The review's F8 read the two Trigger paths as _dropping_ `t.Params` for
Workflow targets: `triggerengine` resolves them and passes only `Event` (`engine.go:141`), and
`triggers/reconcile.go` marshals them then uses them only on the `RunAgainstView` branch (`:161`). Both
readings of the code are accurate. The conclusion was not.

`ValidateTrigger` **already refuses** `params` on a Workflow target — _"workflowName launches carry no Step
fields (the Workflow declares its own)"_ (`desiredstate.go:852`) — and it is right to: `params` are the
Actuator's Step params, which a Workflow's own Steps declare. So the drop sites were unreachable, and the real
situation was larger than a dropped field: **a Trigger could not parameterize a Workflow at all.** The only
Workflows a Trigger could launch were ones that needed no inputs.

That was harmless while launches accepted anything, and becomes **fatal** under D2: a scheduled Trigger firing
a Workflow with a `required` input now fails at every fire. Which is why it belongs in this change rather than
a later one.

So a Trigger gains **`inputs`**, distinct from `params`. Reusing `params` — making one field mean Step params
on a Run target and launch inputs on a Workflow target — is precisely the overloading D4a had to undo, where
one bag carried both a Workflow's parameters and the policy change context and neither could be typed. The
existing prohibition on `params` stays intact and gains a pointer to the right field, and the mirror rule is
added: `inputs` on a **Run** target is refused, because a Run has no launch interface and the field would be
accepted and read by nothing (ADR-0117 D5a's port-with-no-address shape).

Validation timing splits by kind, following ADR-0024 D4's precedent: a **schedule** Trigger's inputs are
literal (event templates are rejected at declaration, D7), so they are validated against the Workflow's schema
**at declaration** — in Git review, not when the schedule first fires. An **event** Trigger's inputs bind
`{{.event.x}}`, so the placeholder is not the value the schema must accept; they are validated after
substitution by D4's chokepoint, and a binding that cannot resolve against the payload is a **terminal** data
error — dropped, never redelivered (ADR-0024 D6: a poison message must not loop).

This **sharpens ADR-0024 D2**, which deliberately routed the payload for Workflow targets through
`DAGInput.Event` for per-Step resolution. That still happens; what D2 did not cover is a Trigger that also
wants to bind the Workflow's declared inputs. Both now travel.

### D6 — Core never parses tool content to discover parameters

`access_subject` / `access_scope` / `access_kind` are Jinja variables inside **opaque play content**. Core must
never parse that content to infer a Workflow's inputs — that is core learning Ansible's language (§1.4) and the
new-config-language non-goal. The only compliant fix is explicit declaration: the Workflow declares its input
schema, and the Step maps values into `extraVars` via `{{.launch.x}}`.

This is written down because "just derive the inputs from the playbook" is the obvious next suggestion and it
is a violation. The repo-hygiene guard that catches unbound play variables is a **test over our own estate**
(`core/internal/desiredstate/*_test.go`), never a code path in the parser — a distinction the implementation
must preserve.

## Deferred

**ADR-0119 — versioned configuration and promotion.** `Intent.version` + `Assignment.intentVersion` (pinned as
`intent: tls-app@3`, mirroring `blueprint: x@1`), so promotion test → stage → prod is a reviewed Git bump and a
prod configuration is immutable between bumps — Crossplane's `compositionUpdatePolicy: Manual` in Stratt's
vocabulary. Split out of this ADR on review: it changes the **identity key of a Named Kind**
(`graph.intent` is keyed on `name` alone with `ON CONFLICT (name) DO UPDATE`,
`core/internal/graph/intentstore.go:24`) and therefore touches orphan-Finding and prune behaviour, the
`Intent/Compute` and singleton provisioning reconciles (ADR-0058/0059), `CompiledOrigin` for §1.8 descent, and
§4.3's compile-diff obligation. That is a different and larger blast radius than plumbing values, and it earns
its own review rather than riding in as a footnote.

**ADR-0120 — keyed, placement-aware spread.** `zones` × `perZone` expanded as typed fields in Go with **keyed**
identity (`web-use1a-01`) instead of ADR-0058's positional ordinal, bound to ADR-0059's placement Relation and
gated by ADR-0058 D4's max-delta on any zone-list edit. Terraform's `for_each`-over-`count` guidance is the
precedent: keyed instances survive reordering, positional ones are destroyed and recreated. Separate because it
changes instance identity and owes a migration story for fleets already carrying positional names.

## Review record

| Finding                                                                                                                 | Verdict            | Resolution                                                                                                    |
| ----------------------------------------------------------------------------------------------------------------------- | ------------------ | ------------------------------------------------------------------------------------------------------------- |
| **V1** — a third layer makes a precedence ladder across Named Kinds; ADR-0083 D5's exclusive half was never implemented | accepted, verified | D1: co-equal declarations, exclusive collision **fails the compile**; ADR-0083 relationship reframed honestly |
| **V2** — bespoke input type enum is a second validation dialect (charter 34/114/120)                                    | accepted, verified | D2: inputs are a JSON Schema Contract validated by the vendored validator                                     |
| **V3** — MCP cannot supply inputs; validation would sit above the transports                                            | accepted, verified | D4: one chokepoint below all four launch paths; MCP forwards inputs                                           |
| **F1** — second unauthored merge at launch                                                                              | accepted           | D3: one binding site per input; conflicting launch key rejected                                               |
| **F2** — no door reads `Baseline.RemediationWorkflow`; `required` would regress hand-launches                           | accepted, verified | D3: explicit remediate-from-Finding path on every surface, rendering params first                             |
| **F3** — complete-vs-partial spec validation collides with omit-to-override                                             | accepted, verified | D1: partial per layer at declaration, complete on the merged spec at compile                                  |
| **F4** — `Environments` is a set, so per-env values means per-env Assignments                                           | accepted           | Context (2) corrected; env-keyed value maps forbidden and linted                                              |
| **F5** — lists can only grow                                                                                            | accepted           | D1: stated; list variation requires the Intent to omit the list                                               |
| **F6** — don't copy params onto the Finding                                                                             | accepted           | D3: read from the Baseline at launch                                                                          |
| **F7** — `with` and `specProvenance` should not freeze                                                                  | accepted           | D3 → `remediationParams`; D1 → `compiledFrom.specLayers`                                                      |
| **F8** — the schedule Trigger drops params too; D5 revisits ADR-0024 D2                                                 | accepted, verified | D5: both paths fixed; recorded as sharpening ADR-0024 D2                                                      |
| **F9** — silent type coercion would add evaluation semantics                                                            | accepted, verified | D1: fail at compile; demo spec corrected to a number                                                          |
| **F10** — never parse play content to infer inputs                                                                      | accepted           | D6, including the test-vs-core distinction                                                                    |
| **D4 scope** (versioning) — changes a Kind's identity key                                                               | accepted           | Split to ADR-0119                                                                                             |

## Charter alignment

- **§2.4 / §4.1 (anti-GPO).** No `priority`/`precedence`/`order`/`weight`/last-writer field is introduced.
  There is exactly one yielding layer (defaults, which yield by definition) and no rung order among
  declarations: a contest is a **compile error**, not a resolution. This is stronger than the first draft and
  stronger than what ships today.
- **§3 (Contracts are data).** Inputs are JSON Schema validated by the standard validator — no second dialect,
  no hand-rolled validator, no language classes.
- **§1 (no new configuration languages).** `{{.ns.path}}` field lookup only. No loops, conditionals,
  `for_each` or evaluation. Three creep vectors closed explicitly: env-keyed value maps (D1), type coercion
  (D1), content-parsing to infer inputs (D6).
- **§2 (vocabulary frozen).** No new Named Kind; fields on existing Kinds only. `remediationParams` reuses a
  frozen concept's name deliberately; `specLayers` avoids overloading **Provenance**.
- **§1.2 (one authority per fact).** Enforced, not merely intended: two declarations of one value cannot both
  land. Baselines stay compiler-written; Findings stay pure observations.
- **§1.1 (type the seams).** The launch door is typed as a closed schema; nothing types a whole Entity.
- **§1.6 (one capability, every surface).** One chokepoint below every transport; MCP gains input passing; the
  UI form is generated from the same schema.
- **§1.8 (never hide diagnosis).** Every failure names its offender at the earliest point it can be known:
  declaration → compile → launch → dispatch.

## Consequences

- **Positive.** A value is declared once, and a second declaration is a build failure rather than a silent
  override. A Workflow becomes a reusable template with a schema-discoverable interface that drives UI, CLI and
  MCP alike. Remediation gains a real door that shows what it will pass. Two broken reference Workflows are
  fixed and the class of bug is guarded.
- **Omit-to-override is a real ergonomic cost.** Setting a value per environment requires removing it from the
  Intent, which reads as unusual until the reason lands. It is the price of no precedence, and `specLayers`
  plus a named compile error are what make it teachable.
- **Per-environment still means per-Assignment.** `Environments` is a set; this ADR does not change that.
- **New compile-time coupling.** A Workflow edit can fail an unrelated Assignment's compile (D3's
  cross-check). That is the §1.8-correct direction, but it must surface as a named, specific error.
- **Backward compatibility is load-bearing.** Every field added is optional; every existing estate must parse
  and compile byte-identically — asserted by regression test, not by inspection. The one deliberate
  behaviour change is that a pre-existing Intent/Assignment pair setting the same path now fails; no in-repo
  estate does, and the error names both layers.

## Alternatives considered

- **Last-layer-wins across three layers** (the first draft) — rejected on review as a precedence ladder over
  Named Kinds; §2.4, and ADR-0083 D5's own prohibition on a core-side precedence table.
- **`Assignment.values` as call-site arguments rather than a layer** — the reviewer's alternative fix, and a
  good one: parameter binding has zero precedence by construction. Rejected in favour of the disjointness rule
  because that rule finally implements ADR-0083 D5 as written, keeps one merge mechanism, and holds at N
  layers. Recorded because it remains a legitimate reshape if layering later proves confusing.
- **A `values/` tree with merge rules (Helm)** — rejected: a second configuration plane racing the Intent layer
  (§1.2) whose merge order is a precedence ladder in all but name (§2.4).
- **A precedence ladder for values (Terraform/Ansible)** — rejected by charter line 69. Recorded because it is
  the industry default and will be proposed again by anyone arriving from those tools.
- **A bespoke `WorkflowInput` struct** — rejected on review (charter 114/120); JSON Schema is both compliant
  and less code.
- **An expression language for computed values** — rejected; the §1 permanent non-goal, settled by ADR-0024.
- **Overlay _directories_ as the override site** (ADR-0083 D5's literal mechanism) — rejected in favour of a
  field on the Assignment, because the Assignment is already the environment-scoped instantiation and a
  directory convention would be a second composition mechanism competing with the Kinds.
- **A new `Values` Named Kind** — rejected; §2 freezes the vocabulary at v1.0.
- **Duplicating an Intent per environment** (the no-code option) — rejected: it multiplies the authority for
  every shared value and makes promotion a diff across N documents.

## Follow-ups

- **ADR-0119** (versioned promotion) and **ADR-0120** (keyed spread) — see _Deferred_.
- **A core-owned `ChangeContext` schema** with enums for `environment`/`changeClass`, validated at D4's
  chokepoint — closes D4b's typo hole on a security-relevant seam.
- ~~**Compiled params for the withdrawal path**~~ — **done**, as `Blueprint.removeParams`; see D3. The premise
  recorded here was wrong: it said this "needs the orphan branch to carry the effective spec", when the
  **compile** already has it. The withdrawal path only ever needed to read what the compile knew, so the params
  are resolved at compile and stamped on the Baseline — which is also the only place they can live, since
  withdrawal deletes the Assignment whose values fed the merge. `estate/blueprints/{access,fileset}.yaml` now
  declare them, so `access-revoke` and `fileset-revert` receive the grant/path the state was created under
  instead of having it retyped. The `fileset` case is the sharp one: that play removes a file by absolute path.
- ~~**A launch door for the withdrawal path.**~~ — **done**, and it needed no new endpoint. The existing
  `GET`/`POST /findings/{id}/remediation` now serves orphans: `resolveFindingLaunch` checks the Finding's own
  withdrawal spec **before** attempting the Baseline read, since `Apply` writes the orphan Finding and then
  prunes the Baseline (correctly — a Baseline whose Assignment is withdrawn must stop being observed), and
  `graph.finding.baseline` has no foreign key, so the Finding survives pointing at a row that is gone.
  - **The spec travels on the Finding, in typed columns.** `Finding.removeWorkflow` / `removeParams`
    (migration 00042, additive). This is the one place a Finding carries its own launch spec rather than
    reading it from its Baseline — the copy this ADR refused elsewhere as "a second, staleable record of a
    Git-derived fact" is here the **only** record, so that reasoning does not apply.
  - **Not scraped from `diff`.** The same values already ride in the orphan's detail blob for humans, but
    `diff` is documented as redacted and size-capped; a launch that parsed its way back out of it would break
    the day anything capped it, with no failing test to notice.
  - **One door, two acts, said out loud.** `FindingRemediation.withdrawal` distinguishes retiring abandoned
    state from converging live state. Same door because both answer "resolve this Finding"; flagged because
    they are not the same act.
  - **`onRemove: retain` now explains itself.** A retained orphan has no Baseline and no withdrawal Workflow;
    it used to answer `baseline <name> not found`, which describes a missing row when the real answer is that
    the declaration asked for the state to be kept (§1.8).
  - The decision is split from the I/O (`resolveFindingLaunch` takes the Baseline lookup as a parameter)
    because `Server.Store` is a concrete `*graph.Store` and a handler test would be Postgres-gated, hence
    skipped in `task ci` — the failure mode this repo has hit repeatedly. The withdrawal branch, which exists
    precisely to serve a Finding whose Baseline is **gone**, would otherwise have been the hardest path to
    cover and the least covered.
- **A fourth per-environment overlay layer**, if duplication across sibling Assignments becomes real pain.
  Note it must obey D1's disjointness or it re-introduces the ladder.
- ~~**Tighten `contracts/intents/application.schema.json`**~~ — **done.** Landed as
  `application.v2.schema.json` (a sibling version, because tightening a type is breaking) typing `port` as a
  string to match the `app.config` Facet. The live run had already shown the cost of leaving it: an Intent
  declaring `port: 443` parsed cleanly and failed only at facet write-back. Still deliberately OPEN — closing
  it would force core to know every application's config fields (§1.1/§9), so only fields a shipping route
  consumes are typed.
- **`environments` as a single value rather than a set**, if per-environment values make the set genuinely
  awkward. Not proposed here; it would touch every EnvScoped Kind.
