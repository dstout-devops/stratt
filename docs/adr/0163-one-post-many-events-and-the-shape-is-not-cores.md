# ADR 0163 — One POST, many events, and the shape is not core's

- **Status:** **Accepted** (2026-08-03, steward) — **live-proven**: one POST in a payload shape
  invented for the demo fans out into five events and crosses a storm threshold, with no Go written
  for that shape. Gated by `demo:network-device` and therefore by E2E-1. See Verification. Charter
  review by hand — this session's rules bar the subagent; §1/§1.4/§1.5/§1.8/§2.4/§2 (vocabulary)
  answered inline. **No new runtime dependency, no migration.**
- **Date:** 2026-08-03
- **Deciders:** steward
- **Charter sections:** §1 (no new configuration languages), §1.4 (boring spine — core authors no
  tool vocabulary), §1.5 (sovereign contracts), §1.8 (never hide diagnosis), §2 (vocabulary is API —
  no tool-specific rendering in a core identifier), §2.4 (no implicit precedence)
- **Extends ADR-0018** (Emitters: `{name, kind, tokenHash}` → events → CEL → launch) by taking the
  one piece of tool vocabulary that ADR ever put in the spine back out of it. Pays the residue
  [ADR-0162](0162-a-trigger-decides-on-more-than-one-event.md) booked verbatim: *"`explode` for
  webhook sources stays core-held. Named here so it is not forgotten."*

## Context

An Emitter turns one inbound POST into events. `webhook` makes one event per POST; `alertmanager`
parses Alertmanager's envelope and fans `alerts[]` out into one event each, folding `receiver` and
`groupLabels` into every payload so a CEL rule can match per alert.

That fan-out is the right BEHAVIOUR. Where it lives is the problem.

### The finding: one tool's vocabulary, in the spine and in the published contract

`emitters.explode()` holds a Go struct with `receiver`, `groupLabels`, `commonLabels`,
`commonAnnotations` and `alerts` in it. Those are **Alertmanager's field names, compiled into the
control plane** — §1.4's line, and the same defect
[ADR-0125](0125-notification-sinks-are-drivers-not-a-core-switch.md) removed on the way OUT
(`deliver()` opened with `if sink.Kind != types.SinkWebhook`) and ADR-0117 (k) removed for Actuators.
The consequence is the familiar one: a Grafana, Sentry, Datadog or NMS webhook whose payload nests
its items anywhere else is a **core change**, in Go, in the deterministic core — which is precisely
how §1.4 says breadth must NOT arrive.

It is also worse than an internal wart, because the kind is not internal:

```yaml
# core/api/openapi.yaml:1826
enum: [webhook, alertmanager]
```

`alertmanager` is a value in the **sovereign OpenAPI contract** (§1.5) and in the generated UI schema
(`ui/src/api/schema.d.ts`). Charter §2 freezes the vocabulary as API and bans tool-specific
renderings in core-model identifiers; a vendor's product name sitting in a published enum is that
ban, broken, in the most visible place available.

### What this must reconcile with, and where the mirror stops

| Machinery                                                                | Bearing on this decision                                                                     |
| ------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------- |
| ADR-0125 — a Sink's `kind` names a driver; core holds no list of kinds    | The outbound mirror, and the precedent for deleting a core-side switch of tool names          |
| ADR-0039 — a `stream` Emitter is a PLUGIN that publishes onto the stream  | Source breadth already arrives as plugin work for OUTBOUND-connecting sources                 |
| ADR-0024 — bindings are explicit field lookup: no operators, no evaluation | The grammar this reuses, rather than inventing a second one                                   |
| ADR-0161 `groupBy`, ADR-0162 `correlateBy`                                | The same shape twice already: a declared key or path, as data, where a language was tempting  |
| ADR-0047 — verify-don't-trust plugin output                               | Why putting a plugin on the ingest path is not free                                           |
| `graph.emitter.spec` is the marshalled declaration as JSONB               | A new declared field needs **no migration**                                                   |

