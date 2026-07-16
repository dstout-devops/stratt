-- +goose Up
-- Cell homing for WorkflowRuns (ADR-0044 slice 5). A WorkflowRun's Gate waits
-- and cancel signals target the Temporal execution in ITS Cell's namespace
-- (stratt-<cell>); a gate decision or cancel that landed on the wrong Cell would
-- signal the wrong namespace (silently, if a same-named execution existed).
-- graph.run got its `cell` in 00026; the WorkflowRun needs the same residency so
-- DecideGate can point-forward to the owning Cell. Set once at creation (= the
-- creating daemon's Cell); 'local' backfills every existing row, a no-op for a
-- single-Cell estate.
ALTER TABLE graph.workflow_run ADD COLUMN cell text NOT NULL DEFAULT 'local';

-- +goose Down
ALTER TABLE graph.workflow_run DROP COLUMN cell;
