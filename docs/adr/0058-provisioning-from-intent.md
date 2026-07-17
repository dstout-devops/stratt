# ADR 0058 — Provisioning from Intent: declaring & building desired infrastructure

- **Status:** Accepted
- **Date:** 2026-07-17
- **Deciders:** steward (dstout), charter-guardian
- **Charter sections:** §1.1, §1.2, §1.4, §1.5, §2, §2.1, §2.4, §4.3, §5, §8
- **Frames under:** [ADR-0055](0055-estate-composition.md) (Estate Composition) — realizes gaps **G1** (no
  provisioning Intent) and **G4** (the compiler produces checks, not builds), with **G2** (cardinality) as
  the typed-count → fan-out pattern.

## Context

Today the estate can **observe and configure** what exists, but it cannot **declare into existence**. The
Intent→Blueprint compiler (`core/internal/compiler`) is a pure read-model transform: it emits facet-observation
Baselines and *references* to gated remediation Workflows — it never generates or launches a build
(`compiler.go` imports no orchestrate path; the orphan route even comments "a ref only… never auto-run, §5").
The build primitives already exist and are proven end-to-end: the `awsec2/create-vm` **Action** (creates one
instance, returns typed `{instanceId}` outputs **and** projects the Entity back with Run provenance), the
`opentofu` **Actuator** (plan-pinned Gate + `stratt_entities` write-back, ADR-0016/0031/0047), the
`Gate → Action → Actuator` Workflow engine with `{{.steps.x.outputs.y}}` chaining, and `linux-onboard.yaml`
as the hand-written `Gate → provision → configure` template.

The missing piece is the **declarative front**: "I want three Linux web servers on this network" has no
home. Writing it as a hand-authored Workflow per fleet doesn't scale and isn't reconciled — nothing detects
that only one of the three exists and offers to build the other two.

The charter makes this **deliberately hard**, and rightly:
- **§1.2 — the graph is a projection.** Desired-but-not-yet-built infrastructure **cannot be an Entity** —
  that is the writable-CMDB / phantom-host anti-pattern. Desired existence must live in Git (CaC).
- **§5 Flow 1 — no silent auto-launch.** A reconcile that notices "two servers missing" must **surface a
  gated build**, never apply one. "Declare and it appears" resolves to a human-approved Workflow.

This ADR defines the **one seam** through which all of it flows — compute now; VMware/Crossplane/DNS/network
later as Actuators behind the same seam. It is the G1/G4 realization ADR-0055 said must have its own
guardian-reviewed decision before any code.

## Decision

**1. Desired infrastructure is a CaC Intent, never an Entity (§1.2).** A **provisioning Intent** — first kind
`Intent/Compute` — declares desired infrastructure and lives only in Git. The graph continues to hold **only
what has been built** (projected with Run/Normalizer provenance). There is no phantom Entity for an unbuilt
server, and no create-device API. The Intent is a typed, schema-pinned Contract (§1.1) — a *named* provisioning
seam, never a universal infrastructure ontology.

```yaml
# estate/intents/web-fleet.yaml  — Intent/Compute (illustrative)
name: web-fleet
kind: Compute
spec:
  count: 3                        # cardinality (G2) — a typed field, NOT a config loop
  namePrefix: web                 # → web-01, web-02, web-03: stable, idempotent identities
  projectKind: host               # what the built infra projects AS (Views select this) — decision 6
  labels: { os: linux, role: web }
  builder: awsec2/create-vm       # the build Action/Actuator ref; the swap point → opentofu /
                                   # vsphere / crossplane / … (decision 3)
  params:                         # OPAQUE pass-through, validated against the BUILDER's own input
    region: us-east-1             # Contract (§1.5) — Intent/Compute's schema never types these fields
    ami: ami-linux
    instanceType: t3.small
```

