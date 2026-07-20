# ADR 0090 — UI rebuild: greenfield on the charter stack, gauntlet-informed patterns

- **Status:** Accepted
- **Date:** 2026-07-20
- **Deciders:** steward (dstout)
- **Reviews:** charter-guardian — **PASS** (§3 stack recovered — the old UI skipped the mandated
  vendored-Radix/shadcn posture; §3 transport correctly parked; §1.8/§1.6/§7.3/§7.5/§2 clean; flags folded
  below). Also ratified **ADR-0003** Proposed→Accepted (its L6 max-delta-gate must-fix folded).
  dependency-scout — **RECOMMEND**, must-watch folded (below). vocabulary-linter — two IA fixes folded (below).
- **Gate outcomes folded:**
  - *dependency-scout:* **vendor `@microsoft/fetch-event-source` in-repo** (~200 LOC; ~5-yr-stale upstream on
    the crown-jewel path) — Stratt owns the SSE reconnect/backoff code. **Skip `json-schema-to-zod`** (declared
    unmaintained) — build Zod inline from JSON Schema per gauntlet's own `SchemaForm` (`buildZodForProperty`),
    no dependency. **Defer `@xyflow/react` + `elkjs` + `comlink` to slice 3** (graph canvas) — and record the
    **EPL-2.0** license election for `elkjs` (dual EPL-2.0/GPL-3.0; never let GPL default) + a **bundle-budget
    CI gate** on the graph route then. `sonner`/`cmdk` accepted (vendor `sonner` later only if it goes dark).
  - *vocabulary-linter:* **Gate is not a Named Kind** — the approval inbox is a screen *within* the Runs
    section (Run/Workflow descent), never a top-level `/gates` nav section. **Adoption is not a Named Kind** —
    `adopt` is an Action producing a Run; it surfaces as an **Entity action** (Graph section) that launches a
    Run, never an `/adoptions` entity route. "Fleet" is kept as a descriptive section *label* (not a
    core-model identifier; matches the screen-catalog).
- **Charter sections:** **§3 / §3.1** (frontend stack — React+TS+Vite, TanStack Router/Query, vendored
  Radix/shadcn owned in-repo, Tailwind build-time only, schema-driven rendering, SSE real-time, Node
  current-or-previous LTS; the rebuild stays inside this mandate), **§1.8** (the abstraction must never hide
  diagnosis — one-click descent is the flagship flow), **§1.6** (agent-native, human-first — the UI is a pure
  `/api/v1` client under one Principal/authz/audit, 1:1 with the MCP tool surface), **§1.7** (evergreen — new
  deps stay ≥ N-1, CI-gated), **§2** (frozen Named-Kinds vocabulary in every route/label/component).
- **Supersedes:** the current `ui/` implementation (a Phase-0/1 thin client, ~30% of the intended product).
  Preserved in git history; replaced in place.
- **Builds on:** ADR-0012 (Views UI v1 — what the current UI proved), ADR-0003 (the ten UX laws, ratified
  here), the `docs/ux/{screen-catalog,design-tokens,competitive-teardown}.md` design foundation, and a
  read-only study of the `gauntlet` frontend (a realized reference of these same laws on a ~90%
  charter-aligned stack).

## Context

The current `ui/` is honest and charter-aligned but partial: the **schema-driven rendering thesis**
(plugins ship JSON Schema → get a UI for free; SchemaForm / SchemaTable / PlanDiff) is **entirely unbuilt**;
4 of the 7 catalogued sections don't exist; there is no graph canvas, no command palette, **zero tests**, and
it is desktop-only. The reusable value is concentrated in non-visual layers (generated types, a fetch client,
hand-rolled OIDC/SSE, design tokens) — but gauntlet demonstrates *better* patterns for even those (a real
OIDC library, `fetch-event-source`, a runtime JSON-Schema→Zod→RHF form engine, `@xyflow`+`elkjs` graph,
`cmdk`, optimistic-mutation + query-options-factory data layer, Comlink workers). The steward chose a **true
greenfield** rebuild taking **gauntlet's feel in our charter stack**, first slice = **the §1.8 descent spine**.

## Decision

**Rebuild `ui/` greenfield on the charter-mandated stack, porting gauntlet's responsiveness patterns onto our
OpenAPI transport. The transport does not change (§3 OpenAPI-first stands); only the frontend is rebuilt.**

### 1. Stack (charter §3, unchanged) + the dep additions
React 19 · Vite · TypeScript · Tailwind v4 (build-time) · TanStack Router/Query/**Table**/Virtual · vendored
**radix-ui + shadcn** primitives owned in `ui/src/components/ui/` (the charter-mandated component posture the
old UI skipped). New deps, all present in gauntlet and mostly charter-blessed classes (dependency-scout
gates the set): `openapi-fetch`, `oidc-client-ts`, `@microsoft/fetch-event-source`, `class-variance-authority`
+`clsx`+`tailwind-merge`+`tw-animate-css`+`lucide-react`, `react-hook-form`+`@hookform/resolvers`+`zod`+
`json-schema-to-zod`, `@xyflow/react`+`elkjs`+`comlink`, `cmdk`, `motion`, `sonner`, `zustand`, fontsource,
`vitest`+`@testing-library/*`+`playwright`.

### 2. Transport stays OpenAPI (§3); the responsiveness is transport-agnostic
Every gauntlet feel-pattern is independent of the wire protocol — "the client call is just the `queryFn`
body." We ship on the existing `/api/v1` via a typed **`openapi-fetch`** client. Whether Stratt's *native
API* should adopt Connect-RPC/proto is a **separate charter-§3 question** (OpenAPI-first is mandated; the AWX
-compat `/api/v2` façade must stay REST for awxkit/terraform-provider-awx regardless) — parked for its own
dependency-scout + charter-guardian + ADR evaluation on control-plane merits, never a rider on the UI arc.

