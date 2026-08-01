# ADR 0157 — Cancelling a WorkflowRun: one writer, no orphans, and a Gate that stops meaning "approve me"

- **Status:** **Proposed** (2026-08-01, steward). Charter review by hand — this session's rules bar the
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