The `Intent/Compute` schema keeps `params` an **opaque object** (S1): it is validated only against the
builder's pinned input Contract downstream, and the Intent schema **never accretes per-landscape fields** —
that would grow it into the per-provider ontology §1.1 forbids. `builder` names an Action/Actuator ref, not a
"provider" (§2). Compute builds via **cloud/hypervisor Actuators only** — OS imaging and bare-metal
provisioning remain permanent non-goals (S2); this seam never grows a PXE/metal path.

**2. The reconcile SURFACES a gated build; it never builds (§5 Flow 1) — reusing the existing
Finding → gated-Workflow pattern, with NO phantom subject (§1.2).** A provisioning-aware reconcile
**recomputes** each pass — Git `count` minus the count of **projected** Entities correlated to the Intent
(decision 5) — and on a shortfall raises a **provisioning Finding** carrying a **gated build Workflow
reference**, exactly the §5 Flow 1 shape the compiler already uses for remediation. **The Finding is keyed to
the `Intent` (the CaC desired object), NOT to a not-yet-built instance** (M2): there is **no placeholder /
"pending" `web-02` Entity**, no desired-count row, nothing about the unbuilt written to the graph — the
shortfall is a *derived, transient* comparison, recomputed from Git + projections every reconcile, never
persisted as desired state (the phantom-host anti-pattern §1.2 forbids). It **never launches**. A human
approves the Gate → the Workflow runs the build Action once per missing instance → each projects back → the
next reconcile recomputes and converges. The facet-observation compiler is **untouched**: provisioning is a
*sibling* reconcile path (`core/internal/provision`, tentative), not an overload of facet-observation — an
Intent/Compute produces a build shortfall, never an observation Baseline.

**3. Landscape-agnostic by the Actuator swap (§1.4 / §1.5).** `spec.builder` names the build Action/Actuator:
`awsec2/create-vm` today, `opentofu` today, `vsphere`/`crossplane`/`dns` as future plugins behind the sovereign
port. **Re-targeting a fleet from EC2 to vSphere is a one-line swap of `builder:`**, not an estate rewrite —
the Intent, the View, the configure Steps, and the Baselines are all landscape-neutral. Core never learns any
provider; every build crosses the port and is governed hub-side. The build's `params` are validated against
**that builder's** pinned input Contract, so a bad param fails at plan time, per landscape.

**4. Cardinality is a typed count → reconcile fan-out, gated by §4.3 max-delta (§1 permanent non-goal, G2).**
`spec.count: 3` fans out into three independently-named, independently-gated build units — deterministic
expansion in Go, **never** a loop/conditional/expression in YAML. Raising the count to 5 surfaces two new
gated builds; lowering it does **not** destroy anything (decommission is a separate, gated Destroy verb, §2.4)
— it raises an over-provision Finding for review. **Fan-out is a membership-delta explosion (count 3→50 = 47
builds; a `namePrefix` change churns the whole fleet), so it carries the mandatory §4.3 max-delta gate** that
ADR-0055's own review made non-negotiable for this blast radius (§5 Flow 1 + §2.3 Gate + §4.3 max-delta): a
count/identity delta beyond the configured fraction **pauses the whole batch pending explicit approval** — not
merely a `log()` line, and never a silent cap. The per-instance Gate protects each build; the max-delta gate
protects against a fleet-wide churn from a one-line edit.

**5. Idempotent correlation by stable name, exclusively claimed (§2.4 — no last-writer-wins).** Each desired
instance gets a deterministic identity from `namePrefix` + ordinal (`web-01`…). The reconcile correlates
desired ↔ built by that name (a `stratt.intent/instance: web-01` label carried on the built Entity's
projection), so re-reconciling a converged fleet builds nothing, a failed build re-surfaces **the same** gated
unit (not a duplicate), and two reconciles never race to double-build. Correlation is an explicit key match —
never a heuristic or a precedence tiebreak. **Instance identity is an exclusive claim in the ownership registry
(M3):** two `Intent/Compute` deriving the same `stratt.intent/instance` value (e.g. both `namePrefix: web` →
`web-01`) is a **compile error** (the §2.1/§2.4 exclusive-claim rule, surfaced at plan time like
`ErrOwnerConflict`), never resolved by first-builder-wins or last-writer-wins.

