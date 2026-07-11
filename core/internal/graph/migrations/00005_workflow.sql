-- +goose Up
-- Workflows (charter §2, ADR-0011): Temporal-backed DAGs of Steps with
-- Gates. graph.workflow is the projection of the Git declaration (CaC-only
-- in v1, mirroring graph.trigger); workflow_run and gate are execution
-- records — the Workflow → Run rung of the §1.8 descent ladder.
CREATE TABLE graph.workflow (
    name       text PRIMARY KEY,
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER workflow_touch_updated_at
    BEFORE UPDATE ON graph.workflow
    FOR EACH ROW EXECUTE FUNCTION graph.touch_updated_at();

CREATE TABLE graph.workflow_run (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_name text NOT NULL,
    temporal_id   text NOT NULL DEFAULT '',
    status        text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'canceled')),
    -- Launching Principal id (never material, §2.5); '' = anonymous.
    principal   text NOT NULL DEFAULT '',
    -- Terminal per-Step summary: {step -> {status, runId|gateId}}.
    summary     jsonb NOT NULL DEFAULT '{}'::jsonb,
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);

CREATE TABLE graph.gate (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_run_id uuid NOT NULL REFERENCES graph.workflow_run (id) ON DELETE CASCADE,
    step            text NOT NULL,
    status          text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'denied', 'expired')),
    -- The declared approver policy, pinned at Gate creation (audit: the
    -- policy that authorized the decision, even if the Workflow changes).
    approvers  jsonb NOT NULL,
    decided_by text NOT NULL DEFAULT '',
    note       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    -- One live Gate per Step per WorkflowRun.
    UNIQUE (workflow_run_id, step)
);

-- Workflow -> Run descent (§1.8): which WorkflowRun/Step spawned this Run.
-- Null for direct API launches and Trigger-fired single-Step Runs.
ALTER TABLE graph.run ADD COLUMN workflow_run_id uuid;
ALTER TABLE graph.run ADD COLUMN step_name text;

-- +goose Down
ALTER TABLE graph.run DROP COLUMN step_name;
ALTER TABLE graph.run DROP COLUMN workflow_run_id;
DROP TABLE graph.gate;
DROP TABLE graph.workflow_run;
DROP TABLE graph.workflow;
