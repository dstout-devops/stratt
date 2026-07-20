# Stratt Screen & Component Catalog

**Status:** Design foundation & target — the UX reference the UI is built against (it informed the
greenfield UI rebuild, ADR-0090/0091). Aspirational superset: it catalogs screens beyond what the current
`ui/src/routes/` yet ships. **Charter authority:** §3.1
(center-of-gravity screens, schema-driven rendering), §1.8 (one-click descent),
§6 (power gradient). **Vocabulary:** every screen and component is named from the
frozen Named Kinds (§2); no banned term (`inventory`, `playbook`, `job template`,
`CI`, `CMDB`, `resource`) appears in any identifier here — hence "catalog", not
"inventory".

This catalog is the map from the charter's Named Kinds to concrete surfaces. It is
a design inventory, not a build order — construction follows the §8 phase plan
(live log tail in Phase 0 → View surface in Phase 1 → generated portal in Phase 4).

---

## 1. Information architecture

Primary navigation follows the mental model, not the database. Seven top-level
sections; the §1.8 descent is a **cross-cutting spine**, not a section.

| Nav section | Named Kinds surfaced | Charter |
|---|---|---|
| **Graph** | Entity · Relation · Facet · Provenance · View · Source | §2.1 |
| **Intents** | Intent · Assignment · Blueprint · Baseline | §2.4 |
| **Runs** | Workflow · Run · Step · Trigger · task event | §2.3 |
| **Findings** | Finding · Evidence | §2.4 |
| **Connectors** | Connector (Syncer/Action/Emitter) · Actuator · Source | §2.2 |
| **Fleet** | Site · Bundle · agent | §2.3 |
| **Admin** | Principal · CredentialRef · Contract · authz · audit | §2.5 |

**The descent spine (§1.8).** From any Intent a user descends
**Intent → Blueprint route → Workflow → Run → task event** in one click. This is a
persistent `DescentRail` breadcrumb present on every screen along the ladder, never
a dead end — the load-bearing anti-"abstraction hides failure" mechanism.

**Power gradient (§6).** Progressive disclosure, never a ceiling: a team starts at
Intent, and every lower rung (Blueprint → Baseline → Workflow/Step) stays
first-class and reachable. Nav never greys out a lower rung a Principal is granted.

---

## 2. Screen catalog

Legend — **CoG** = charter center-of-gravity screen (§3.1); **Schema** = rendered
from a Contract/Facet JSON Schema, not hand-built (§3.1); **SSE** = live via
NATS-backed SSE (§3.1); **Descent** = a rung on the §1.8 ladder.

### Graph plane
| Screen | Purpose | Flags |
|---|---|---|
| **View Explorer** | Graph canvas over a saved View's live Entity set | CoG |
| **View Detail / Builder** | A versioned, CaC-declared View + its query + membership | Schema |
| **Entity Detail** | One Entity: Facets, Relations, per-attribute **Provenance** ("who wrote this, from which Run/Source") | Schema |
| **Source Detail** | An external system of record + its trust settings | — |

### Intent layer
| Screen | Purpose | Flags |
|---|---|---|
| **Intent Detail** | A declarative *what* by payload kind; form rendered from the Intent Contract | Schema · Descent |
| **Assignment Detail** | Binds an Intent to a View per ring; shows claim type (exclusive/additive) and any Gate | Schema · Descent |
| **Blueprint Detail** | The compiler surface: how (Intent × Assignment × View) **routes** by capability Facets into Baselines + remediation Workflows | Descent |
| **Baseline Detail** | Compiled/hand-written desired state: selector + expected Facet values + remediation Workflow ref + cadence | Schema · Descent |
| **Plan preview** (`stratt plan` in UI) | Compiled effect before merge — target Entities, mechanisms, changes — as a `PlanDiff` | Schema |

