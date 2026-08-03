# ADR 0157 — Cancelling a WorkflowRun: one writer, no orphans, and a Gate that stops meaning "approve me"

- **Status:** **Accepted** (2026-08-03, steward) — **live-proven on kind**, all three assertions
  including "the K8s Job is gone", now gated by `demo:app-cert` and therefore by E2E-1. Proposed
  2026-08-01. Charter review by hand — this session's rules bar the
  subagent; §1.6/§1.8/§2.1/§2.4/§2.5 answered inline. **No new dependency.**
- **Date:** 2026-08-01
- **Deciders:** steward
- **Charter sections:** §1.6 (one Principal model, one authorization model), §1.8 (never hide
  diagnosis), §2.1 (single write-owner), §2.4 (no implicit precedence), §2.5 (execution authz)
- **Extends ADR-0026** (Run cancellation) to the DAG. **Depends on the Phase-1 fix** shipped in
  `6a50981` — see Context, because it changed what this ADR has to decide.

## Context

`RunAgainstView` has been cancellable since ADR-0026: its cancellation handler runs on a
disconnected context, calls `CleanupRun` to delete the K8s Job on the hub and on each remote Site,
then stamps `canceled` — the Workflow being the single writer of its own terminal status.

`RunDAG` has none of that. There is no `ctx.Err()` check, no disconnected context, and no terminal
write. And there is no native door to cancel a WorkflowRun at all: `/api/v2/jobs/{id}/cancel/` on
the compat surface cancels a single Run, and the `workflow_jobs` equivalent was **deliberately not
shipped** (1d7ffc0) precisely because it would have signalled Temporal and left
`graph.workflow_run` reading `running` forever.

### What implementing it found, and two of these change the decision (2026-08-02)

Recorded here rather than quietly fixed, because the first is a premise this ADR asserted and got
wrong — the reasoning below was built on it.

1. **A Gate does NOT unblock on cancellation.** The Context section states "a Gate Step blocks in
   `sel.Select(ctx)`. On cancellation that returns". It does not. A Temporal `Selector` unblocks
   only on a branch it was given, and `runGateStep` supplies the signal channel and an optional
   timer — nothing selecting on `ctx.Done()`. So a cancelled DAG left that goroutine blocked
   forever: `done` never received, `running` never reached zero, the DAG never reached its own
   cancellation handler, and the execution sat until its Temporal timeout. **The cancel door would
   have returned 202 having stopped nothing** — the "success that did nothing" D6 refuses, arriving
   through the front door instead. Fixed by adding the branch; falsified by removing it again.
   Everything D2 says about `expired` vs `canceled` still holds and is now actually reachable.
2. **D3 was not implementable as written.** It authorizes a cancel with "the `runner` grant on every
   actuation Step's View — exactly the check `api.authorizeLaunch` already applies". At the launch
   door that works; at the cancel door it does not, because a Finding-launched DAG's Steps name no
   View of their own (ADR-0151: the Assignment says WHERE, the recipe does not), the inherited value
   rode `DAGInput` into Temporal, and `graph.workflow_run` kept no copy. The handler would have had
   a spec whose actuation Steps name no View and no default to supply, and `authorizeLaunch`
   correctly refuses that — so **the most ordinary cancel there is, stopping a remediation sitting on
   a Gate, would have failed as a malformed Workflow.** Deriving it from the child Runs' `view_ref`
   fails in exactly that case: a DAG blocked on its Gate has no child Run yet. Migration 00050 adds
   `workflow_run.view_name`, written at launch, before the execution starts.
3. **D5 needed a step outcome it did not name.** "Which Steps completed, which were cancelled in
   flight, and which never started" was three categories over a two-value vocabulary — an
   interrupted Step recorded as `failed`, indistinguishable from one that failed on its own. A
   failed converge reported why it stopped; a cancelled one may be mid-change on a real machine, and
   that is the difference an operator has to act on. Added `canceled` as a Step outcome, and
   nothing new is scheduled once cancellation arrives, so "never started" stays literally true.
4. **D6 shipped as routing, not refusal.** It offered "routes exactly as a Gate decision does, or it
   is refused", preferring refusal until cross-Cell was measured on a two-Cell floor. Routing
   shipped: `CancelRun` already forwards through `forwardWriteToPeers`, and making WorkflowRun
   cancel refuse where Run cancel forwards would invent an asymmetry to express a doubt. What D6
   actually guards against is satisfied — an unreachable home is a loud 503, never a silent 202.
   **The doubt is still unmeasured, and now applies to both paths equally rather than being
   half-mitigated in one of them.**

