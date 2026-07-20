# ADR 0003 — UX design principles: schema-driven rendering and one-click descent

- **Status:** Accepted (ratified by ADR-0090, 2026-07-20 — charter-guardian PASS-WITH-CHANGES; the L6
  max-delta-gate must-fix folded)
- **Date:** 2026-07-11
- **Deciders:** Stratt maintainers (project lead)
- **Charter sections:** §1.8, §3.1, §1.1, §1.2, §1.3, §1.5, §1.6, §2.4, §4.3, §7.5
- **Review:** charter-guardian pass (2026-07-11) — four findings folded into L2/L3/L4/L6/L8
  and follow-up (d) below.

## Context

The charter fixes the frontend *philosophy* (§3.1: React/Vite/TanStack, vendored
components, tokens-as-data, schema-driven rendering, SSE) and makes one-click
descent a binding discipline (§1.8), but does not enumerate the concrete UX laws
that make those real. Charter §8 sequences UI *construction* late (live log tail →
View surface → generated portal), yet the interface's load-bearing decisions —
what descent means, how schemas render, where diagnosis lives — constrain the API
and Contract shape and therefore must be settled *before* the spine is built, not
after.

To ground these laws in evidence rather than taste, we ran a competitive teardown
of AWX/AAP, HCP Terraform / Terraform Enterprise, Spacelift, Chef Automate,
Backstage, and Port (see [competitive-teardown.md](../ux/competitive-teardown.md)).
Those tools are widely "UX-last"; the teardown isolates their concrete failures so
we can encode the inverse as law. This ADR does **not** authorize building product
UI (still gated by §8 Phase-0 and §7.4) — it fixes the design constraints the
schema and API must satisfy, and the target the eventual UI is measured against.

In scope: the binding UX laws, the two artifacts they govern (`SchemaForm`/
`SchemaTable`/`PlanDiff`; the descent spine), and the acceptance tests. Out of
scope: visual brand, component-by-component design, and any code.

## Decision

Adopt the following **ten binding UX laws**. Each is testable; a shipped screen
that fails its test is a defect, not a preference. (L1–L10 correspond to the
teardown synthesis.)