### Execution plane
| Screen | Purpose | Flags |
|---|---|---|
| **Workflow Detail** | Temporal-backed DAG of Steps with success/failure/always edges + Gates | Descent |
| **Run Stream** | Live Run: virtualized log viewer + host/task matrix, follow-tail, per-target results | CoG · SSE · Descent |
| **task-event Detail** | The floor of the descent: one task event, its output, and the failure detail | SSE · Descent |
| **Trigger Detail** | What starts a Run (Schedule, Emitter × CEL, manual, API/MCP) | Schema |

### Findings
| Screen | Purpose | Flags |
|---|---|---|
| **Findings Table** | Drift/compliance/orphan results: Entity + Baseline + observed-vs-expected + severity | CoG · Schema |
| **Finding Detail** | One diff + its immutable **Evidence** bundle (the audit/PCI unit) | Schema · Descent |

### Connectors, Fleet, Admin
| Screen | Purpose | Flags |
|---|---|---|
| **Connector Catalog** | Installable Connectors by trust tier (core/verified/community) | — |
| **Connector Detail** | A Connector's Syncers/Actions/Emitters + pinned, hash-verified schemas | Schema |
| **Actuator Detail** | An execution-engine plugin (`ansible`, `opentofu`, `script`, `helm`, `mcp`) | Schema |
| **Site / Bundle Detail** | Remote execution locus + signed pull Bundles | — |
| **Principal / CredentialRef** | Identity, grants (incl. `use-without-read`), cost/usage per identity | Schema |
| **Contract Registry** | Pinned Facet/Step schemas; drift is blocking | Schema |
| **Audit** | One audit stream across UI/CLI/CI/agent | — |

---

## 3. Component inventory (vendored, in-repo)

Headless Radix/Base-UI primitives with shadcn-style copy-in components **owned in
the repo** (§3.1) — no component library as a hard runtime dependency. All consume
[design tokens](design-tokens.md); none hardcode color/spacing.

**Schema-driven (the extensibility mechanism — plugins ship schemas, not React):**
| Component | Renders from | Feeds screens |
|---|---|---|
| `SchemaForm` | a Contract (JSON Schema + UI hints — the survey successor) | Intent, Assignment, Trigger, Step inputs |
| `SchemaTable` | a Facet/Finding schema | Findings, Entity Facets |
| `PlanDiff` | a compiled plan | Plan preview, Baseline, drift |

**Diagnosis & real-time (§1.8, §3.1):**
| Component | Role |
|---|---|
| `DescentRail` | the Intent→Blueprint→Workflow→Run→task-event breadcrumb, always live |
| `LiveLogViewer` | virtualized, SSE-fed, follow-tail, ANSI/severity-mapped |
| `HostTaskMatrix` | per-target × per-Step status grid for a Run |
| `WorkflowDAG` | live Workflow graph with Gate/branch state |
| `ProvenanceBadge` | shows which Run/Syncer/Source wrote an attribute (§ projections-never-a-second-truth) |
| `StateChip` | Run/Finding state as dot+icon+label (never color alone) |

**Graph & shell:**
| Component | Role |
|---|---|
| `GraphCanvas` | Entity/Relation exploration over a View |
| `EntityFacetPanel` | Facets + Provenance for one Entity |
| `CommandPalette` | keyboard-first navigation; parity with the `stratt` CLI verbs (§1.6) |
| `AppShell` | nav + `DescentRail` + theme toggle (light/dark, tokens-as-data) |

---

## 4. Two cross-cutting invariants (bind every screen)

1. **Diagnosis is never hidden (§1.8).** Every abstraction screen exposes the
   descent to the concrete Run and failing task event. No screen forces a user to
   leave the UI (SSH, raw log grep) to see *why* something failed.
2. **Schema-driven, not hand-built (§3.1).** Forms, tables, and diffs are generated
   from Contracts; a new Connector gets a working UI for free by shipping schemas,
   and community code never executes in the interface plane.

> Open item: the concrete "steal this / avoid this" UX patterns behind each screen
> land from the competitive teardown (`competitive-teardown.md`) and are codified as
> testable laws in the UX-principles ADR.
