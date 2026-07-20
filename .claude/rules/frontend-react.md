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
