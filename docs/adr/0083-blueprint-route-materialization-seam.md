# ADR 0083 — The Blueprint route is the tool-materialization seam; declare outcomes, plugins materialize (+ G6 defaults/override)

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** steward (dstout)
- **Reviews:** charter-guardian PASS-WITH-CHANGES on the doctrine (folded: kind-vs-value distinction, per-capability route MAP not scalar, sufficiency-gate as admission, the six guardrails below); vocabulary-linter on the new field identifiers before freeze.
- **Charter sections:** §1.1 (type the seams, not the world), §1.2 (projections, never a second truth), §1.4 (boring spine, pluggable everything), §1.8 (never hide diagnosis), §2 (frozen vocabulary), §2.4/§4.1 (no implicit precedence), §5 (no silent auto-launch), §7.6 (strangler-fig per-route accounting), §9 (no ontology creep)
- **Sharpens:** ADR-0055 (Estate Composition — makes the materialization seam explicit; **discharges G6**), ADR-0023 (Intent/Assignment/Blueprint compiler), ADR-0058 (provisioning from Intent)
- **Unblocks:** G5 (the unified onboarding template) — which may not ship its defaults/override merge until this ADR lands

## Context

The operator experience we are building toward (ADR-0055, charter §0): *"a web server is deployed to a group of
devices; those devices are config-as-code and are VMs"* — declared from the **simplest, most tool-agnostic form**,
sane defaults + optional overrides, and Stratt fans it across provision → network → DNS → cert → configure → app.

The failure mode to design **against** is a **side-by-side per-tool configuration** — "helm is configured here, and
Ansible is configured here, and Chef is configured here" — bolted next to each other in the operator's estate. That
is the AWX/CMDB shape the charter refuses: it forces the operator to know and maintain the *mechanism* of every tool,
and it makes "which tool realizes this outcome" an operator burden instead of a routing decision.

The machinery to avoid this already exists — the Intent/Assignment/Blueprint compiler (ADR-0023), provision→build→
project-back (ADR-0058), and the composition frame with its landscape-agnostic L1/L2/L3 constructs (ADR-0055). What is
**not yet explicit** is the single load-bearing principle that ties them together, and one gap (G6) the onboarding
template leans on is undecided. This ADR fixes both.

**In scope:** (1) naming the Blueprint route as *the* tool-materialization seam, as a per-capability map; (2) discharging
**G6** (the typed defaults/override merge). **Out of scope:** building the G5 onboarding template (a follow-up BUILD task,
now unblocked); any new Intent *kind* (the frozen vocabulary is untouched).

## Decision

### 1. Declare outcomes, not tool configs. The Blueprint route is the sole tool-materialization seam.

An operator declares an **outcome** — a capability bound to a group — and **never** the tool that realizes it. The tool
(Helm / Ansible / Chef / Crossplane / OpenTofu / …) is named in exactly **one** place: a **Blueprint route**, which binds
a capability to an **Actuator (plugin)** that **materializes** the tool-specific state. The route is authored **once** per
capability shape, defaulted, and reused; it is the only surface where a tool name ever appears. Core stays content-blind —
it **routes and accounts**, it never *understands* the tool or the capability (§1.4).

- **The operator's per-deployment surface** is: an **Assignment** binding an **Intent** (whose spec carries the capability
  *value*, e.g. `name: web-server`) to a **View** over the target devices, plus optional overrides. Nothing else.
- **The plugin materializes the proper state** — it owns the tool-specific *how* (render the chart, converge the role). The
  spine names no tool (ADR-0046); every Run's Actuator is traceable to the route that selected it.

### 2. Kind is frozen (core); capability is a value (estate-authored). Never conflate them.

`web-server` is **not** an Intent kind — it is a **value** inside a frozen Named Kind (`Intent/Application`, `Intent/Compute`,
… — charter §2). There is **no** `Intent/WebServer`, ever; minting a per-domain kind is the §9 ontology-creep / writable-CMDB
violation. The capability lives as (i) a value in the Intent spec, (ii) a capability-scoped Facet value a route matches on, or
(iii) a label on a projected Entity — **never** as a core-model kind, schema, or field that names a domain concept. Core learns
neither `web-server` nor `helm`.

### 3. Routing is a per-capability MAP, never a scalar (co-management is reality).

One outcome legitimately fans across **several** tools on the same device — a Helm deploy route **and** an Ansible config route
**and** a certissuer cert route. A Blueprint therefore resolves a **per-capability route map** — conceptually
`routes: { app: helm-actuator, config: ansible-actuator, cert: certissuer }` — **never a single scalar "the tool."** Each entry
is an independently-metered §7.6 route (cost / latency / failure per channel). This is also the seam where landscape re-targeting
(VMware → Crossplane) is a route edit no Intent author notices (ADR-0055).

### 4. Sufficiency gate: no route, capability Facet, or Intent schema without a shipping consumer (§1.1, §9).

A capability value, a capability-scoped Facet schema, or a route may **only** land if a **shipping Blueprint route consumes it**.
A `web-server` (or `db`, `cache`, …) schema that no shipping route materializes is a speculative schema — the §1.1 sufficiency
violation and exactly the §9 ontology creep the ADR-0055 risk table names. Enforce as a **compile-time admission check** (the
PEP already at the compile seam, ADR-0073): an Intent naming a capability with no consuming route is rejected at admission, not
silently accepted.

### 5. G6 — the defaults/override merge — discharged as explicit overlay + §2.4 claim-type merge, never precedence.

"Sane defaults + optional overrides" is the softest target for smuggling in implicit precedence ("override beats default" =
last-writer-wins), so it is bound hard (ADR-0055 guardrail 6, §4.1/§2.4):

