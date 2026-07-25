# ADR 0121 — `TaskEvent.scope`: an event says whether it describes the Run or a task

- **Status:** **Proposed** (2026-07-25, steward)
- **Date:** 2026-07-25
- **Deciders:** steward
- **Charter sections:** §1.4 (boring spine, pluggable everything — the content-blind spine), §1.5
  (sovereign contracts, pinned transports), §1.6 (one capability, every surface), §1.8 (never hide
  diagnosis), §2.4 (no implicit precedence, no two fields that can disagree)

## Context

ADR-0117 follow-up **(j)** asked for `kind=ee-content` to be surfaced in the UI's Run descent, and was
**deliberately not done as written** — with a note that is this ADR's whole premise:

> The UI cannot pin `ee-content` as run metadata without hardcoding one plugin's kind vocabulary in the
> interface plane, which is exactly the `if ansible{}` §1.4 forbids. … Pinning run-scoped metadata
> properly needs a **generic marker on the port** distinguishing "this event describes the Run" from
> "this event describes a task" (a `scope` on `TaskEvent`, spine-blind to which plugin sent it). That is
> a port change, so it belongs in its own ADR.

What (j) shipped instead was the content-blind half that carried the actual §1.8 value: `live-log.tsx`
counts the Run's warned/failed events and can filter to them, reading `level` and never a kind. That
works because ADR-0117 (g) had just made `level` a **spine-level, content-blind** property of an event.
This ADR adds the second such property, and the argument is the same one.

**Why a marker is needed at all.** `RunEvent.kind` is open by design — `openapi.yaml` says "tool-shaped
and deliberately open — the spine does not enumerate what a plugin may emit (§1.4)". So a consumer that
wants to show "this Run ran EE image X with collection Y at version Z" has exactly two options today:

1. Match on `kind == "ee-content"`, which puts one plugin's vocabulary in the interface plane. Every
   other plugin then needs its own case, and the spine's content-blindness becomes a convention the UI
   quietly breaks.
2. Show nothing, which is what happens now: the `ee-content` event exists, is correct, carries a real
   statement, and is one line in a stream of fifty thousand — findable only by scrolling.

Neither is acceptable, and the second is a §1.8 failure of exactly the kind ADR-0117 was written to
close: the mechanism works and the diagnosis does not arrive.

### What already ships that this must reconcile with