**ADR-0125's mechanism is deliberately NOT copied, and the asymmetry is the interesting part.** A
notification Sink's kind names an Action dispatched to a plugin pod, and that is correct there:
delivery is already asynchronous, already retried, and a slow driver delays a message nobody is
holding a connection open for. Ingest is the opposite surface. It answers a live HTTP request from a
system that will drop the alert if we do not take it, and its whole job is to accept reliably. Making
every POST wait on a plugin round-trip would put a third party on the hot path of the one endpoint
whose reliability is the product — and would mean an alert storm arriving while a plugin pod is
rescheduling is an alert storm lost.

So the fan-out stays in core. What leaves core is **knowing any particular tool's shape**.

## Decision

### D1 — The fan-out is DECLARED as data on the Emitter; core holds no payload shapes

```yaml
name: alerts
kind: webhook
tokenHash: <hex(sha256(token))>
explode:
  path: alerts # the array to fan out — one event per entry
  merge: # envelope fields folded into every event
    - { path: receiver }
    - { path: groupLabels }
```

Absent `explode`, one POST is one event — today's `webhook`, unchanged.

**This is not a new configuration language** (§1 non-goal), and the test is that nothing here
evaluates. `path` is a dotted lookup, exactly ADR-0024's blessed binding: no operators, no
expressions, no computation. It is the third time this shape has answered a "we need to express
something about a payload" question — `groupBy` (ADR-0161) and `correlateBy` (ADR-0162) are the other
two — and reaching for the same grammar a third time is the point rather than a coincidence.

**Core's remaining knowledge of any source is: JSON has arrays and objects.** That is spine
knowledge, not tool vocabulary.

### D2 — `alertmanager` stops being a kind, including in the published contract

`kind` is `webhook | stream`. The Alertmanager shape becomes four lines an estate declares, shipped
as a reference example rather than as a Go struct, and the enum in `openapi.yaml` loses a vendor's
product name (§2, §1.5).

**Nothing in this repo's estates declares `kind: alertmanager`** — the blast radius is one test and
one enum. Existing declarations are still accepted: parse-time normalization rewrites the kind into
the equivalent `explode` block, in ONE place, deletable when it has aged out. That normalization is
not a compatibility shim to be taken on trust — **the equivalence is provable and is proven**: the
same POST through both paths must produce byte-identical events (see Verification).

### D3 — `merge` is EXPLICIT, and a collision is refused rather than resolved

Two things this does not do, each for the same reason:

- **It does not merge "everything except the array".** An implicit merge means the source adding one
  top-level field silently changes the payload every rule in the estate matches against. The estate
  says which envelope fields it wants; nothing arrives because somebody else's release notes said so.
- **It does not pick a winner when a merged key collides with a key the item already has.** This is
  not hypothetical: Alertmanager's envelope has `status` and so does every alert inside it, meaning
  the obvious declaration collides on the first POST. Choosing item-wins or envelope-wins would be
  exactly the implicit precedence §2.4 exists to refuse, and the loser would be invisible.

A collision is **refused at ingest, naming the key** (§1.8), and `as:` is how the estate resolves it:

```yaml
merge:
  - { path: status, as: groupStatus } # the envelope's, kept apart from the alert's own
```

Refusing costs a 400 on a POST whose declaration is wrong. That is worse than silently dropping a
field and much better than inventing a winner — and unlike both, it is fixable by the person who
declared it.

### D4 — What is NOT decided here, stated precisely so the claim is not overread

**A new webhook source needs no core change — for its BODY. Its AUTHENTICATION is still core-held
and single-shaped, and that is a real remaining gap.**

Ingest authenticates one way: a shared token in `X-Stratt-Emitter-Token`, compared against
`hex(sha256(token))`. Alertmanager can send that. **GitHub, GitLab and Stripe cannot** — they sign
the request body with an HMAC in their own header, so no declaration in this ADR makes them reachable.

This is booked rather than solved because it is a different decision with a different shape (a
verification step over raw bytes, and a secret core must hold to check a signature — §2.5 territory),
and folding it in would produce one change nobody can review. Until it lands, "any webhook source"
means "any webhook source that can send a shared token in a header we name", and this ADR does not
claim otherwise.

