-- ADR-0157 D2: a Gate on a cancelled WorkflowRun is recorded `canceled`, and `expired` would lie.
--
-- A Gate Step blocks in `sel.Select(ctx)`. On cancellation that returns and the existing code path
-- computes `status := types.GateExpired` as its DEFAULT — the value it falls to when neither
-- approved nor denied. So a cancelled DAG would either record `expired`, which says the approval
-- window lapsed when the truth is that someone stopped the run, or (today, because the activity
-- cannot run on a cancelled context) record nothing at all and leave the Gate PENDING forever: an
-- approval an operator can still act on, for a workflow that is gone.
--
-- The Gate record is EVIDENCE. Evidence that misattributes a cause is worse than evidence that says
-- "cancelled" (§1.8), which is why this is a new value rather than a reuse of `expired`.
--
-- expand/contract-ok: the DROP/ADD pair below WIDENS the allowed set — it adds `canceled` and
-- removes nothing. UPG-1 exists to stop a rolling upgrade breaking the PREVIOUS release's still-
-- running replicas, and those replicas write only 'pending'/'approved'/'denied'/'expired', every
-- one of which stays valid under the new constraint. There is no value any live replica can write
-- that this makes illegal, and no row that becomes invalid. Postgres has no ALTER CONSTRAINT for a
-- CHECK, so drop-and-add is the only way to widen one; the marker acknowledges the shape, not a
-- destructive effect. This is the EXPAND half in ADR-0078's sense: nothing reads `canceled` until
-- the RunDAG handler shipping alongside it writes one, and older rows are untouched.

-- +goose Up
ALTER TABLE graph.gate DROP CONSTRAINT gate_status_check;
ALTER TABLE graph.gate ADD CONSTRAINT gate_status_check
    CHECK (status IN ('pending', 'approved', 'denied', 'expired', 'canceled'));

-- +goose Down
-- Narrowing back would reject rows this release legitimately wrote, so they are folded to
-- `expired` first. That is lossy and known: `expired` is the closest pre-existing terminal value,
-- and a Down that failed on its own data would be worse than one that says what it flattened.
UPDATE graph.gate SET status = 'expired' WHERE status = 'canceled';
ALTER TABLE graph.gate DROP CONSTRAINT gate_status_check;
ALTER TABLE graph.gate ADD CONSTRAINT gate_status_check
    CHECK (status IN ('pending', 'approved', 'denied', 'expired'));
