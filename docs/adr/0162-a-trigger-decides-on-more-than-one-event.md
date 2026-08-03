# ADR 0162 — A Trigger decides on more than one event

- **Status:** **Accepted** (2026-08-03, steward) — **live-proven**: nine link-flap events against a
  real device produced **exactly one** Run, counted from the estate's own WorkflowRuns. Gated by
  `demo:network-device` and therefore by E2E-1. See Verification. Charter review by hand — this
  session's rules bar the subagent; §1/§1.2/§1.4/§1.8/§2.4/§9 answered inline. **No new runtime
  dependency.**
- **Date:** 2026-08-03
- **Deciders:** steward
- **Charter sections:** §1 (no new configuration languages), §1.2 (projections, never a second
  truth), §1.4 (boring spine), §1.8 (never hide diagnosis), §2.4 (no implicit precedence), §9 (no
  ontology creep)
- **Extends ADR-0018** (the Trigger engine: Emitter × CEL → launch) with the case it did not answer:
  a decision that depends on events ALREADY SEEN. Follows **ADR-0160**'s standard — reproduce the
  BEHAVIOUR, not the mechanism.

## Context

Event-Driven Ansible is the last named AAP product where Stratt is materially behind. The audit that
produced this ADR corrected two claims about that gap before it found the real one.

### What the parity register got wrong

- **"Throttling — dedup only."** False. `CooldownSeconds` is declared, enforced, and shipped
  (`triggerengine/engine.go`). The real limitation is different: the bookkeeping is a **map in one
  process**, so it resets on restart and does not hold across replicas.
- **"Source breadth — 3 kinds vs dozens."** This compares Stratt's `kind` enum against AAP's source
  LIBRARY. A `stream` Emitter is a **plugin** that outbound-connects and publishes onto the emitter
  stream itself — salt already does it (ADR-0039). A Kafka or SQS source needs **no core change**;
  it is plugin-authoring, which is precisely how §1.4 says breadth arrives. What core does still hold
  is the `explode` for webhook-shaped sources, where `alertmanager` is hardcoded — small, real, and
  not what "dozens of sources" implied.
- **"Rulebook format."** A packaging difference, not a capability gap. The engine evaluates EVERY
  Trigger against every event and fires every match, which is what a ruleset does. AAP binds sources
  and rules in one file; Stratt has reusable Emitters plus Triggers, which is the better factoring
  and is not worth trading away for file-shape fidelity.

### The real gap, stated as behaviour

An operator can express "when THIS event arrives, do that". They cannot express:

- **"when this keeps happening"** — five link-flaps in ten minutes is an incident; one is noise. This
  is the actual reason people run EDA, and today every flap launches a Run.
- **"when both of these have happened"** — a deploy finished AND the health check failed.

Both are one question — *given the events I have already seen, should this Trigger fire?* — and both
need the same thing: state about the recent past. Cooldown already needs it and already has it, in
the weakest possible form.

### What this ADR refuses to copy

AAP's mechanism is a rules engine with a **working memory**: `set_fact` writes facts, conditions
match against them, `retract_fact` removes them. Reproducing that would mean a second store of facts
about the estate that is not a projection, has no provenance, and is invisible to drift — §1.2's line,
crossed for the sake of resembling somebody else's implementation.

The BEHAVIOUR people use working memory for is the two bullets above. Those are expressible without
one, and that is what this ADR does.

## Decision

### D1 — The unit of decision is unchanged: one Trigger, one CEL, one target

No rulebook file, no ordered rulesets, no first-match precedence. CEL still answers exactly one
question — *does THIS event match?* — and it still sees one event.

What changes is that a Trigger may additionally declare a **pattern over matching events**. The
matching stays a pure function of one event; the counting is the engine's.

**This is what keeps §1 intact.** A condition language that could say `count(...) > 5` would be a new
configuration language with state and aggregation. A Trigger that declares `within: 10m, count: 5`
beside its existing `when:` is DATA — the same shape `cooldownSeconds` already is, and the same shape
ADR-0161 used for `groupBy` rather than growing an expression evaluator.