1. **Live streaming is a reliability guarantee.** Run output streams over
   NATS-backed SSE, is backpressure-safe, and stays default-on at estate scale.
   *Test:* a Run emitting high-volume events streams to completion without the UI
   freezing or requiring a manual refresh (the AWX #15342/#1831 failure mode).
2. **The descent is never truncated.** The full task-event stream of any Run is
   virtualized and reachable; no event cap hides a failing task. Every descent rung
   is reachable **both** as a rendered screen **and** as a structured API/MCP
   capability under one Principal model (§1.6) — agents descend as data, humans as
   UI. *Test:* the exact failing task event of a large Run is reachable in the UI
   (defeats AWX's 4000-event cap) *and* fetchable via the API/MCP surface.
3. **Diffs render from typed data, never scraped text.** `PlanDiff` consumes a
   typed, versioned plan/drift model (per-target change with an **action** and a
   **reason**), never parsed log text; the model is a *rendering*, never a system of
   record. The plan surface renders the **membership delta** too (§4.3) — which
   Entities join/leave the compiled target set per Assignment — plus the max-delta
   gate state when tripped, so blast radius is never hidden. *Test:* the diff renders
   the config change *and* the Entity join/leave set and explains *why*, from
   Contract data alone with the log stream absent.
4. **Diagnosis is an in-product surface.** From any Intent, the descent
   **Intent → Blueprint route → Workflow → Run → task event** is one click and never
   forces the user to SSH or read raw logs to learn *why* something failed (§1.8).
   *Test:* every rung is a live UI screen **and** an equally-addressable API/MCP
   capability (§1.6); none dead-ends to CLI-only or UI-only.
5. **Drift is a Finding: estate roll-up → per-Entity diff → proposed fix.** A clean
   state is visible without a manual query (defeats Spacelift's invisible clean
   state); a drifted state descends to the observed-vs-expected attribute diff and
   the remediation Workflow. *Test:* drift status is visible estate-wide and
   descends to per-Entity Evidence.
6. **Changes are gated: plan-then-apply with explicit confirm.** Mutating Runs
   surface a compiled plan — including the membership delta of L3 — and require
   explicit confirmation (a Gate) unless an Assignment opts into auto-apply.
   **Auto-apply waives only the routine confirmation Gate; the mandatory §4.3
   max-delta gate still fires and pauses execution regardless of auto-apply** — the
   catastrophic-blast-radius insurance is never opt-out. *Test:* no mutating Run
   applies without a plan (config change + Entity join/leave set) the Principal could
   have reviewed, and a delta exceeding the Assignment's max-delta always pauses even
   under auto-apply.
7. **Presentation metadata lives in the schema.** Facet/Contract schemas carry UI
   hints (title, icon, description, ordering); a Connector that ships a schema gets a
   labeled UI for free, and no community code executes in the interface plane (§3.1,
   §1.5). *Test:* a new Connector's inputs render a labeled form with zero React changes.
8. **Schema-driven forms ship a declarative escape hatch and a conditional model.**
   `SchemaForm` supports declarative widget selection (a `ui:*`-style hint layer),
   async/remote validation, secret masking, and multi-step grouping — and a
   first-class conditional-field story (the Backstage #30090 failure). The escape
   hatch is a **core-owned, in-repo (vendored) extension registry**: a Connector's
   Contract may *name* a platform-provided field extension but may **never ship or
   register widget code** — no community code executes in the interface plane (§3.1,
   §1.5). The hint/conditional layer is **declarative data annotation**
   (RJSF-`uiSchema`-class), explicitly **not** an evaluable logic/expression language
   (guards the "new configuration languages" non-goal, §7.5). *Test:* a conditional
   field and a custom widget both render from schema + a *core-registered* extension
   with no imperative form code; a plugin-supplied widget *implementation* is rejected.
9. **No paywalled diagnosis or drift — ever.** Every diagnostic and drift surface is
   in the single Apache-2.0 product; there is no gated tier (§1.3), inverting HCP's
   paid-tier drift. *Test:* no capability is flag-gated by license tier.
10. **Perceived latency is a trust budget.** Honor the charter Phase-0 gates (View
    query < 200 ms @ 50k Entities; pod-spawn p95 < 5 s), virtualize large surfaces,
    and make every diagnostic state URL-addressable (TanStack Router) so a descent
    step is linkable/shareable. *Test:* the budgets hold at target scale; a descent
    state round-trips through its URL.

These laws govern three artifacts named in [screen-catalog.md](../ux/screen-catalog.md):
the `DescentRail` (L2, L4), the schema-driven `SchemaForm`/`SchemaTable`/`PlanDiff`
(L3, L7, L8), and drift Findings (L5). Colors/states come from
[design-tokens.md](../ux/design-tokens.md).

## Charter alignment

- **Upholds §1.8** (diagnosis never hidden) — L2, L4 are its concrete tests.
- **Upholds §3.1** (schema-driven rendering, tokens-as-data, SSE) — L3, L7, L8.
- **Upholds §1.1** (type the seams) — L3/L7 attach schema at Contracts/Facets only,
  not whole Entities.
- **Upholds §1.2** (drift is the diff) and **§2.4** (Finding/Evidence) — L5.
- **Upholds §1.3** (rug-pull-proof, no gated tier) — L9, a deliberate differentiator.
- **Upholds §1.5** (pinned/hash-verified schemas; no community code in the interface
  plane) and **§1.6** (one model across UI/CLI/CI/agent) — L7, L8, and the
  `CommandPalette`↔CLI parity in the catalog.
- **Tension / review bar:** this ADR *interprets* Founding Disciplines (§1) and uses
  frozen Vocabulary (§2) but changes neither; it therefore does not trip the highest
  review bar. It should still be run past **charter-guardian** before Acceptance, and
  the vocabulary of its governed artifacts has already passed **vocabulary-linter**.

## Consequences

- **Positive:** the UI target is defined and evidence-backed before code; the
  schema/API can be shaped to satisfy L3/L7/L8 from the start; each law has an
  acceptance test, so "diagnosis is a product surface" becomes checkable, not aspirational.
- **Negative / trade-offs:** L1/L2/L10 impose real engineering cost on the streaming
  and virtualization layers (the exact cost AWX avoided by capping/throttling) —
  accepted deliberately. L8's conditional-field + extension model adds `SchemaForm`
  complexity up front.
- **Follow-ups:** (a) add UX acceptance tests (L1–L10) to the frontend CI gate when
  the UI lands (Phase 1); (b) specify the typed plan/drift model (L3) alongside the
  Contract work, kept a rendering and never a system of record (§1.2); (c) a
  dedicated Intune/Jamf MDM-console teardown pass (thin in this round); (d) fold the
  `ui:*` hint vocabulary into the Contract schema spec **as declarative data
  annotations only** — no evaluable expression language (§7.5); (e) enumerate the
  core field-extension registry and its API/MCP-addressable descent capabilities.

## Alternatives considered

- **Leave UX to emerge during Phase-1 build** — rejected: L3/L7/L8 constrain the
  Contract and API shape, so deferring them forces rework of the spine; and §1.8 is a
  discipline, not a late-stage polish.
- **Adopt an off-the-shelf admin framework / component library as a hard dependency**
  — rejected: violates §3.1 vendored-not-depended-upon and would let external code
  own the interface plane (against §1.5).
- **Pure JSON-Schema form rendering with no escape hatch** — rejected: the Backstage
  #30090 conditional-field failure shows pure schema-rendering breaks on real inputs;
  L8's registered-extension escape hatch is required.
- **Match the incumbents' UX (AWX/TFE parity)** — rejected: the teardown shows their
  UX is the problem (truncated descent, out-of-product diagnosis, paywalled drift);
  parity would import the failures Stratt exists to fix.
