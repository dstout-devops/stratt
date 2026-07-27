-- +goose Up
-- The nesting link (ADR-0139 D2): which WorkflowRun launched this one, and from which Step.
--
-- Charter §2.3 has listed nesting in the definition of a Workflow since day one, and ADR-0011
-- deferred it explicitly rather than dropping it. Everything else on that deferral list has since
-- shipped; this is the remainder.
--
-- WHY A ROW AND NOT AN INLINING. The tempting alternative is to splice a child's Steps into the
-- parent DAG, and it is wrong on three counts that already work on WorkflowRun and would have to be
-- rebuilt: DESCENT (§1.8's ladder is Intent → Workflow → Run → task event, and a Workflow that
-- executed but appears nowhere breaks a rung), GATES (listed and approved per WorkflowRun), and
-- AUDIT. It would also erase the child's own `inputs` Contract, which is the whole point of it
-- being a Workflow rather than a macro.
--
-- Without this link a nested run is an ORPHAN whose existence is only inferable from timing. That
-- is the same shape as a record that lies about a run's state, and it is why nothing else in this
-- ADR is verifiable until a nested run is findable.
--
-- NO FOREIGN KEY, deliberately, and for the reason 00010 gives for graph.finding.baseline: the
-- parent may be pruned by retention while the child's own record is still the audit trail for what
-- ran. A dangling parent id is a navigable dead end; a cascading delete is evidence destroyed.

ALTER TABLE graph.workflow_run
    ADD COLUMN parent_workflow_run_id uuid,
    ADD COLUMN parent_step_name text;

-- Both or neither. A parent id with no Step name cannot say WHERE in the parent it came from, and
-- a Step name with no parent id names a Step in no particular run — each half is unusable alone,
-- and a half-written link is worse than none because it reads as navigable.
ALTER TABLE graph.workflow_run
    ADD CONSTRAINT workflow_run_parent_complete
    CHECK ((parent_workflow_run_id IS NULL) = (parent_step_name IS NULL));

-- Descent walks DOWN from a parent — "show me this run's children" is the read the tree render
-- makes, once per parent node.
CREATE INDEX workflow_run_parent_idx
    ON graph.workflow_run (parent_workflow_run_id)
    WHERE parent_workflow_run_id IS NOT NULL;

-- +goose Down
DROP INDEX graph.workflow_run_parent_idx;
ALTER TABLE graph.workflow_run
    DROP CONSTRAINT workflow_run_parent_complete,
    DROP COLUMN parent_step_name,
    DROP COLUMN parent_workflow_run_id;