### Phase 1 changed the shape of this decision

Planning this ADR turned up a live defect and it was fixed first, on purpose, so the design would be
made against the real state rather than the assumed one.

`RunDAG` launches children in three places. ADR-0139 D2 had set
`ParentClosePolicy: REQUEST_CANCEL` on **one** — the nested `RunDAG` — with a comment naming the
hazard exactly. The other two, the actuation Step's `RunAgainstView` and the Action Step's
`RunAction`, took Temporal's `TERMINATE` default. Both have correct cancellation handlers, and
TERMINATE skips them: the pod keeps converging real machines after the DAG is over, and the Run row
reads `running` forever. **That was reachable without any cancel door**, because the policy fires
whenever the parent closes — including on a DAG that merely fails with a Step in flight.

The guard meant to prevent it grepped the file for the constant and passed while two of three sites
were wrong; it now counts sites, and was falsified before being trusted.

**What that leaves this ADR:** cancellation already PROPAGATES correctly to children, and each child
already reaps its own pod and writes its own status. The remaining gap is the **parent's own row**,
its **Gates**, and the **door**.

## Decision

### D1 — `RunDAG` writes its own `canceled`, on a disconnected context

The same shape its children already use, for the same reason: the Workflow is the single writer of
its terminal status (ADR-0026), so the API handler must never write `canceled` itself. Two writers
of one terminal value is the §2.1 hazard this repo enforces structurally everywhere else, and it
fails in the ugliest direction — the handler marks `canceled`, Temporal cancellation loses a race,
and the DAG carries on against a row that says it stopped.

Activities cannot run on a cancelled context, so the handler takes `workflow.NewDisconnectedContext`
before stamping. That is not a subtlety to rediscover: it is why the children's handlers are written
the way they are.

### D2 — a pending Gate is recorded `canceled`, and `expired` would be a lie

A Gate Step blocks in `sel.Select(ctx)`. On cancellation that returns, and the existing code path
computes `status := types.GateExpired` as its default, then tries `RecordGateDecision` on the
cancelled context — which cannot run. So today a cancelled DAG would leave its Gate **pending
forever**: an approval an operator can still act on, for a workflow that is gone.

Two things follow, and the second is the decision:

- The Gate must be recorded terminally, on the disconnected context, as part of D1's handler.
- It must be recorded **`canceled`, a new terminal Gate status** — not `expired`. `expired` means the
  approval window lapsed. Telling an operator that, when the truth is that someone cancelled the
  run, is a wrong answer in the audit record rather than a missing one (§1.8). The Gate record is
  evidence; evidence that misattributes a cause is worse than evidence that says "cancelled".

This adds a value to the Gate status set and therefore to whatever constrains it in the data layer.
Expand-half only (ADR-0078): nothing reads the new value until this ships, and older rows are
untouched.

### D3 — authorization is the `runner` grant that launched it, not a new relation

A cancel is refused unless the Principal holds `runner` on **every actuation Step's View** — exactly
the check `api.authorizeLaunch` already applies at the launch door.

The alternative was a distinct `canceller` relation. It is rejected for the reason ADR-0035 rejects
auto-teaming and ADR-0130 D3 rejects a mirrored grant graph: a second authorization vocabulary for
one plane is a second model to keep in agreement, and §1.6 says there is one. The property that
matters — _you may stop only what you were entitled to start_ — is exactly what reusing the launch
check gives, with no new tuple for an operator to forget.

It is deliberately **not** the Gate's approver set. Approving is a judgement about a change;
cancelling is an operational act on an execution, and conflating them would let an approver stop a
run they could not have launched.

### D4 — cancelling is idempotent, and a finished run is not an error

Cancelling an already-terminal WorkflowRun returns success and does nothing. A client that retries a
cancel — which is exactly what a UI does when the first response is slow — must not receive a 409
that reads as "your cancel failed". The state it asked for is the state that holds.

### D5 — a cancelled DAG leaves partial state, and the summary says so

Cancellation is not rollback and this ADR does not pretend otherwise. Some Steps ran; some never
started; one may have been mid-converge. The estate is genuinely half-applied.

So the WorkflowRun's terminal summary records **which Steps completed, which were cancelled in
flight, and which never started** — the same per-Step map a normal finish writes. Drift detection is
what surfaces the rest, and it will: the next Baseline pass sees a host that does not match its
Assignment and opens a Finding. That is the system working, not a gap. Inventing a rollback would
mean synthesising an inverse for every Actuator, which is a promise no tool-blind spine can keep.

### D6 — cross-Cell cancel routes exactly as a Gate decision does, or it is refused

