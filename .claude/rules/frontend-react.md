---
paths:
  - "**/*.ts"
  - "**/*.tsx"
  - "**/*.css"
---

# Frontend (React/TS) rules — charter §3.1

- **The UI is a first-party, pure `/api/v1` client (ADR-0091).** The **OpenAPI contract**
  (`core/api/openapi.yaml` → generated `ui/src/api/schema.d.ts`) is the SINGLE seam between the UI and
  Stratt. The UI imports nothing from the Go core, holds no privileged path, and authenticates as an
  ordinary Principal (same grants/authz/audit as CLI/MCP, §1.6). **API-first:** a new UI capability
  requires the corresponding API capability *first* — the UI can do nothing the API doesn't expose, so a
  change is cleanly *either* backend/contract *or* UI presentation. Never a sovereign-port plugin (that
  port is outbound tool breadth); never gated/paywalled/optional-for-diagnosis (§1.3/§7.5/§1.8, L9).
  Served-by-default by strattd (`STRATT_UI_DIR` → `go:embed`), but serving is packaging, not coupling.
- **Stack:** React + TypeScript + Vite · TanStack Router + Query. Node current-or-previous LTS,
  framework majors ≤ N-1, CI-gated (§1.7).
- **Components are vendored, not depended-upon.** Headless accessible primitives (Radix today; Base
  UI on the watchlist) with shadcn-style **copy-in components owned in-repo**. Nothing external owns
  our components — swapping the primitive layer must stay a refactor, not a rewrite. Do not add a
  component library as a hard runtime dependency.
- **Styling:** Tailwind, **build-time only** (no runtime CSS-in-JS lock-in).
- **Design tokens as data:** all theming via CSS variables. **No hardcoded colors/spacing in
  components.** (charter §3.1)
- **Schema-driven rendering is the extensibility mechanism.** Intent forms, Step inputs, Finding
  tables, and plan diffs are generated from JSON Schema (Contracts). Plugins extend the UI by
  shipping **schemas, not React code** — never execute community code in the interface plane.
- **Center-of-gravity screens:** virtualized live log viewer, graph exploration, Run streaming,
  Findings. Real-time via **SSE** (NATS-backed).
- **Diagnosis is a product surface (§1.8):** every view must support one-click descent
  Intent → Blueprint route → Workflow → Run → task event. Never hide failure detail behind the
  abstraction.

## React 19 idioms (this stack)

We're on React 19. Prefer the current idioms — but only the ones that fit an SPA-on-`/api/v1`; the
form/data APIs below are *deliberately* not ours because TanStack Query and react-hook-form already
own that ground.

- **Ref as a prop — no `forwardRef`.** In vendored primitives (`components/ui/*`) accept `ref?:
  React.Ref<…>` as an ordinary prop. `forwardRef` is legacy; keep the copy-in components on the modern
  form so swapping the primitive layer stays a refactor.
- **`useEffectEvent` for the live/SSE effects.** The center-of-gravity streams (live-log, run-events,
  SSE) have effects that must read the *latest* props/handlers without re-subscribing. Extract the
  non-reactive part into `useEffectEvent` and keep the effect deps to the true resubscribe keys
  (e.g. `runId`), not `theme`/callbacks. Prevents needless reconnects.
- **Ref-callback cleanup.** A `ref` callback may return a cleanup fn — use it for observers/listeners
  (DAG canvas, virtualized log) instead of a paired `useEffect`.
- **`use()` for context/promises** where it reads cleaner than `useContext`; data-fetching still goes
  through TanStack Query, not hand-rolled `use(fetch())`.

**Deliberately NOT used (don't let a generated snippet introduce these):**
- **No React Server Components / `'use client'` / Next.js** — the UI is a pure client SPA served by
  strattd (ADR-0091). Anything RSC-shaped (`cache`, `cacheSignal`, server actions) is out of scope.
- **No Actions form APIs** (`useActionState`, `useFormStatus`, `<form action>`). Forms are
  **react-hook-form + Zod** via the schema-driven `SchemaForm` (§ schema-driven rendering above).
- **Optimistic UI via TanStack mutation `onMutate`/rollback, not `useOptimistic`** — keep one
  optimistic mechanism (see the `useStartRun`/`useDecideGate` mutations).
- **State: TanStack Query (server state) + Zustand (UI state) only** — don't add Redux or a third
  store; "choose a state library" is already decided (§1.4 boring spine).