| Machinery                                                                      | Where                                 | Bearing on this decision                                                                            |
| ------------------------------------------------------------------------------ | ------------------------------------- | --------------------------------------------------------------------------------------------------- |
| `TaskEvent` as the **typed, core-legible** descent unit (port invariant #12)   | `proto/stratt/plugin/v1/plugin.proto` | The precedent that diagnostics ride a typed channel while desired state stays opaque                |
| `TaskEvent.Level` → `RunEvent.Level` → OpenAPI → UI                            | ADR-0117 (g); `dispatch.go:387`       | The **exact** path and the exact argument; this is its sibling                                      |
| `LEVEL_UNSPECIFIED` maps to **empty, not `info`**                              | ADR-0117 (g)                          | §1.8 rule this must obey: an absent signal must not read as a benign one                            |
| `RunEvent.Target` — the Entity an event applies to "when per-target"           | `types/run.go`; ADR-0054              | **A per-target scope member would be a second field saying the same thing** — see D2                |
| `RunEvent.Slice` — the target-set slice; part of event identity                | ADR-0054                              | Orthogonal: slices partition targets, not descriptive levels                                        |
| One TaskEvent→RunEvent conversion site                                         | `dispatch.go:383-392`                 | The whole plumbing cost is one struct literal and one mapper                                        |
| SSE tail built from the **published** schema, with a field-by-field drift test | ADR-0091; ADR-0117 (g)                | A new field must be added to `openapi.yaml` or it is served and invisible to every generated client |
| Uncapped event stream, never truncated                                         | ADR-0003 L1/L2                        | Why "just scroll to it" is not the answer: the stream is deliberately huge                          |

**Nothing in the corpus already decides this.** ADR-0047 sets the port's surface, ADR-0117 (g) added the
one adjacent field, and ADR-0054 owns per-target/slice structure. This is additive to all three and
contradicts none.

## Decision

### D1 — `TaskEvent.scope`: a closed, spine-owned enum with two stated members

```proto
enum Scope {
  SCOPE_UNSPECIFIED = 0;
  SCOPE_RUN = 1;   // this event describes the RUN as a whole
  SCOPE_TASK = 2;  // this event describes one unit of work within the Run
}
Scope scope = 9;
```

**Spine-owned and closed**, exactly like `Level`: this is a property every tool means identically, which
is the only kind of thing the content-blind spine may read off an event (§1.4). A plugin cannot add a
member; a plugin that needs to say something tool-shaped says it in `fields`, which is what `fields` is
for.

**`SCOPE_UNSPECIFIED` maps to empty, never to `task`.** ADR-0117 (g) had to learn this for `Level` and
the reason transfers unchanged: most of the stream predates the field, so defaulting unspecified to
`task` would assert that no plugin has ever emitted run-scoped output — a confident claim built from
missing data. A consumer reads empty as "the producer did not say", and shows the event in the stream as
it does today.

**Two members, not three, and `bool run_scoped` was rejected.** A bool cannot distinguish "this is a task
event" from "this producer does not state scope", and that distinction is the difference between "this
plugin emits no Run metadata" and "we cannot tell" — a §1.8 distinction, the same one the `Level` empty
case exists to preserve. An enum also extends (a future `SCOPE_STEP`) without a second boolean whose
relationship to the first has to be explained.

### D2 — No per-target member: `Target` already answers that, and two fields that can disagree is the bug

The obvious third member is `SCOPE_TARGET`, and it must not exist. `RunEvent.Target` is already "the
Entity this event applies to, when per-target" — so a `SCOPE_TARGET` event with an empty `Target`, or a
`SCOPE_TASK` event with a `Target` set, would be two fields making contradictory claims with nothing to
say which wins.

That is precisely the defect ADR-0120 D1/V5 caught between `Finding.Framework` and `launchKind`: shipping
both without stating which is authoritative gives two discriminators resolved by whichever branch runs
first. §2.4 forbids the implicit precedence that resolves it. So: **`Target` says which Entity, `scope`
says which descriptive level, and the two never overlap.** A per-target event is `SCOPE_TASK` with a
`Target`.

### D3 — Carried to every surface, or it is not a capability (§1.6)

`scope` rides the same path `level` does, and every rung is load-bearing:

- `dispatch.go` maps `TaskEvent.Scope` → `RunEvent.Scope` at the single conversion site — now a named
  `runEventFromTaskEvent`, not a struct literal inside the follow loop. **That change was forced by
  falsification, and it matters beyond this field.** Deleting the `Scope:` mapping from the literal broke
  no test: the mapper had a unit test and the CALL had none, which is the inert-mechanism shape this repo
  keeps finding. `TaskEvent.Level` has had the same gap since ADR-0117 (g) — its mapper is tested, its
  wiring was not. With the conversion in one function, one test covers every field that actually reaches
  the operator, and both fields are now falsifiable. The pod-start diagnosis event was split out for the
  same reason: every dispatcher in `podstart_test.go` is built with a nil bus, so an assertion about that
  event's contents had nowhere to live.
- `types.RunEvent` gains `Scope string`, with the `RunEventScope*` constants beside the level ones.
- `core/api/openapi.yaml` declares it on the `RunEvent` schema. **Not optional bookkeeping**: the SSE tail
  is marshalled through a generated wire type, so a field absent from the spec is served by the server and
  invisible to every generated client — the exact trap ADR-0117 (g) hit and closed with a drift test. That
  test extends to the new field by construction.
- The UI can then pin run-scoped events as Run metadata **while matching on nothing tool-shaped**.

### D4 — The spine stamps its own events, and `ee-content` becomes the first `SCOPE_RUN` producer

A marker no producer sets is an inert mechanism, and this repo has shipped several. So this decision
includes its first consumers and producers, not just the field:

- **`stratt-ansible`** marks its `kind=ee-content` event `SCOPE_RUN` — it describes the image the whole
  Run executed in, which is the statement (j) wanted pinned.
- **The spine's own run-level events** are stamped where they are already emitted: `pod-start-blocked` /
  `pod-start-failed` (ADR-0117 l — a pod that never started is a fact about the Run, not about a task),
  `governance-rejected`, and the bundle refusal.
- `diagnostic-output` is deliberately **left unspecified**: it is a ring buffer of lines the plugin never
  claimed, so the spine does not know what they describe, and guessing would be the invention this ADR
  exists to avoid.

## Charter alignment

- **§1.4 (content-blind spine).** The point of the whole change: the interface plane stops needing a case
  per plugin kind. The spine reads `scope`, never `kind`.
- **§1.5 (sovereign contracts).** An additive field on a pinned proto, new field number, no wire break; the
  transport stays beneath our own contract.
- **§1.6 (every surface).** Declared in `openapi.yaml`, so UI, CLI and MCP see it identically.
- **§1.8 (never hide diagnosis).** Unspecified never reads as a benign value; run-level facts become
  findable in an uncapped stream instead of technically present.
- **§2.4 (no implicit precedence).** D2 refuses the member that would have made `scope` and `Target` two
  discriminators for one question.

## Consequences

- **Positive.** Closes ADR-0117 (j) as the port change it said it needed. Gives every future plugin a way
  to say "this describes the Run" without asking core to learn its vocabulary — the same leverage `Level`
  gave, which is what made the warned/failed filter possible.
- **Negative / trade-offs.** A new port field is a permanent surface, and a two-member enum will look
  under-populated until something asks for `SCOPE_STEP`. Producers must be updated to benefit; an
  unstamped plugin's Run metadata stays as invisible as it is today, which is honest but not automatic.
- **Follow-ups.** (1) A richer run-metadata **panel**. What shipped here is the minimum that keeps the
  field from being inert: an "N about this Run" filter in the log header, beside the warned/failed one it
  is modelled on. That makes run-level facts findable in an uncapped stream, which is the §1.8 value (j)
  was after; a dedicated panel — pinned above the log, showing the EE image and its content without a
  click — is presentation, and belongs with the screen it lands on rather than in a port ADR.
  (2) A `SCOPE_STEP` member if and when a Workflow-Step-level statement needs one; it must argue
  membership rather than be added for symmetry.

## Alternatives considered

- **Match on `kind` in the UI, with a registry of well-known kinds.** Rejected: it is the `if ansible{}`
  §1.4 forbids, relocated from core Go into TypeScript, where it is less visible and no less wrong. The
  registry would also have to be maintained by whoever ships a plugin, in a repo they do not own.
- **`bool run_scoped`.** Rejected in D1: cannot express "task, stated" versus "unstated", which is the
  §1.8 distinction the `Level` empty case already established as load-bearing.
- **A well-known `fields` key (`fields["scope"] = "run"`).** Rejected: `fields` is documented as
  structured-but-tool-shaped, so a key the spine reads would be a spine field hiding in a plugin map —
  untyped, unvalidated, and undiscoverable from the proto. Invariant #12 exists to keep diagnostics typed.
- **Infer it: an event with no `Target` and no `item_key` is run-scoped.** Rejected: `stdout` has neither
  and is not run-scoped. Inference from absence is how a wrong signal gets asserted confidently, which is
  the failure §1.8 keeps naming.