### 3. The data layer (the responsiveness core)
Per-resource **`queryOptions()` factories** over a centralized hierarchical **queryKey** helper — the same
factory feeds `useQuery`, hover-prefetch (100 ms debounce), and route preload, so paths never double-fetch.
**Optimistic mutation template** (`onMutate` cancel→snapshot→`setQueryData` → `onError` restore → `onSettled`
invalidate) for `startRun`/`decideGate`/`adoptObject`. **URL is the filter store** (Zod `validateSearch` +
`.catch({})`) so every diagnostic state is linkable (§1.8 L10).

### 4. Real-time — the SSE data-vs-keys split (recorded deliberately)
Our `GET /runs/{id}/events` streams **actual RunEvents** → they feed the **virtualized live-log** (data path,
via `@microsoft/fetch-event-source` + `@tanstack/react-virtual` + rAF batching; uncapped, follow-tail — the
AWX-beating L1/L2 guarantee). Gauntlet's *stream-sends-keys-only → `invalidateQueries` → normal refetch*
pattern is the **target for a future general event stream**; until the backend grows one, list freshness
stays on TanStack polling (2–5 s), as today. This is an explicit, revisitable split, not an oversight.

### 5. Schema-driven rendering is the extensibility mechanism (ADR-0003 L7/L8)
Forms/tables render generically from `GET /contracts` JSON Schema: a runtime **JSON-Schema → Zod → RHF** form
engine + a read-only **schema-renderer**, with a declarative **`x-*` widget-hint vocabulary** (`x-renderer`,
`x-entity-type`, `x-secret-name-prefix`, `x-suggestions`) — data annotations, **not** an evaluable expression
language (§7.5 no-new-config-language holds). Plugins extend the UI by shipping *schemas, not React*; no
community code executes in the interface plane (§7.3). Widget extensions, if ever needed, come from a
**core-owned in-repo registry**, never plugin-shipped code.

### 6. Design system — tokens as data (§3, `design-tokens.md`)
The three-tier token system (reference → semantic → domain; **fixed status + 8-series categorical palettes**;
**color+icon+label, never color alone**) is expressed in **Tailwind v4 `@theme`** (CSS variables, dark as a
selected theme, no runtime CSS-in-JS). Components consume semantic/domain tokens only — raw hex or a
primitive-tier token in a component is a lint defect (a UI lint gate enforces it).

### 7. Agent parity + vocabulary (§1.6, §2)
The UI is a pure `/api/v1` client holding no privileged path — the same API/grants/audit as CLI/CI/agents,
its action set 1:1 with the MCP tools. Every route, screen title, and component name draws only from the
frozen Named Kinds; the banned terms (`inventory`/`playbook`/`job template`/`CI`/`CMDB`/`resource`) never
appear (vocabulary-linter gates).

### 8. First slice = the descent spine; testing from day one
Slice 1 rebuilds the §1.8 flow narrow-and-deep (shell + design system + data layer + OIDC + the crown-jewel
Run Stream + Findings + Entity/View + Gates) with **vitest** coverage (pure-logic seams) and a **Playwright
bench** asserting the Phase-0 / L10 latency budgets (View query < 200 ms @ 50k; every descent state
URL-addressable) — the UI's first CI perf gate. Later slices (each ADR-gated) add the authoring SchemaForm +
Intents, the graph canvas depth, and the Connectors/Fleet/Admin sections.

## Charter alignment
- **§3:** stays inside the mandated stack (adds the vendored Radix/shadcn posture the old UI omitted); Tailwind
  build-time only; tokens as data; schema-driven rendering; SSE real-time; evergreen deps.
- **§1.8:** the descent is the flagship, uncapped + virtualized + URL-addressable; diagnosis is a product
  surface, not a CLI fallback.
- **§1.6:** pure API client; UI/CLI/agent parity; one Principal/authz/audit.
- **§7.3/§7.5:** plugins ship schemas not code; the hint layer is declarative data, no new config language.
- **§2:** Named-Kinds-only naming.

## Alternatives considered
- **Evolve the current UI in place.** Rejected by the steward — the headline schema-driven surfaces and 4/7
  sections are unbuilt anyway; a fresh foundation on the proper vendored-component posture is cheaper than
  retrofitting and gets the gauntlet-grade feel.
- **Adopt gauntlet's stack wholesale (Connect-RPC/proto).** Rejected for the UI — the feel is
  transport-agnostic; changing the native API transport is a separate charter-§3 decision (see §2).
- **Keep the old non-visual layers (client, OIDC, log viewer).** Rejected (true greenfield) — gauntlet's
  patterns (openapi-fetch typed paths, `oidc-client-ts`, `fetch-event-source`) supersede the hand-rolled
  versions; re-deriving them on the new foundation is an upgrade, not a loss.

## Slice roadmap
1. **This ADR + the descent spine** (foundation + Run Stream + Findings + Entity/View + Gates + tests).
2. Writable SchemaForm (Run/Intent/Assignment authoring) + Intents section + PlanDiff (membership delta).
3. Connectors/Fleet/Admin sections + GraphCanvas depth (blast-radius/neighborhood) + WorkflowDAG.
4. CommandPalette CLI-verb parity + a11y hardening (`forced-colors`, screen-reader) + the general
   invalidation-SSE once the backend event stream exists.