### D5 — A transform LANGUAGE is refused, not deferred

JQ, JSONPath filters, or CEL over the request body would each answer this and every future variant of
it. All three are the §1 non-goal, and there is a second reason beyond the charter's: an expression
that reshapes a payload makes the event's shape a computation, so "what does `event.x` mean here?"
stops being answerable by reading a declaration. A path plus a merge list is legible to somebody who
has never seen Stratt before.

## Consequences

- **A source whose shape core has never heard of arrives as an estate declaration**, which is what
  §1.4 promises and what "source breadth" was actually short of.
- **The OpenAPI enum changes**, which regenerates `server.gen.go` and the UI schema. A published
  contract narrowing is the cost of getting a vendor's name out of it, and it is cheap exactly now:
  nothing declares the value.
- **`explode()` stops being a switch and becomes a walk**, which means it can now fail on a
  declaration rather than only on malformed JSON: a path addressing nothing, or addressing something
  that is not an array, must be refused visibly rather than quietly yielding one event (§1.8).
- **The ingest path stays synchronous and plugin-free**, deliberately (see the asymmetry above).
- **Webhook authentication remains single-shaped** (D4) and is the next decision in this arc.

## Verification

Not shippable on assertion. This ADR owes:

- unit: the declared Alertmanager form produces **byte-identical events** to the hardcoded path it
  replaces — the migration's entire claim, and the one thing a reader has to be able to check;
- unit: an Emitter with no `explode` yields exactly one event per POST (the regression that matters —
  every shipped Emitter is that case);
- unit: a `path` addressing nothing, or addressing a non-array, is **refused with a message naming
  the path** — never silently one event, which is the failure mode that would look like it worked;
- unit: a merge collision is refused and names the colliding key; `as:` resolves it;
- **live**: a single POST carrying five items fans out into five events and the ADR-0162 storm
  Trigger fires **exactly once** — which asserts the fan-out from the estate's own Runs rather than
  from a log line, and fails loudly in both directions: no fan-out means one event and NO Run.

### All of it, paid (2026-08-03)

`demos/network-device` gains `emitters/nms-batch.yaml`, whose payload shape was invented for that
file — items under `report.linkEvents`, a `site` in the envelope, a `batchId` beside it, and a
`status` at both levels. **No Go was written for it.** `task demo:network-device:run` EXIT=0:

```
demo: post ONE batched report in a shape core has never heard of, and assert it fans out
  (0 rtr-batch-remediate Run(s) already on this floor; counting the delta)
  one POST became five events and crossed the threshold — exactly ONE Run
  …and its own marker route 10.96.0.0/24 is on the device — its own evidence, not the storm's
```

The assertion fails in both directions, which is why it is worth running: no fan-out means that POST
is ONE event, `count: 5` is never reached, and NO Run exists. The marker route is read back off the
device because a Run that launched and did nothing would satisfy the count.

**Falsified**, each mechanism removed in turn: a bad path degrading quietly to the un-exploded body
(the failure that looks like success), a merge overwriting silently instead of refusing, the
normalization losing its merge list, and each of the two API doors dropping or hiding the
declaration. **The door guards did not falsify on the first attempt** — nothing tested them, which is
exactly why both doors had silently dropped the field when it was added. That gap was the finding;
the tests came second.

### What running it taught, both about the demo rather than the feature

- **`demo:network-device:down` deliberately leaves the floor standing**, so Postgres survives a
  teardown and a second run inherits the first one's Runs. The storm assertion demanded a prior count
  of zero and therefore passed only on a never-used cluster. Both assertions now measure the DELTA
  this burst caused, which is the honest measurement anyway.
- **Each run is now its own correlated burst** (`correlateBy: event.burst`), because a re-run inside
  the ten-minute window would otherwise inherit a count it did not earn and fire on its first flap —
  a false pass indistinguishable from a true one. A side effect worth having: `correlateBy` is now
  live-proven rather than only unit-tested, and the batch Trigger correlates on a field that reached
  the event through this ADR's own `merge`.