### D2 — Trigger decision state is DURABLE and CROSS-REPLICA

The in-memory `lastFire` map becomes a row per (trigger, correlation key) in Postgres — the boring
spine's existing store (§1.4), not a new dependency.

**This is a bug fix before it is a feature.** Cooldown today silently stops working when a pod
restarts, and two replicas each keep their own idea of when a Trigger last fired — so the storm
damping an operator declared is not the storm damping they get. Nothing in the estate says so.

**And it makes the state READABLE, which AAP's cannot be.** A rules engine's working memory is
opaque; a table is not. "Why did this Trigger not fire?" becomes answerable — the window, the count
so far, and the last fire are all things an operator can be shown (§1.8). That is the parity
argument turned around: same behaviour, better diagnosis.

### D3 — `within` + `count`: act on a storm, not on an alert

A Trigger may declare a window and a threshold. It fires when the count of MATCHING events inside the
window reaches the threshold, and the window then resets.

```yaml
when: "event.labels.alertname == 'LinkFlap'"
within: 10m
count: 5 # ⇒ fire once when the fifth flap arrives inside ten minutes
```

Absent `count` (or `count: 1`) is today's behaviour exactly: fire on every match.

**Reset, not slide, and the choice is deliberate.** A sliding window fires on the 5th, 6th, 7th…
event — a storm produces a storm of Runs, which is the problem being solved. Resetting means one Run
per five flaps, and the operator can express "keep telling me" by pairing it with a short window
rather than by getting it as a default they did not ask for.

### D4 — `allOf` + `correlateBy`: act when several things have happened

A Trigger may declare several conditions and the field that ties events together. It fires when every
condition has been satisfied by some event sharing one correlation value, inside the window.

```yaml
allOf:
  - "event.kind == 'deploy.finished'"
  - "event.kind == 'healthcheck.failed'"
correlateBy: event.service # both must be about the SAME service
within: 15m
```

**`correlateBy` is REQUIRED with `allOf`, and that is the interesting constraint.** Without it, "a
deploy finished somewhere and a health check failed somewhere" fires — which is not what anybody
means and is a very good way to converge the wrong estate at 3 a.m. AAP's `all()` has this hazard and
leaves it to the author; here the correlation key is not optional, so the mistake is unavailable.

**An event carrying no value for the key participates in NO window** — it is excluded, not bucketed.
This is the second half of the guarantee and it is easy to get wrong: pooling uncorrelated events
under one empty key would rebuild the exact hazard the required field removes, among precisely the
events we know least about. `correlateBy` is spelled `event.service`, the same namespace `when:`
reads, and the prefix is required rather than optional for the same reason — a bare `service` would
address a top-level key for one payload shape and nothing at all for another, and addressing nothing
is silent non-participation (§1.8).

`allOf` and `count` are mutually exclusive: "five of these" and "one of each of those" are different
questions, and a Trigger that declared both would need a rule to combine them (§2.4).

### D5 — This state is NOT a projection, and the distinction is load-bearing

Window state describes **the event stream**, not the estate. It is transient by construction (it
expires), rebuildable by definition (it is derived from events that are themselves durable on
JetStream), and no Entity attribute is ever written from it. §1.2 governs facts about the ESTATE;
this is bookkeeping about the engine's own recent past, in the same category as a Temporal timer.

The line to hold: **nothing in this state may ever be readable as a fact about a host.** If a future
change wants "the host has flapped five times" to be queryable, that is a Facet with a Normalizer and
provenance, not a widened trigger table.

### D6 — What is declined, each for a stated reason

- **`set_fact` / `retract_fact` working memory** — refused, D5's line. The behaviours it serves are
  D3 and D4; a general fact store is a second truth with no owner.
- **`post_event`** — declined as a core mechanism because it already exists at the edge: an Emitter
  plugin can publish onto the emitter stream, which is exactly what chaining means here. Adding a
  core action to re-enter the engine would build a loop the core has to detect and bound.
- **Ordered rulesets / first-match** — declined. All-match is what ships, it has no precedence
  question to answer, and §2.4 is happier for it.
