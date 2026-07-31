# ADR 0123 — Keyed, placement-aware spread: identity survives a zone-list edit, and declared placement finally reaches the build

- **Status:** **Proposed** (2026-07-25, steward). Charter review done **by hand** (this session's rules bar
  the subagent); the §1.1/§2/§2.4/§4.3 checks are answered inline under _Charter alignment_, including the
  contestable one (D2 changes an ADR-0059 decision).
- **Date:** 2026-07-25
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams), §1.2 (projections, never a second truth), §1.4 (content-blind
  spine), §1.5 (opaque provider params), §1.8 (never hide diagnosis, fail as early as possible), §2 (frozen
  vocabulary), §2.4 (no implicit precedence), §4.3 (blast-radius gating), §5 Flow 1 (a build is gated)

## Context

ADR-0118 booked this as "keyed, placement-aware spread": `zones × perZone` expanded as typed fields with
**keyed** identity (`web-use1a-01`) instead of ADR-0058's positional ordinal. The reason was Terraform's own
`for_each`-over-`count` guidance — keyed instances survive a reordering, positional ones are destroyed and
recreated.

Implementing ADR-0120 turned that from a nice-to-have into the head of a queue, because it surfaced two
defects that are really the same defect, and this ADR owns both:

1. **`placement` is declared by all seven build Workflows and bound by NONE of them.** `BuildLaunchParams`
   derives it faithfully; every advertised builder ignores it. `estate/intents/app-tier.yaml` declares
   `placement.subnet: app-subnet` and is the only Intent in the estate that does — so **its declared
   placement currently reaches nothing.** The one Workflow that would project the `placed-in` edge,
   `app-tier-build.yaml`, is unreachable: a provider advertises exactly one builder per kind
   (`provisions: {Compute: …}`), and that is `compute-build`.

2. **It cannot be fixed by folding `app-tier-build` into the shared builder**, and the reason is structural:
   `BuildLaunchParams` **omits `placement` entirely** when an Intent declares none, the substituter fails
   closed on an unknown field, and ADR-0083 D5 rules out conditionals. So `{{.launch.placement.subnet}}` in
   the shared builder would break every unplaced Compute Intent — `web-fleet` today.

Underneath both sits a gap in the parameter plane itself: `additionalProperties: false` **forces** a builder
to declare every param the reconcile might send, so an input that is accepted and silently dropped is
structurally indistinguishable from one that is consumed. Nothing at declaration, compile, launch or
dispatch says a word. That is a §1.8 failure in the plane, not in any one Workflow, and it is why defect 1
survived review: `compute-build` declaring `placement` _looks_ like `compute-build` using it.

Zones make this urgent rather than merely untidy: a keyed spread's whole point is that the zone reaches the
provider, and a plane that cannot deliver `placement` cannot deliver a zone either.

### What already ships that this must reconcile with

| Machinery                                                            | Where                                  | Bearing                                                               |
| -------------------------------------------------------------------- | -------------------------------------- | --------------------------------------------------------------------- |
| `count` + `namePrefix` + zero-padded ordinal; `desired`/`Excess`     | ADR-0058; `provision.go`               | The positional expansion this extends without replacing               |
| Max-delta batch pause on a missing-count spike                       | ADR-0058 D4; §4.3                      | A zone-list edit is exactly the spike it exists to catch              |
| `stratt.intent/instance` correlation label                           | ADR-0058 M1; ADR-0120 D2               | Keyed names must stay correlatable by the same mechanism              |
| `Placement{Subnet,Dmz,AvailabilityZone}`, emitted only when declared | ADR-0059 D3/D5                         | **D2 changes this** — the omission is what makes placement unbindable |
| Placement-drift Findings (declared vs observed)                      | ADR-0059 S5; `reconcilePlacementDrift` | Already keyed off declared placement; keyed spread feeds it more      |
| Typed-field expansion in Go, never a YAML loop                       | ADR-0083 D5                            | Binds D1: `zones` is a typed field, not a `for_each`                  |
| Declaration-time check over every advertised builder                 | ADR-0120 D3; ADR-0114 (teardowns)      | Where D3's new rule lands, beside the checks that already run there   |

## Decision

### D1 — `zones` × `perZone`, with keyed identity, exclusive with `count`

`Intent/Compute` gains `zones: [use1a, use1b]` and `perZone: 2` (schema `compute.v4`, a sibling version —
`count` is unchanged and every existing Intent keeps parsing). Instance identity becomes
**`<namePrefix>-<zone>-<ordinal>`** (`web-use1a-01`), with the ordinal scoped **within** its zone.

That is the entire point. Under ADR-0058's positional scheme, `zones: [a, b]` → `[a, b, c]` renumbers
everything after the insertion point and every instance is destroyed and recreated — the fleet-wide churn
ADR-0058 D4 itself flags. Keyed, adding a zone adds `web-use1c-01..02` and **touches nothing else**.

**`count` and `zones` are exclusive, and a declaration carrying both fails at parse.** Not a precedence rule
(§2.4): they are two spellings of cardinality, and picking a winner in Go is what ADR-0083 D5 forbade.
`perZone` without `zones`, or `zones` without `perZone`, likewise fail — a half-declaration that expands to
nothing is the port-with-no-address shape ADR-0117 D5a refused.