**6. The Intent declares the projected kind + labels; the projection is written ONLY by a Run/Normalizer path
(§1.2).** A build must land as an Entity the fleet's Views already select. `spec.projectKind` + `spec.labels`
are the contract the build's projection must satisfy. **The reconcile never writes the Entity itself** (M1 —
that would be a third write path §1.2 forbids, blocked in the data layer by `enforce_write_path`): the kind +
labels + `stratt.intent/instance` correlation label are carried on the build Action's **output projection (Run
provenance)** — or, where the builder can't shape its own output, by a **declared relabel Normalizer** — never
by a reconcile-side write. So an EC2 instance built for `web-fleet` projects as
`kind: host, labels:{os: linux, role: web}` and is selected by `linux-fleet`, with no per-landscape kind
divergence leaking into Views and no reconcile write path. (Landscape-native identity — `aws.instanceId`,
`vsphere.uuid` — is still projected by the Syncer for enrichment; `projectKind`/labels are the *selection*
seam.)

**Scope — compute first; network/topology deferred to ADR-0059.** This ADR ships the seam and `Intent/Compute`,
proven on the in-repo `awsec2/create-vm` + `opentofu` Actuators (moto/tofu, no new plugin). **Subnet, VLAN, DNS,
availability-zone, and DMZ are genuinely new typed constructs** and get their **own** ADR (0059): they follow
the *same* seam (declare desired → gated build → project back) but additionally need a **placement model** — a
host/service "in" a subnet/AZ/DMZ is a typed **Relation**, and topology must be modeled without a universal
network ontology (§1.1: each is a named Facet/Relation a shipping Contract demands, never a schema-of-networks).
The `vsphere` / `crossplane` / `dns` build Actuators are each their own plugin ADR. This ADR makes all of them
**slot-in**, not rewrites.

## Charter alignment

- **§1.2 — projection purity.** Desired existence lives in the Intent (Git); only built infra becomes an
  Entity, via the existing Run/Normalizer write paths (enforced in the data layer, `enforce_write_path`). No
  phantom, no writable CMDB, graph stays rebuildable.
- **§5 Flow 1 + §4.3 — no auto-launch, blast-radius gated.** Provisioning reuses the Finding →
  gated-Workflow-ref pattern verbatim; the build is human-approved. The reconcile computes and surfaces; it
  never applies. Fan-out beyond the configured max-delta fraction pauses the batch pending approval (§4.3), so
  a one-line `count`/`namePrefix` edit can never churn a fleet unreviewed.
- **§2.4 — no implicit precedence.** Desired↔built correlation is an explicit stable-name key match; count
  changes never silently destroy; no last-writer-wins field is introduced.
- **§1.1 — type the seams, not the world.** `Intent/Compute` is one pinned schema demanded by this shipping
  seam; it is not a generic "server" model, and network kinds are deferred precisely so they arrive as
  demanded typed constructs, not a speculative ontology.
- **§1.4 / §1.5 — boring spine, sovereign port.** Every landscape is an Actuator plugin over the port; the
  build `params` validate against the builder's pinned Contract; core learns no provider.

## Consequences

- **Positive:** "declare N servers → gated build → converge" exists, landscape-neutral; the whole
  spin-up-services frontier (VMware, Crossplane, DNS, networks) becomes incremental Actuators + typed Intent
  kinds behind one seam; the reconcile finally *closes the loop* it already half-owns (it configures and
  drift-checks fleets it can now also grow).
