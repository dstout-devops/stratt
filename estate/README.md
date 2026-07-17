# `estate/` — the reconciled estate-as-code

The single Git tree Stratt reconciles as **desired state** (`STRATT_DESIRED_STATE_PATH`). One directory per
declarable kind — the layout `desiredstate.ParseDir` expects — plus `authz/tuples.yaml`. This consolidates what
used to live inert under `deploy/dev/examples/**` (reconciled by nothing) into one live, planned artifact.

**This is composition, not a configuration language** ([ADR-0055](../docs/adr/0055-estate-composition.md)). You
declare typed primitives; the reconcile + Intent compiler + orchestrator fan them out through **plugins** over
the sovereign port. Nothing here models "server" or "network" generically (§1.1 — type the seams, not the
world), and there are no loops/conditionals/expressions (§1 — no new config language).

## Layout
| dir | kind | role |
|---|---|---|
| `views/` | View | **groups** (label/kind/facet selectors) |
| `intents/` `blueprints/` `assignments/` | Intent · Blueprint · Assignment | the **template layer** — a Blueprint is a template with defaults, an Assignment binds it to a group; the compiler emits drift-checked Baselines |
| `workflows/` | Workflow | **lifecycle DAGs** (Gate → Actuator Steps) composing plugins |
| `triggers/` `emitters/` | Trigger · Emitter | event/schedule → launch |
| `hosts/` | (static-inventory Connector content) | **devices-as-code** — a file that a Syncer projects (not a writable CMDB); populated in the Estate-as-Code slice ([ADR-0056](../docs/adr/0056-estate-as-code.md)) |
| `authz/tuples.yaml` | — | grants (pointers only, §2.5) |

## The flagship: `linux-fleet` (the layered / CDK-style construct model)
"Onboard Linux servers from the simplest form, with defaults + optional overrides," realized as typed
declarative constructs (the useful half of AWS CDK — see ADR-0055):

- **`views/linux-fleet.yaml`** — the group (`kinds:[host], labels:{os:linux}`), landscape-neutral: a host counts
  whether it was projected from vSphere, OpenTofu, Crossplane, or the static-inventory file.
- **`intents/linux-baseline.yaml` + `blueprints/linux-baseline.yaml` + `assignments/linux-fleet-baseline.yaml`**
  — the "template Z" (L2 construct with defaults) bound to the group; the compiler drift-checks every member.
- **`workflows/linux-onboard.yaml`** — the L3 onboarding lifecycle: `Gate → provision (Action) → configure
  (ansible)`. The provision Step's `action` is the **landscape binding** — `awsec2/create-vm` in dev, swappable
  for a `crossplane`/`opentofu`/`vsphere` Action without touching the rest of the estate. Provisioning is
  **gated** (§5 Flow 1 — never a silent auto-launch). Cert (certissuer) + app (helm) Steps are the next slice.