**Switching an existing fleet from `count` to `zones` renames every instance**, so it is not a migration this
can perform silently: the old names stop being desired and the new ones do not exist, which the reconcile
correctly reads as "tear down N, build N". That is real and it is gated — ADR-0058 D4's max-delta pauses the
batch, and ADR-0114's decommission path surfaces the teardowns as gated Findings rather than doing them. The
consequence is stated here so nobody discovers it from a plan diff.

### D2 — `placement` is emitted COMPLETE, and ADR-0059 D3's omission is withdrawn

`BuildLaunchParams` now always emits `placement` with all three fields, empty when undeclared, instead of
omitting the key and the undeclared fields. A keyed instance's `availabilityZone` is filled from its own zone
key, so a zone reaches the provider by the same route a subnet does.

**This changes an ADR-0059 decision, so here is the argument.** D3 emitted only declared fields "so a build
Workflow can tell 'no zone declared' from 'zone is empty'". That distinction is **unusable by any consumer
that exists**: template substitution has no conditionals (ADR-0083 D5), so a builder cannot branch on
presence — it can only bind a field or not. What the omission actually bought was the opposite of its intent:
it made `{{.launch.placement.*}}` unsafe in any shared builder, which is why the one Workflow that binds
placement is the one nothing routes to. A distinction no consumer can act on, that breaks the consumers that
exist, is not a feature.

Empty-string-means-undeclared is legible to the thing that actually decides: the provider's own Action
Contract, which validates its params downstream (§1.5) and can require what it needs.

**2026-07-30 — D2 now covers `params` too, and it should have from the start.** `BuildLaunchParams` and
`SingletonLaunchParams` emitted `params` only `if len(Spec.Params) > 0`: the exact omit-when-undeclared shape
this decision withdrew one field over, on an argument that never mentioned placement specifically. It survived
because every provisioning Intent in the estate happened to declare params, so no builder ever met the
vanished key. That stopped the moment one legitimately did not — `web-fleet` on the kubernetes substrate,
whose provider reads no build params at all — and the estate was refused with _"kubecompute-build declares
input `params`, which the provisioning reconcile never supplies"_: true of that one Intent, false of the
builder, and blaming the wrong document. `params` is now present-and-empty like `placement`.

`app-tier-build.yaml` is therefore **deleted**. It was kept through ADR-0120 as the only worked example of
the placed-in projection; with `compute-build` binding placement, the example is live rather than
illustrative, and keeping an unreachable duplicate would leave two Workflows that build a Compute with only
one reachable — the `subnet-provision` shape ADR-0120 removed for the same reason.

### D3 — An advertised builder must BIND every input it declares

The check that makes the accepted-but-dropped class unshippable, and the mirror of the rule already there.
ADR-0120 D3 refuses a builder that does **not declare** an input the reconcile supplies; this refuses one
that **declares an input no Step binds**. Together they make a builder's declared interface exactly what it
consumes.

Without it, D2 is one careless edit from regressing to the state this ADR exists to fix — and it would
regress silently, because `additionalProperties: false` makes the declaration mandatory whether or not it is
used. Checked at declaration, in Git review, which is the earliest rung §1.8 offers.

Scoped to **advertised** build and teardown Workflows, deliberately: an ordinary Workflow may reasonably
declare an input a human supplies for one Step and not another, and the reconcile is not the one filling it.
The rule bites where core is the caller and therefore knows the whole contract.

## Charter alignment

- **§1.1.** `zones`/`perZone` are typed fields on a kind that already exists; no new Kind, no whole-Entity
  schema.
- **§1.2.** Nothing here writes an Entity: the reconcile still surfaces gated Findings and the build
  projects (ADR-0058 M1 unchanged).
- **§1.4/§1.5.** `params` stays opaque; placement is a core-typed field the provider's Workflow maps into
  its own params, as every launch value already is.
- **§2.** No new Named Kind; keyed names use the same `stratt.intent/instance` correlation label.
- **§2.4.** `count` XOR `zones` fails the parse rather than resolving by precedence.
- **§4.3.** A zone-list edit is gated by the max-delta pause that already exists, not by a new mechanism.
- **§5 Flow 1.** Every derived instance still surfaces as a **gated** build.

## Consequences

- **Positive.** Per-zone spread with identity that survives a zone-list edit — the shape a real fleet needs.
  Closes the inert-`placement` defect: a declared placement now reaches the provider, and the placement-drift
  Finding (ADR-0059 S5) finally has a build path that honours what it compares against. D3 closes the
  accepted-but-dropped gap in the parameter plane generally, not just for placement.
- **Negative / trade-offs.** `count` → `zones` renames a fleet; that is inherent to keyed identity and is
  gated rather than prevented. Placement is now always present, so a builder that wants "was it declared?"
  must ask "is it empty?" — a real if small loss of fidelity, taken because the fidelity was unusable.
  D3 makes a declared-but-unbound input a parse error, so a builder cannot carry an input for a future Step.
- **No follow-ups are booked.**

## Alternatives considered

- **Keep positional identity and record the zone as a label.** Rejected: the label does not change identity,
  so a zone-list edit still renumbers and recreates the fleet — the exact failure this exists to prevent.
- **Emit `placement` only when declared and let builders opt in.** That is today, and it is why placement
  reaches nothing.
- **A per-shape builder (`app-tier-build`) selected by the reconcile.** Rejected: a provider advertises one
  builder per kind, and selecting among several by the Intent's shape is a precedence rule core would have to
  invent (§2.4).
- **`for_each` over a zone list in YAML.** Rejected by ADR-0083 D5 and charter §1 — no new configuration
  language. `zones` is a typed field expanded in Go.