- **A rulebook file format** — declined. It is file-shape fidelity, and it would cost the reusable
  Emitter.

## Consequences

- **Cooldown starts working correctly**, including across replicas and restarts. Estates that already
  declare it get a fix they did not ask for and would not have detected.
- **One new table**, expiring, with no foreign key into the graph — deliberately separate so it can
  never be joined into an estate query and mistaken for a fact about a host (D5). "Expiring" is a
  swept cadence in `strattd`, not a property of the schema: a Trigger correlating on a
  high-cardinality field leaves a row per value behind, so an unwired sweep would grow the table
  without bound while every declaration still looked correct. The retention is far longer than any
  sensible window on purpose — a sweep that could delete a LIVE window would reset a storm count
  mid-storm, which is the failure this ADR exists to prevent.
- **The engine gains a read-modify-write per matching event.** It is per (trigger, key) and bounded
  by the window, but it is no longer a pure function — the load characteristics change, and a storm
  is exactly when it is under most pressure.
- **`explode` for webhook sources stays core-held.** Named here so it is not forgotten; it is a
  separate, smaller decision than this one.

## Verification

Not shippable on assertion. This ADR owes:

- unit: `count` fires on the Nth match and not before; the window resets after firing; `correlateBy`
  keeps two services' events apart; `allOf` does not fire until every condition is satisfied by the
  SAME key; absent `count`/`allOf` behaves exactly as today (the regression that matters most —
  every shipped Trigger is that case);
- unit: cooldown survives a simulated restart, which is the current defect;
- **live**: a demo that posts a burst of events to an Emitter and asserts **exactly one Run** was
  launched, counted from the Runs the estate actually has rather than from the engine's own log.
  "The storm was damped" is precisely the class of claim this repo has repeatedly found false when
  executed.

### All of it, paid (2026-08-03)

`demos/network-device` gains a webhook Emitter, a `count: 5, withinSeconds: 600` Trigger, and the
Workflow it fires. Nothing else in that estate launches `rtr-flap-remediate`, so **the number of its
WorkflowRuns IS the number of times the engine decided a storm had happened** — the assertion reads
that number off `/workflow-runs`, never off a log line saying "accumulating", which would be true
whether or not anything launched. `task demo:network-device:run` EXIT=0:

```
demo: post a burst of link-flap events and assert the storm is damped to ONE Run
  four flaps matched the rule and launched NOTHING — below the threshold
  the fifth flap launched it — the storm was recognised
  nine flaps in total, and still exactly ONE Run — the window reset, it did not slide
  …and the device's own running-config carries the remediation route 10.97.0.0/24
```

The last line is there because a damped storm that launched a Run which did nothing would satisfy
every count above it. The nine-flap tail is D3's reset asserted directly: a sliding window would have
fired again on the 6th, 7th, 8th and 9th.

**Falsified**, each mechanism removed in turn and the matching guard confirmed to fail: no count
threshold (every flap fires), uncorrelated events pooled under one key (the `somewhere and somewhere`
hazard returns), correlation ignored entirely, the fire not persisted (the cooldown stops surviving a
restart), and `satisfied` appended without the containment check (one condition repeated completes a
two-condition pattern). The restart case is exercised by building a SECOND Engine over the same
store — which is both a restarted pod and a second replica, the two situations D2 exists for.

### Two things the implementation corrected in this ADR

- **`correlateBy` did not work as this document first spelled it.** The examples say
  `correlateBy: event.service`, matching how `when:` addresses the payload, but the path walker read
  the payload directly — so `event` addressed nothing and every event fell into one shared window.
  That is not a cosmetic mismatch: it is exactly the hazard D4 exists to remove, arrived at silently
  through the spelling the ADR itself recommends. The prefix is now required and stripped, and an
  event with no value for the key **participates in no window at all** (D4).
- **The sweep was written and never wired.** The Consequences called the table expiring while nothing
  called `TriggerWindowSweep` — a bound that existed only in prose. It is now a cadence in `strattd`.
