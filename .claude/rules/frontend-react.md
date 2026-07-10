---
paths:
  - "**/*.ts"
  - "**/*.tsx"
  - "**/*.css"
---

# Frontend (React/TS) rules — charter §3.1

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
