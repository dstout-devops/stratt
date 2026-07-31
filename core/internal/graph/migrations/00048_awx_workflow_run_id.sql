-- +goose Up
-- The AWX façade's workflow_jobs family needs the same reverse lookup migration
-- 00014 gave jobs: an AWX client polls `/api/v2/workflow_jobs/{id}/` with the
-- synthetic integer id, and that must resolve to a WorkflowRun.
--
-- A scan over recent WorkflowRuns was the alternative and it is a WRONG ANSWER
-- generator, not merely a slow one: ListWorkflowRuns caps at 100 rows, so a
-- perfectly live workflow_job just past that horizon would 404. The façade must
-- not tell a client its execution does not exist because the lookup was lazy.
--
-- Reuses graph.awx_run_id (00014) verbatim — same pure function of the uuid,
-- still no mapping table and no new datum (§1.5). Index-only: nothing reads a
-- new column, so this is expand-half-safe on its own (ADR-0078).

CREATE INDEX workflow_run_awx_id_idx ON graph.workflow_run (graph.awx_run_id(id));

-- +goose Down
DROP INDEX IF EXISTS graph.workflow_run_awx_id_idx;