- **Defaults live in the Blueprint** (the L2 construct — CaC in Git), **never in core Go and never as a core-side precedence
  table.**
- **Overrides are explicit Kustomize-style overlay directories** layered by declaration, resolved by **§2.4 claim-type merge**
  (exclusive double-claim **fails the compile**; additive claims **union** with per-element provenance). There is **no**
  `priority` / `precedence` / `order` / `weight` / last-writer field anywhere in the defaults/override path. "Override beats
  default" is never an implicit rule — it is an explicit overlay layering the operator can read.
- The merge is **typed-field expansion + `{{.ns.path}}` substitution over JSON-Schema specs** — **never** loops / conditionals /
  `for_each` / expression *evaluation*. The moment a config value must be *evaluated* rather than *substituted*, it is the
  forbidden config-language non-goal.

### 6. The build stays gated and the descent stays intact.

- **§5 no silent auto-launch:** "declare web-server → it deploys" resolves to a **plan-Gated Workflow** under the §4.3 max-delta
  blast-radius gate (ADR-0058 decisions 2/4). The abstraction may **surface** a build; it may never **launch** one. A count/
  delta beyond the fraction pauses the whole batch pending approval — never a silent cap, never an auto-apply.
- **§1.8 descent survives the abstraction:** the full ladder **Intent → resolved Blueprint route → Workflow → Run → task event**
  must remain one-click descendable *through* the composition. This is why §3 forbids silent plugin auto-selection: an unnamed
  route is a broken rung. **Any composition surface that cannot render its resolved route + Run + task-event descent is
  non-shippable.** Hiding *mechanism* is the product; hiding *which route ran and whether it failed* kills it.

## The six guardrails (binding; a violation if broken)

1. **Anti-GPO merge (§2.4/§4.1).** Defaults/override resolves only via explicit overlay directories + claim-type merge; no
   precedence/priority/weight/last-writer field is introduced anywhere in the path.
2. **Explicit named routing (§1.4/§7.6/§1.8).** Tool selection is a value in a versioned Blueprint route, a per-capability map,
   never a scalar; no plugin auto-selects its own actuation; every route is a metered unit.
3. **Sufficiency gate (§1.1/§9).** No Intent-kind schema, capability Facet schema, or route lands unless a shipping route
   consumes it; speculative capability schemas are rejected at admission.
4. **Projection purity (§1.2).** Desired existence lives only in Git (`Intent/Compute`); the graph holds only built infra; no
   phantom/desired-count rows; write-back only via Run provenance or a declared Normalizer, enforced by `enforce_write_path`
   (cross-check ADR-0058 decisions 2/5/6).
5. **Gated build (§5/§4.3).** "Declare → deploy" resolves to a plan-Gated Workflow under the max-delta gate; never an auto-apply.
6. **Descent survives (§1.8).** Any composition surface must render the resolved-route → Workflow → Run → task-event descent in
   one click, or it does not ship.

## Charter alignment

- **§1.1/§9:** the capability is a value at a typed seam demanded by a shipping route; core gains no domain kind/schema/field. No
  universal ontology, no writable CMDB.
- **§1.4:** tool knowledge lives in the versioned Blueprint route (a plugin/steward surface), not core; core is content-blind.
- **§1.2:** devices-as-code declare desired existence in Git only; built infra projects back via provenance; no second truth.
- **§2.4/§4.1:** defaults/override is explicit overlay + claim-type merge; no precedence field.
- **§5/§1.8:** the build is gated; the Intent→route→Run→event descent is preserved end to end.
- **§7.6:** each per-capability route is the metered accounting unit; retiring a backend is a route edit no Intent author notices.

## Consequences

- **Positive:** the operator declares one outcome (`web-server → group`) with no side-by-side tool config; the tool is a route
  detail, defaulted and reused; co-management (deploy + config + cert on one box) is first-class via the route map; landscape/tool
  re-targeting is a route edit; the whole thing stays charter-clean by construction (typed seams, open body).
- **Negative / trade-offs:** the "simplest form" arrives in typed increments (G6 → G5 → …), not in one leap; a Blueprint author
  must exist per capability shape (the once-per-capability cost that buys the per-deployment simplicity); the per-capability route
  map is more structure than a scalar, justified by co-management reality.
- **Follow-ups:** run **vocabulary-linter** on the route-map / defaults / overlay field identifiers before they freeze (watch for
  banned `resource`/`inventory`/`provider`/`binding` synonyms and any config-language keyword); then **build G6** (the overlay +
  claim-type merge engine) and **G5** (the onboarding template) as the AAP-e2e capstone — the "linux-fleet" worked example.

## Alternatives considered

- **Plugin silently auto-selects the tool from a generic capability** — rejected: defeats §7.6 (no explicit route to meter) and
  §1.8 (the "Intent → Blueprint route" descent rung goes opaque), and grows a smart-core-in-a-plugin. The route must be explicit
  and named.
- **Scalar "the tool" per outcome** — rejected: co-management is reality; one outcome fans across deploy + config + cert routes.
  The route map is mandatory.
- **A per-domain Intent kind (`Intent/WebServer`)** — rejected: minting domain kinds is the §9 ontology-creep / writable-CMDB
  violation. The capability is a value inside a frozen kind.
- **Centralized capability orchestration (deferred, on the table).** A higher layer that *computes* the per-capability route map
  and centralizes cross-capability defaults (rather than each Blueprint carrying its own) — deferred, **not rejected**. It is a
  natural evolution once many capabilities share defaults, and the per-capability route map + Blueprint-held defaults decided here
  are forward-compatible with it. **Constraint when it lands:** it must still emit **explicit, named, metered routes** (guardrail 2)
  and preserve the descent (guardrail 6) — it may centralize *authoring*, never re-introduce silent auto-selection.
