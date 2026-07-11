# ADR 0012 — Views UI v1: React shell, OIDC login, descent screens

- **Status:** Accepted
- **Date:** 2026-07-11
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.6, §1.8, §3 (frontend stack), §8 (Phase 1 "Views UI"); ADR-0003 (UX laws)

## Context

Phase-1 board item. The charter fixes the stack (React + TS + Vite · TanStack
Router/Query · vendored components · Tailwind build-time only); ADR-0003 and
docs/ux/* fix the constraints (semantic tokens only, URL-addressable descent, SSE
never truncated, status = color + icon + label). Slices 5–7 delivered what the UI
renders: real identity, Runs with provenance, Triggers, Workflow DAGs, Gates.

## Decision

1. **Scope = what the API honestly serves.** Screens: Views + members + Entity
   detail with per-Facet Provenance; Runs + the live Run Stream; Workflows +
   WorkflowRuns (per-Step descent); the Gates approval inbox; Triggers. The
   schema-driven components (SchemaForm/SchemaTable/PlanDiff — laws L3/L5/L6/L7/L8)
   need Phase-2 Contract machinery and are **deliberately not built**; law coverage
   now: **L1/L2** (fetch-parsed SSE tail, rAF-batched, virtualized, uncapped),
   **L4** (DescentRail + every rung linked: WorkflowRun → Step → Run → events;
   Provenance badges link Facet → Run), **L9**, **L10** (every screen a route).
2. **Dependencies** (scouted): the charter-named set plus `@tanstack/react-virtual`
   (RECOMMEND — same TanStack trust boundary; log virtualization) and
   `openapi-typescript` (RECOMMEND — dev-only; UI types generated from
   core/api/openapi.yaml in `task generate`, mirroring oapi-codegen; drift caught by
   `generate:check`). No component library: primitives are vendored in-repo
   (`ui/src/components/`), the modal is the native `<dialog>`.
3. **Tokens as data:** `ui/src/tokens.css` carries docs/ux/design-tokens.md verbatim
   (reference → semantic → domain tiers; dark = selected theme via `data-theme` +
   `prefers-color-scheme`); components consume semantic/domain tokens only.
4. **Auth: hand-rolled PKCE** (authorization-code + S256, ~100 lines) against the
   Zitadel SPA app the dev bootstrap now provisions (USER_AGENT + auth method NONE +
   JWT access tokens — the same Bearer the API already verifies via JWKS, ADR-0009).
   No OIDC client library: one well-understood flow beats a dependency (§1.4).
   Tokens in sessionStorage (dev posture; production hardening lands with the Helm
   slice). With OIDC unconfigured, the shell offers the dev-principal field that
   mirrors the server's gated header mode. The UI is a **pure client of /api/v1** —
   the same API and grants as CLI/CI/agents (§1.6); it holds no privileged path.
5. **SSE without EventSource:** Run event kinds are tool-shaped and unbounded, but
   EventSource only delivers *named* listeners — so the Run Stream parses
   `text/event-stream` frames from `fetch` directly and takes every event.
6. **Serving:** dev = Vite on :5173 with `/api` proxied to :8080 (CORS-free);
   production = `STRATT_UI_DIR` static serving with SPA fallback from strattd
   (go:embed single-binary packaging deferred to the Helm slice).
7. **New API list endpoints** (`GET /views`, `GET /runs?limit`,
   `GET /workflow-runs?limit`) — additive, needed by any client, not UI-private.

## Consequences

- Live WorkflowRun state uses TanStack Query polling (2–5s); pushing
  workflow/gate change events over the bus (and SSE beyond Runs) is a follow-up.
- The Gates inbox makes slice-7 approvals usable by humans; agents keep the
  identical POST /gates/{id}/decision (§1.6 parity by construction).
- Follow-ups: token refresh + silent renew; View Explorer graph canvas
  (categorical palette §5.4); CommandPalette (CLI-verb parity); virtualized member
  tables at 10⁵ scale; `forced-colors` audit; UI-side lint gate for raw-hex-in-
  component (design-tokens compliance checklist).
