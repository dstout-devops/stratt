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
| `hosts/` | (declared-estate Connector content) | **devices-as-code** — a file that a Syncer projects (not a writable CMDB); populated in the Estate-as-Code slice ([ADR-0056](../docs/adr/0056-estate-as-code.md)) |
| `authz/tuples.yaml` | — | grants (pointers only, §2.5) |

## The flagship: `linux-fleet` (the layered / CDK-style construct model)
"Onboard Linux servers from the simplest form, with defaults + optional overrides," realized as typed
declarative constructs (the useful half of AWS CDK — see ADR-0055):

- **`views/linux-fleet.yaml`** — the group (`kinds:[host], labels:{os:linux}`), landscape-neutral: a host counts
  whether it was projected from vSphere, OpenTofu, Crossplane, or the declared-estate file.
- **`intents/linux-baseline.yaml` + the shared `blueprints/fileset.yaml` + `assignments/linux-fleet-baseline.yaml`**
  — the "template Z" (L2 construct with defaults) bound to the group; the compiler drift-checks every member.
  The flagship REUSES the one `fileset` Blueprint (a namespace has a single Blueprint owner, §2.1; additive keys
  union within it), so the fleet's `sshd-config` key and web-files' `nginx-conf` key coexist in `fileset.content`.
- **`workflows/linux-onboard.yaml`** — the L3 onboarding lifecycle: `Gate → provision (Action) → configure
  (ansible)`. The provision Step's `action` is the **landscape binding** — `awsec2/create-vm` in dev, swappable
  for a `crossplane`/`opentofu`/`vsphere` Action without touching the rest of the estate. Provisioning is
  **gated** (§5 Flow 1 — never a silent auto-launch). Cert (certissuer) + app (helm) Steps are the next slice.

## The defaulted unit: `web-server` (G6 defaults + the materialization seam)
The onboarding template made concrete — "declare outcomes, not tool configs"
([ADR-0083](../docs/adr/0083-blueprint-route-materialization-seam.md), the G6 defaults/override merge):

- **`blueprints/web-server.yaml`** — an `Intent/Application` Blueprint carrying **`defaults: {port, channel}`**
  (the "sane defaults" base layer). Its route observes `app.config` and routes drift to a **gated** ansible
  remediation — the **plugin materializes** the state; core never learns "web server" or "ansible" (§1.4). The
  tool appears in exactly one place, the Blueprint route; there is **no side-by-side helm/ansible/chef config**.
- **`intents/web-server.yaml`** — the **simplest form**: `spec:{package:nginx}` and nothing else. It OMITS
  `port`, so it takes the Blueprint default (`8080`).
- **`intents/web-server-secure.yaml`** — the **optional override**: `spec:{package:nginx, port:"443"}`. The only
  thing written is what DIFFERS from the default; the override layers on explicitly (provenance records both
  layers — never a silent precedence rule, §2.4/§4.1).
- **`assignments/web-server.yaml` / `web-server-secure.yaml`** — bind each to a group (`web-hosts` / `secure-hosts`)
  via the SAME `web-server@1` Blueprint. Blueprint reuse across groups (§2.1); the compiler resolves each
  Assignment's spec = `merge(Blueprint defaults, Intent spec)` and drift-checks the group.

The Blueprint's `defaults` cross the composed kind's Contract at ingestion (§1.1 seam, partial-tolerant), so an
author-supplied default never reaches a Baseline unvalidated. Co-management fans out by ADDING routes (a cert
route, an app route) — the per-capability route map (ADR-0083 §3), each an independently-metered §7.6 channel.

## Environment slices ([ADR-0057](../docs/adr/0057-environment-scoped-reconciliation.md))
One estate tree, many logical slices. A daemon carries an active `STRATT_ENVIRONMENT`; a launching
declaration (Assignment · Trigger · Baseline) reconciles only where its `environments:` list contains that
value (untagged ⇒ every environment). Empty `STRATT_ENVIRONMENT` ⇒ unscoped (reconciles everything).

The four Triggers under `triggers/` are tagged `environments: [prod]` — each fires a Run needing a plugin or
target set the dev cell doesn't run (certissuer, real host fleets, the Salt event bus). So the **turnkey dev
stack** (`values-e2e`, `STRATT_ENVIRONMENT=dev`) reconciles the whole estate but launches **none** of them:
no cross-env schedule noise, and — data-layer-scoped — they are never prune targets either. Scoping is a
boolean membership filter, never precedence (§2.4); Views/Workflows are reached only through a scoped kind and
are never filtered. `task dev:stage-estate` stages this tree into the inline-declarations ConfigMap.

> `views/dev-hosts.yaml` + `views/dev-vms.yaml` and their `dev-runner` grants are the **plugin-e2e** target
> Views (the seeded synthetic host; the vcenter Syncer's vcsim VMs). Untagged ⇒ present in every slice.