A `WorkflowRun` carries the `Cell` whose Temporal owns its execution. A cancel must reach that
Temporal, and the Gate-decision path is the precedent to follow — not a second routing rule.

**Honest limit, stated rather than assumed:** this ADR does not assert that today's Gate path
already federates correctly; that needs measuring against a two-Cell floor before either is claimed.
Until it is measured, a cancel for a WorkflowRun homed on a peer Cell must **fail with that reason
named** rather than silently signalling the local Temporal and returning 202 for a workflow that
never heard it. A refusal an operator can read beats a success that did nothing (§1.8).

## Consequences

**The façade route that was withheld becomes shippable.** `workflow_jobs/{id}/cancel/` was declined
in 1d7ffc0 with the reason recorded; D1 removes it, and `can_cancel` stops being a field with no
mechanism behind it.

**A new terminal Gate status** is a small vocabulary addition that the UI, the audit stream and any
Gate listing must handle. Cheap now, and cheaper than teaching them that `expired` sometimes means
cancelled.

**Not addressed:** cancelling a single Step within a running DAG (stop this Step, keep the DAG). That
is a different operation with a different question behind it — what the DAG should then do at that
node — and it belongs with the `when:` semantics rather than here.

**Verification this ADR owes.** A unit test can show the row is stamped and the Gate is recorded. It
cannot show the pod died. The live proof is: launch a multi-Step DAG on kind, cancel mid-Step, and
assert three things — the WorkflowRun reads `canceled`, the child Run reads `canceled`, and **the
K8s Job is gone**. Only the third distinguishes a real cancel from bookkeeping, and it is the one
Phase 1 has made plausible but has not yet demonstrated.

### Live-proven (2026-08-03) — and the third assertion failed first

`demos/app-cert` carries a third guard Workflow,
[`cancel-guard`](../../demos/app-cert/estate/workflows/cancel-guard.yaml): one Step that sleeps on the
target, cancelled mid-run. The demo asserts all three things, plus D5's per-Step summary.
`task demo:app-cert:run` EXIT=0 on kind:

```
1/3 WorkflowRun reads canceled
2/3 child Run reads canceled
3/3 the K8s Job is GONE — the pod was reaped, not just the row stamped
per-Step: sleep-until-cancelled=canceled
```

The Step sleeps on purpose: the first attempt at this proof used the demo's real install Workflow,
whose ansible Step finishes in SEVEN SECONDS, so the cancel arrived with no pod left to reap and the
third assertion had nothing to measure. **A proof that cannot fail is not a proof** — and this one
promptly failed, which is the next section.

### What the third assertion found is older than this ADR

The live proof ran. **Assertions 1 and 2 passed; assertion 3 failed** — and the reason is a shipped
defect that has made **every cancellation since ADR-0026 a lie**, not a gap in anything this ADR
introduced.

The dispatcher Role granted `create` and `get` on `batch/jobs`. `DeleteRunJobs` selects a Run's Jobs
by the `stratt.dev/run-id` **label**, so it must `list` before it can `delete`, and it held neither:

```
ActivityType=CleanupRun Error="dispatch: list run jobs …: jobs.batch is forbidden:
User "system:serviceaccount:stratt:stratt" cannot list resource "jobs" … in namespace "stratt""
```

Measured, not inferred: the guard's Step sleeps 120s on the target; after a cancel its Job read
`Complete, DURATION 2m4s` and was **still present 243 seconds later**. The API returned 202, both
rows read `canceled`, and the pod converged the host to completion regardless.

**Why nobody saw it.** The handler called cleanup as `_ = workflow.ExecuteActivity(…)`, so the RBAC
denial was discarded — it reached the daemon log as an activity error and nothing else. Meanwhile
`TestDeleteRunJobs` passes against a fake clientset that has no RBAC at all, so it proves the label
selector is right, which was never the problem; and `helm lint` renders the Role without knowing
what the Go calls. **The defect lived precisely in the gap between the two tests that both passed.**

Fixed here: the Role grants `list` and `delete`; the cleanup error is carried onto the Run's summary
instead of discarded, so a cancel that cannot reap its pod says so where an operator reads it; and
`TestChartGrantsTheJobVerbsThisPackageCalls` pins the verbs to the calls that need them, falsified by
reverting them.

**This is the clearest vindication of the rule that a seam is not shipped until it is executed.**
ADR-0026's cancellation was reviewed, unit-tested, shipped, and wrong for its entire life, and the
only thing that found it was insisting on the one assertion that could not be satisfied by
bookkeeping.