- **Negative / trade-offs:** the reconcile gains a **new state comparison** (desired count vs built count) — the
  central new hazard, mitigated by stable-name correlation + the §5 gate (a mis-count surfaces a reviewable
  Finding, never a wrong build). Partial/failed builds, count-down over-provision, and the relabel Normalizer
  are real edges called out for the build. Provisioning latency + external-provider failure live outside the
  deterministic core (the Workflow/Actuator plane already owns that).
- **Follow-ups:** ADR-0059 network/topology primitives + placement Relations; the `vsphere`/`crossplane`/`dns`
  build-Actuator plugin ADRs; G6 defaults/override so an `Intent/Compute` is a few lines; the Destroy/decommission
  verb for count-down (gated, §2.4); the `stratt` CLI `plan` showing provisioning blast-radius.

## Alternatives considered

- **Phantom desired-Entities** (write the three servers into the graph as "desired", reconcile to real) —
  rejected: the writable-CMDB / second-truth anti-pattern §1.2 forbids; the graph must never hold the
  unbuilt.
- **Auto-launch the build on shortfall** — rejected under §5 Flow 1; builds are gated, always.
- **A cardinality/expression language in the Intent** (`for_each`, `count.index` interpolation à la Terraform)
  — rejected as a new configuration language (permanent non-goal); cardinality is a typed field expanded by
  the reconcile.
- **Overload the facet-observation compiler to emit builds** — rejected: it would entangle "observe drift on
  existing members" with "grow the member set", two different reconcile shapes; provisioning is a sibling path
  so the observation compiler stays a pure read-model transform.
- **A Kubernetes CRD / Cluster-API Machine per instance** — deferred to the charter's Phase-4 CRD interface;
  CaC-over-Git is primary so the estate stays portable off any single substrate (§7.1).

## Reviews

- **charter-guardian (2026-07-17): SOUND-WITH-CHANGES → folded.** The projection-purity and no-auto-launch
  axioms survive at the design level (desired existence stays in Git as `Intent/Compute`; only built infra
  becomes an Entity; provisioning reuses the Finding → gated-Workflow-ref pattern; the §1.8 descent chain is
  preserved). **Must-fixes (all folded):**
  - **M1 (§1.2 third write path):** decision 6 said "the reconcile stamps [kind/labels] onto the built Entity"
    — a write path only Normalizers/Run provenance may take. Reworded: kind/labels/correlation-label are
    carried on the build Action's **output projection (Run provenance)** or a **declared relabel Normalizer**,
    never a reconcile-side write (`enforce_write_path` blocks it in the data layer).
  - **M2 (§1.2 phantom subject):** the provisioning Finding is now explicitly keyed to the **Intent** (the CaC
    desired object), Entity-less; **no placeholder/"pending" `web-02` Entity** and no desired-count row are
    ever written — the shortfall is a derived, transient comparison recomputed from Git + projections each
    reconcile, never persisted.
  - **M3 (§2.4 cross-Intent name collision):** two `Intent/Compute` deriving the same `stratt.intent/instance`
    identity is now a **compile error** (exclusive claim in the ownership registry, §2.1/§2.4), never a
    first/last-writer-wins.
  - **M4 (§4.3 max-delta gate):** restored the mandatory §4.3 blast-radius gate ADR-0055's review already
    required for fan-out — a count/identity delta beyond the fraction **pauses pending approval**, not a
    `log()` line.
  - **Flags folded:** S1 — `Intent/Compute`'s schema keeps `params` an opaque pass-through, never accreting
    per-landscape fields (§1.1); S2 — compute builds via cloud/hypervisor Actuators only, OS imaging/bare-metal
    stays a permanent non-goal; S3 — renamed `spec.landscape` → `spec.builder` (an Action/Actuator ref, not a
    pseudo-Named-Kind / `provider` synonym) and `spec.template` → `spec.params` (avoids the banned
    `job template`). `kind: Compute` + `stratt.intent/instance` to be run through `vocabulary-linter` before
    the type name freezes in code.
